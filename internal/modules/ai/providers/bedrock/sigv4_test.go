package bedrock

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignIsDeterministic(t *testing.T) {
	s := signer{region: "us-east-1", service: "bedrock"}
	creds := Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	body := []byte(`{"messages":[]}`)

	req1, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/converse", nil)
	req2, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/converse", nil)

	if err := s.sign(req1, body, creds, now); err != nil {
		t.Fatal(err)
	}
	if err := s.sign(req2, body, creds, now); err != nil {
		t.Fatal(err)
	}

	if req1.Header.Get("Authorization") != req2.Header.Get("Authorization") {
		t.Fatal("same input must produce the same signature")
	}
	if req1.Header.Get("x-amz-date") != "20240102T030405Z" {
		t.Errorf("x-amz-date = %q", req1.Header.Get("x-amz-date"))
	}
	if !strings.Contains(req1.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		t.Errorf("authorization = %q", req1.Header.Get("Authorization"))
	}
}

func TestSignChangesWithBody(t *testing.T) {
	s := signer{region: "us-east-1", service: "bedrock"}
	creds := Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"}
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	req1, _ := http.NewRequest(http.MethodPost, "https://example.com/model/x/converse", nil)
	req2, _ := http.NewRequest(http.MethodPost, "https://example.com/model/x/converse", nil)
	_ = s.sign(req1, []byte(`{"a":1}`), creds, now)
	_ = s.sign(req2, []byte(`{"a":2}`), creds, now)

	if req1.Header.Get("Authorization") == req2.Header.Get("Authorization") {
		t.Fatal("different bodies must produce different signatures")
	}
}

func TestSignIncludesSessionToken(t *testing.T) {
	s := signer{region: "us-east-1", service: "bedrock"}
	creds := Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "session-token",
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/x", nil)
	if err := s.sign(req, nil, creds, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-amz-security-token") != "session-token" {
		t.Errorf("token = %q", req.Header.Get("x-amz-security-token"))
	}
}

func TestCanonicalPathDoubleEncodesColon(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost,
		"https://example.com/model/"+escapePathSegment("anthropic.claude-v2:1")+"/converse", nil)
	path := canonicalPath(req.URL)
	if !strings.Contains(path, "%253A") {
		t.Errorf("canonical path = %q, want a double-encoded colon", path)
	}
}

func TestCanonicalHeaderValueCollapsesSpaces(t *testing.T) {
	if got := canonicalHeaderValue("  a   b  "); got != "a b" {
		t.Errorf("got %q", got)
	}
}
