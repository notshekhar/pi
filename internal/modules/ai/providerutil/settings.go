package providerutil

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// LoadAPIKey resolves a credential from an explicit value or an environment
// variable, and reports a usable error naming the variable when neither is set.
func LoadAPIKey(explicit, envVar, description string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v, nil
	}
	return "", &provider.LoadAPIKeyError{
		Message: fmt.Sprintf(
			"%s API key is missing: pass it explicitly or set the %s environment variable",
			description, envVar,
		),
	}
}

// LoadSetting resolves a non-secret setting from an explicit value or the
// environment, returning the fallback when neither is set.
func LoadSetting(explicit, envVar, fallback string) string {
	if explicit != "" {
		return explicit
	}
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return fallback
}

// idAlphabet is lowercase alphanumerics, which every provider accepts in an
// id and which survive logs and URLs unescaped.
const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// GenerateID returns a random identifier with the given prefix, for
// synthesising ids that a provider did not supply.
func GenerateID(prefix string, length int) string {
	if length <= 0 {
		length = 16
	}
	buf := make([]byte, length)
	// crypto/rand.Read never fails on supported platforms.
	if _, err := rand.Read(buf); err != nil {
		panic("providerutil: random source unavailable: " + err.Error())
	}
	for i, b := range buf {
		buf[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	if prefix == "" {
		return string(buf)
	}
	return prefix + "_" + string(buf)
}

// TrimTrailingSlash normalises a base URL so path joining is predictable.
func TrimTrailingSlash(url string) string {
	return strings.TrimRight(url, "/")
}
