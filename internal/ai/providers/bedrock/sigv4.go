package bedrock

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4, hand-rolled on stdlib crypto so that pigo keeps its
// zero-dependency guarantee. The algorithm is fully specified and stable; the
// SDK is 200 packages for what fits in one file.
//
// The parts that bite:
//   - The canonical request hashes the *payload*, so a streaming body has to be
//     buffered. Bedrock request bodies are small, so this is fine.
//   - Header names are lower-cased and values trimmed, but a value's internal
//     spacing must be collapsed too, or the signature will not match.
//   - The credential scope date is UTC and must agree with the x-amz-date
//     header to the day, or the request is rejected as out of scope.

const (
	algorithm       = "AWS4-HMAC-SHA256"
	terminator      = "aws4_request"
	amzDateFormat   = "20060102T150405Z"
	scopeDateFormat = "20060102"
)

// Credentials are the AWS keys used to sign a request.
//
// SessionToken is set for temporary credentials — anything from STS, an
// instance role, or SSO — and must be sent as a header as well as signed.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// valid reports whether the credentials can sign anything.
func (c Credentials) valid() bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// signer signs requests for one service in one region.
type signer struct {
	region  string
	service string
}

// sign adds the SigV4 authorization headers to a request.
//
// body is the payload as it will be sent; it is hashed rather than read from
// the request, so the caller keeps ownership of it.
func (s signer) sign(req *http.Request, body []byte, creds Credentials, now time.Time) error {
	if !creds.valid() {
		return fmt.Errorf("bedrock: incomplete AWS credentials")
	}

	now = now.UTC()
	amzDate := now.Format(amzDateFormat)
	scopeDate := now.Format(scopeDateFormat)

	// The host header is not in Header for an outgoing request; SigV4 requires
	// it in the signature, so it is set explicitly.
	req.Header.Set("host", req.URL.Host)
	req.Header.Set("x-amz-date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("x-amz-security-token", creds.SessionToken)
	}

	payloadHash := hexSHA256(body)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	canonicalRequest, signedHeaders := canonicalRequest(req, payloadHash)

	scope := strings.Join([]string{scopeDate, s.region, s.service, terminator}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(s.signingKey(creds, scopeDate), stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, creds.AccessKeyID, scope, signedHeaders, signature,
	))

	return nil
}

// signingKey derives the date/region/service-scoped key.
func (s signer) signingKey(creds Credentials, scopeDate string) []byte {
	key := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), scopeDate)
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, s.service)
	return hmacSHA256(key, terminator)
}

// canonicalRequest renders the request in SigV4's canonical form and returns
// it along with the signed-header list.
func canonicalRequest(req *http.Request, payloadHash string) (string, string) {
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		lowered := strings.ToLower(name)
		// Authorization is what is being produced, and the two below are set
		// by proxies and intermediaries after signing.
		switch lowered {
		case "authorization", "content-length", "user-agent":
			continue
		}
		names = append(names, lowered)
	}
	sort.Strings(names)

	var headers strings.Builder
	for _, name := range names {
		headers.WriteString(name)
		headers.WriteByte(':')
		headers.WriteString(canonicalHeaderValue(req.Header.Get(name)))
		headers.WriteByte('\n')
	}

	signedHeaders := strings.Join(names, ";")

	return strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL),
		headers.String(),
		signedHeaders,
		payloadHash,
	}, "\n"), signedHeaders
}

// canonicalHeaderValue trims a header and collapses its internal runs of
// spaces, which the specification requires and which is easy to miss: a value
// with two spaces signs differently from one with one.
func canonicalHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	for strings.Contains(value, "  ") {
		value = strings.ReplaceAll(value, "  ", " ")
	}
	return value
}

// canonicalPath renders the URI path.
//
// Bedrock model ids contain slashes and colons ("anthropic.claude-v2:1",
// "us.anthropic.claude-opus-5"), so the path is double-encoded: URL.EscapedPath
// has already escaped once, and SigV4 wants the escaping of that.
func canonicalPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}

	segments := strings.Split(path, "/")
	for i, segment := range segments {
		// Un-escape then re-escape twice: what reaches the wire is escaped
		// once, and the canonical form escapes it again.
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			decoded = segment
		}
		segments[i] = escapePathSegment(escapePathSegment(decoded))
	}
	return strings.Join(segments, "/")
}

// escapePathSegment percent-encodes everything outside SigV4's unreserved set.
//
// url.PathEscape is not usable here: it leaves characters such as ':' and '$'
// alone, and AWS expects them encoded.
func escapePathSegment(segment string) string {
	var b strings.Builder
	for i := range len(segment) {
		c := segment[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// canonicalQuery renders the query string with parameters sorted by name and
// then value, each percent-encoded the same way as a path segment.
func canonicalQuery(u *url.URL) string {
	values := u.Query()
	if len(values) == 0 {
		return ""
	}

	type pair struct{ key, value string }
	var pairs []pair
	for key, vs := range values {
		for _, v := range vs {
			pairs = append(pairs, pair{escapePathSegment(key), escapePathSegment(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].value < pairs[j].value
	})

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.key+"="+p.value)
	}
	return strings.Join(parts, "&")
}

// isUnreserved reports whether a byte may appear unencoded, per RFC 3986's
// unreserved set, which is what SigV4 uses.
func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.', c == '~':
		return true
	}
	return false
}

// hmacSHA256 computes an HMAC.
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// hexSHA256 hashes data and hex-encodes it.
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
