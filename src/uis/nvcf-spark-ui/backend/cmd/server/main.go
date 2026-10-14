// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/NVIDIA/nvcf/src/uis/nvcf-spark-ui/backend/internal/middleware"
	"github.com/NVIDIA/nvcf/src/uis/nvcf-spark-ui/backend/internal/utils"
)

const (
	defaultServerPort = "8300"
	defaultStaticDir  = "static"

	serverPort = "SERVER_PORT"
	staticDir  = "STATIC_DIR"
)

// Connection timeouts. Without them a slow or idle client holds a connection
// (and its goroutine) open indefinitely, so a handful of them can exhaust the
// server. Every timeout is a hard deadline on a distinct phase:
//
//   - read header caps how long a client may take to send its request headers,
//     which is what bounds a slow-loris style attack.
//   - read covers headers plus body.
//   - write bounds the response write.
//   - idle reaps keep-alive connections between requests.
//
// Each is overridable in whole seconds through its environment variable, which
// the Helm chart populates from values.yaml. A value of 0 disables that
// deadline; only set one that way for a deployment that has a proven need,
// because it restores the unbounded behavior above.
const (
	defaultReadHeaderTimeoutSeconds = 10
	defaultReadTimeoutSeconds       = 30
	defaultWriteTimeoutSeconds      = 60
	defaultIdleTimeoutSeconds       = 120

	readHeaderTimeoutSeconds = "READ_HEADER_TIMEOUT_SECONDS"
	readTimeoutSeconds       = "READ_TIMEOUT_SECONDS"
	writeTimeoutSeconds      = "WRITE_TIMEOUT_SECONDS"
	idleTimeoutSeconds       = "IDLE_TIMEOUT_SECONDS"
)

// serverTimeouts holds the resolved connection deadlines for the HTTP server.
type serverTimeouts struct {
	readHeader time.Duration
	read       time.Duration
	write      time.Duration
	idle       time.Duration
}

// timeoutsFromEnv resolves each connection timeout from its environment
// variable, falling back to the package default when the variable is unset or
// empty. It reports an error rather than silently falling back when a variable
// is set to something unusable, so a typo in the chart values surfaces at
// startup instead of quietly serving with a different deadline.
func timeoutsFromEnv() (serverTimeouts, error) {
	var (
		timeouts serverTimeouts
		err      error
	)
	for _, f := range []struct {
		env      string
		fallback int
		dst      *time.Duration
	}{
		{readHeaderTimeoutSeconds, defaultReadHeaderTimeoutSeconds, &timeouts.readHeader},
		{readTimeoutSeconds, defaultReadTimeoutSeconds, &timeouts.read},
		{writeTimeoutSeconds, defaultWriteTimeoutSeconds, &timeouts.write},
		{idleTimeoutSeconds, defaultIdleTimeoutSeconds, &timeouts.idle},
	} {
		if *f.dst, err = secondsFromEnv(f.env, f.fallback); err != nil {
			return serverTimeouts{}, err
		}
	}
	return timeouts, nil
}

// secondsFromEnv reads key as a whole number of seconds, returning fallback if
// it is unset or empty.
func secondsFromEnv(key string, fallback int) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return time.Duration(fallback) * time.Second, nil
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if secs < 0 {
		return 0, fmt.Errorf("%s: %d is negative", key, secs)
	}
	return time.Duration(secs) * time.Second, nil
}

func main() {
	ctx := signals.SetupSignalHandler()
	logger := utils.ConfigLogger()
	ctx = logger.WithContext(ctx)

	portStr := utils.GetEnvOr(serverPort, defaultServerPort)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		logger.Fatal().Err(err).Msgf("Invalid %s", serverPort)
	}
	timeouts, err := timeoutsFromEnv()
	if err != nil {
		logger.Fatal().Err(err).Msg("Invalid server timeout")
	}
	gateway, err := gatewayFromEnv()
	if err != nil {
		logger.Fatal().Err(err).Msg("Invalid gateway configuration")
	}
	ui, err := uiConfigFromEnv()
	if err != nil {
		logger.Fatal().Err(err).Msg("Invalid UI configuration")
	}
	recipes := newRecipeCatalog(os.Getenv(recipeCatalogPath), logger)
	if recipes.path == "" {
		logger.Info().Msgf("%s is unset, so the recipes page has no catalog", recipeCatalogPath)
	} else {
		// Report the catalog's state at startup; it is read again on change.
		_, _ = recipes.current()
	}

	server := http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", port),
		Handler:           newHandler(logger, utils.GetEnvOr(staticDir, defaultStaticDir), gateway, ui, recipes),
		ReadHeaderTimeout: timeouts.readHeader,
		ReadTimeout:       timeouts.read,
		WriteTimeout:      timeouts.write,
		IdleTimeout:       timeouts.idle,
	}

	go func() {
		logger.Info().Stringer("gateway", gateway.url).Msgf("Started server on port %d", port)
		if errS := server.ListenAndServe(); errS != nil &&
			!errors.Is(errS, http.ErrServerClosed) {
			logger.Fatal().Err(errS).Msg("Failed to start server")
		}
	}()

	<-ctx.Done()
	if err := server.Shutdown(context.Background()); err != nil {
		logger.Fatal().Err(err).Msg("Failed to shutdown server")
	}
}

// newHandler builds the routes and the middleware chain the server runs with.
func newHandler(logger zerolog.Logger, staticRoot string, gateway gatewayConfig, ui uiConfig, recipes *recipeCatalog) http.Handler {
	router := http.NewServeMux()

	// Liveness probe: respond 200 to GET /status with no upstream dependency.
	router.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	registerGateway(router, gateway, logger)
	registerConfig(router, ui)
	registerRecipes(router, recipes)

	// Serve the static UI build. Every file is registered as its own route at
	// startup and the "/" subtree pattern serves index.html for anything else.
	registerStatic(router, staticRoot, logger)

	var handler http.Handler = router
	mdwChain := []func(http.Handler) http.Handler{
		hlog.AccessHandler(
			func(r *http.Request, status, _ int, duration time.Duration) {
				if r.Method == http.MethodGet {
					return
				}
				hlog.FromRequest(r).Info().
					Int("status", status).
					Str("latency", duration.String()).
					Msg("Exit Audit")
			},
		),
		middleware.EntryAudit,
		hlog.RequestHandler("url"),
		hlog.RemoteAddrHandler("client_ip"),
		middleware.PanicRecovery,
		// Outside PanicRecovery, so its 500s carry the headers too.
		middleware.SecurityHeaders,
		hlog.NewHandler(logger),
	}
	for i := range mdwChain {
		handler = mdwChain[i](handler)
	}
	return handler
}

// registerStatic wires the static UI build under root into router.
//
// The file tree is walked once at startup and every file becomes an explicit
// route, so the mux dispatches requests directly instead of touching the
// filesystem to decide whether a path exists. The "/" pattern is a subtree
// match, so it doubles as the not-found handler: any path the mux can't map to
// a real file (client-side routes like /registry, plus the root itself) serves
// index.html, giving the SPA its deep links.
//
// API paths (see apiPrefixes) must not fall through to index.html. They get a
// JSON 404, so a mistyped or unforwarded route surfaces as an API error instead
// of HTML the UI fails to parse.
//
// Caching is split by asset type: Vite emits content-hashed files under
// assets/, safe to cache immutably forever, while index.html must never be
// cached so a redeploy is picked up immediately and browsers don't keep
// referencing assets that no longer exist.
//
// The build writes compressed copies (.br, .gz) next to text assets; they are
// served in place of the file they compress, never as routes of their own.
func registerStatic(router *http.ServeMux, root string, logger zerolog.Logger) {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || isPrecompressed(p) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		urlPath := "/" + filepath.ToSlash(rel)
		if urlPath == "/index.html" { // served by the "/" fallback below
			return nil
		}
		cacheControl := "no-cache"
		if strings.HasPrefix(urlPath, "/assets/") {
			cacheControl = "public, max-age=31536000, immutable"
		}
		router.Handle(urlPath, staticFile(p, cacheControl))
		return nil
	})
	if err != nil {
		logger.Warn().Err(err).Str("dir", root).
			Msg("Failed to index static dir; only the SPA fallback will be served")
	}

	// Not-found handler: unmatched paths (and "/") get index.html, never cached.
	router.Handle("/", staticFile(filepath.Join(root, "index.html"), "no-store"))

	// API paths never fall back to the SPA. Registered API routes are more
	// specific patterns, so they still win over these subtree matches.
	for _, prefix := range apiPrefixes {
		router.Handle(prefix, apiNotFound())
	}
}

// apiPrefixes are the path prefixes reserved for the API: the gateway routes
// this server forwards (/v1/) and its own routes (/api/).
var apiPrefixes = []string{"/api/", "/v1/"}

// apiNotFound answers unknown API paths with a JSON 404 in the gateway's
// {"message": ...} error shape.
func apiNotFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	})
}

// writeError answers with the {"message": ...} body the gateway uses for
// relayed errors, so the UI reads BFF and gateway errors the same way.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Message string `json:"message"`
	}{message})
}

// precompressed lists the compressed copies the UI build writes next to a
// file, in the order they are preferred: brotli is the smaller of the two.
var precompressed = []struct{ encoding, ext string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

// isPrecompressed reports whether p is a compressed copy of another file.
func isPrecompressed(p string) bool {
	for _, c := range precompressed {
		if strings.HasSuffix(p, c.ext) {
			if _, err := os.Stat(strings.TrimSuffix(p, c.ext)); err == nil {
				return true
			}
		}
	}
	return false
}

// staticFile serves a single file from disk with the given Cache-Control value.
// Static assets are read-only, so only GET and HEAD are served; any other
// method gets 405.
//
// When the build wrote compressed copies of the file, the best one the client
// accepts is served instead, with Content-Encoding set and the original's
// Content-Type. Compression happens once at build time, never per request, and
// the streamed API responses are never touched.
func staticFile(path, cacheControl string) http.Handler {
	variants := map[string]string{}
	for _, c := range precompressed {
		if _, err := os.Stat(path + c.ext); err == nil {
			variants[c.encoding] = path + c.ext
		}
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", cacheControl)
		if len(variants) > 0 {
			w.Header().Add("Vary", "Accept-Encoding")
		}
		for _, c := range precompressed {
			variant, ok := variants[c.encoding]
			if !ok || !acceptsEncoding(r.Header.Get("Accept-Encoding"), c.encoding) {
				continue
			}
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.Header().Set("Content-Encoding", c.encoding)
			http.ServeFile(w, r, variant)
			return
		}
		http.ServeFile(w, r, path)
	})
}

// acceptsEncoding reports whether an Accept-Encoding header value admits
// encoding, either by name or through "*", and not with q=0.
func acceptsEncoding(header, encoding string) bool {
	accepted := false
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.TrimSpace(name)
		if !strings.EqualFold(name, encoding) && name != "*" {
			continue
		}
		refused := false
		for param := range strings.SplitSeq(params, ";") {
			key, value, _ := strings.Cut(strings.TrimSpace(param), "=")
			if strings.EqualFold(strings.TrimSpace(key), "q") {
				if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && q == 0 {
					refused = true
				}
			}
		}
		// An explicit entry for the encoding overrides "*".
		if strings.EqualFold(name, encoding) {
			return !refused
		}
		accepted = !refused
	}
	return accepted
}
