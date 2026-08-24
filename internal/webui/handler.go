package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const contentSecurityPolicy = "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; font-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self' ws://127.0.0.1:* ws://localhost:* ws://[::1]:*"

// Handler returns a same-origin SPA handler mounted beneath /ui/.
func Handler() http.Handler {
	files := assets()
	fileServer := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(writer.Header())
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path == "/" || request.URL.Path == "/ui" {
			http.Redirect(writer, request, "/ui/", http.StatusTemporaryRedirect)
			return
		}
		if !strings.HasPrefix(request.URL.Path, "/ui/") {
			http.NotFound(writer, request)
			return
		}

		name := strings.TrimPrefix(path.Clean(request.URL.Path), "/ui/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if info, err := fs.Stat(files, name); err != nil || info.IsDir() {
			name = "index.html"
		}
		if name == "index.html" {
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			value, err := fs.ReadFile(files, name)
			if err != nil {
				http.Error(writer, "UI unavailable", http.StatusInternalServerError)
				return
			}
			writer.WriteHeader(http.StatusOK)
			if request.Method == http.MethodGet {
				_, _ = writer.Write(value)
			}
			return
		} else if strings.HasPrefix(name, "assets/") {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		cloned := request.Clone(request.Context())
		cloned.URL.Path = "/" + name
		fileServer.ServeHTTP(writer, cloned)
	})
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
