package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
)

//go:embed console/*
var consoleAssets embed.FS

// ConsoleHandler serves the embedded ProGoA2A operator console.
func ConsoleHandler() http.Handler {
	assets, err := fs.Sub(consoleAssets, "console")
	if err != nil {
		panic("server: embedded console assets are unavailable")
	}
	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	})
}

// ConsoleBootstrapHandler reports the non-sensitive runtime facts needed to
// initialize the public console shell without probing protected endpoints.
func ConsoleBootstrapHandler(cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := "api"
		backend := "memory"
		authRequired := false
		if cfg != nil {
			if cfg.Role != "" {
				role = cfg.Role
			}
			if cfg.Storage.Backend != "" {
				backend = cfg.Storage.Backend
			}
			authRequired = cfg.Security.Enabled
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth_required":            authRequired,
			"role":                     role,
			"storage":                  backend,
			"refresh_interval_seconds": 15,
		})
	})
}
