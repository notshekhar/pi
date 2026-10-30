package providerutil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A body that goes silent fails with ErrStreamStalled; one that keeps
// trickling bytes, however slowly, is never cut off.
func TestSendAbandonsASilentBody(t *testing.T) {
	old := StreamIdleTimeout
	StreamIdleTimeout = 60 * time.Millisecond
	t.Cleanup(func() { StreamIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush := w.(http.Flusher).Flush
		if r.URL.Path == "/trickle" {
			for range 6 {
				_, _ = io.WriteString(w, ": keep-alive\n\n")
				flush()
				time.Sleep(30 * time.Millisecond)
			}
			return
		}
		_, _ = io.WriteString(w, "data: first\n\n")
		flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	read := func(path string) ([]byte, error) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		resp, err := Send(DefaultHTTPClient, req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}

	start := time.Now()
	got, err := read("/silent")
	if !errors.Is(err, ErrStreamStalled) {
		t.Fatalf("silent body: err = %v", err)
	}
	if string(got) != "data: first\n\n" || time.Since(start) > 2*time.Second {
		t.Errorf("silent body: got %q after %v", got, time.Since(start))
	}
	if _, err := read("/trickle"); err != nil {
		t.Errorf("a trickling body was cut off: %v", err)
	}
}
