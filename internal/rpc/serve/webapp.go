package serve

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The UI serve hosts: the desktop app's own React app (loop apps/web), whose
// browser mode speaks /ws. First match wins:
//  1. LOOP_WEB_APP_DIR (a build directory);
//  2. a pack file (loop's web-app-pack format) at LOOP_WEB_APP_PACK, or
//     ~/.loop/web-app.pack;
//  3. a loop checkout's apps/web/dist next to this repo (development).

// webApp reads one file by URL path, or nil.
type webApp interface {
	read(p string) []byte
}

type dirApp string

func (d dirApp) read(p string) []byte {
	root, _ := filepath.Abs(string(d))
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(p)))
	if !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	return b
}

type packApp map[string][]byte

func (m packApp) read(p string) []byte { return m[path.Clean(p)] }

// unpackWebApp is loop's unpackWebApp: gzip of [u32 header len][header
// JSON][bytes], header = [{path, offset, length}].
func unpackWebApp(packed []byte) (packApp, error) {
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if len(raw) < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(raw[:4])
	var header []struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(raw[4:4+n], &header); err != nil {
		return nil, err
	}
	body := raw[4+n:]
	out := packApp{}
	for _, e := range header {
		if e.Offset+e.Length <= len(body) {
			out[e.Path] = body[e.Offset : e.Offset+e.Length]
		}
	}
	return out, nil
}

func loadWebApp(explicit string) webApp {
	dir := explicit
	if dir == "" {
		dir = os.Getenv("LOOP_WEB_APP_DIR")
	}
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dirApp(dir)
		}
		return nil
	}
	packs := []string{os.Getenv("LOOP_WEB_APP_PACK")}
	if h, err := os.UserHomeDir(); err == nil {
		packs = append(packs, filepath.Join(h, ".loop", "web-app.pack"))
	}
	for _, p := range packs {
		if p == "" {
			continue
		}
		if raw, err := os.ReadFile(p); err == nil {
			if app, err := unpackWebApp(raw); err == nil && app["/index.html"] != nil {
				return app
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, cand := range []string{
			filepath.Join(filepath.Dir(exe), "..", "loop", "apps", "web", "dist"),
			filepath.Join(filepath.Dir(exe), "..", "..", "loop", "apps", "web", "dist"),
		} {
			if _, err := os.Stat(filepath.Join(cand, "index.html")); err == nil {
				return dirApp(cand)
			}
		}
	}
	return nil
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png",
	".ico": "image/x-icon", ".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".wasm": "application/wasm",
}

// serveWebApp is a file, or the app shell for an extensionless path (a
// client-side route).
func serveWebApp(app webApp, w http.ResponseWriter, pathname string) (isHTML bool) {
	decoded, err := url.PathUnescape(pathname)
	if err != nil {
		http.Error(w, "Not found", 404)
		return false
	}
	p := decoded
	if path.Ext(decoded) == "" {
		p = "/index.html"
	}
	body := app.read(p)
	if body == nil {
		http.Error(w, "Not found", 404)
		return false
	}
	ct := contentTypes[path.Ext(p)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("content-type", ct)
	if strings.HasPrefix(p, "/assets/") {
		w.Header().Set("cache-control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("cache-control", "no-cache")
	}
	_, _ = w.Write(body)
	return strings.HasPrefix(ct, "text/html")
}

const webAppMissingText = "loop serve: the web UI is not built. From a source checkout run `bun run --filter @loop/web build`."
