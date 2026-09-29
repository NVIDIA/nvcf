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
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	cplane "github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/control-plane"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/middleware"
	twatcher "github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/token-watcher"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/utils"
)

const (
	defaultNVCFURL    = "api.nvcf.svc.cluster.local:8080"
	defaultNVCTURL    = "nvct-api.nvcf.svc.cluster.local:8080"
	defaultSISURL     = "api.sis.svc.cluster.local:8080"
	defaultServerPort = "8300"
	defaultStaticDir  = "static"

	nvcfURL    = "NVCF_URL"
	nvctURL    = "NVCT_URL"
	sisURL     = "SIS_URL"
	serverPort = "SERVER_PORT"
	staticDir  = "STATIC_DIR"
)

// Connection timeouts. Without them a slow or idle client holds a connection
// (and its goroutine) open indefinitely, so a handful of them can exhaust the
// server. Every timeout is a hard deadline on a distinct phase:
//
//   - read header caps how long a client may take to send its request headers,
//     which is what bounds a slow-loris style attack.
//   - read covers headers plus body. Only GET and HEAD reach this server (see
//     middleware.AllowReadMethods), so bodies are effectively absent.
//   - write bounds the response write. For proxied routes the upstream round
//     trip is written inside this window, so it must stay comfortably above the
//     slowest control-plane call rather than tracking the read timeout.
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
	tokenWatcher := twatcher.Watch(ctx)
	k8sClient, err := utils.InitK8sClient()
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize K8s client")
	}

	router := http.NewServeMux()
	var handler http.Handler = router
	mdwChain := []func(http.Handler) http.Handler{
		middleware.AllowReadMethods,
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
		middleware.AddRequestId,
		middleware.PanicRecovery,
		hlog.NewHandler(logger),
	}
	for i := range mdwChain {
		handler = mdwChain[i](handler)
	}

	// One reverse proxy per upstream. SetURL leaves the request path unchanged,
	// so /v2/nvcf/... -> NVCF, /v1/nvct/... -> NVCT, /v1/si/... -> the cluster API.
	nvcfURL := &url.URL{Scheme: "http", Host: utils.GetEnvOr(nvcfURL, defaultNVCFURL)}
	nvctURL := &url.URL{Scheme: "http", Host: utils.GetEnvOr(nvctURL, defaultNVCTURL)}
	sisURL := &url.URL{Scheme: "http", Host: utils.GetEnvOr(sisURL, defaultSISURL)}

	nvcfProxy := newProxy(nvcfURL, tokenWatcher.NVCFToken, logger)
	router.Handle("/v2/nvcf/accounts", nvcfProxy)
	router.Handle("/v2/nvcf/accounts/", nvcfProxy)
	router.Handle("/v1/nvct/accounts/", newProxy(nvctURL, tokenWatcher.NVCTToken, logger))
	router.Handle("/v1/si/accounts/", newProxy(sisURL, tokenWatcher.SISToken, logger))

	// Liveness probe: respond 200 to GET /status with no upstream dependency.
	router.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Control-plane component health, read from the ConfigMap the monitor writes.
	router.Handle("GET /v1/control-plane", cplane.StatusHandler(k8sClient))

	// Serve the static UI build. Every file is registered as its own route at
	// startup and the "/" subtree pattern serves index.html for anything else.
	registerStatic(router, utils.GetEnvOr(staticDir, defaultStaticDir), logger)

	server := http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", port),
		Handler:           handler,
		ReadHeaderTimeout: timeouts.readHeader,
		ReadTimeout:       timeouts.read,
		WriteTimeout:      timeouts.write,
		IdleTimeout:       timeouts.idle,
	}

	go func() {
		logger.Info().Msgf("Started server on port %d", port)
		if errS := server.ListenAndServe(); errS != nil &&
			!errors.Is(errS, http.ErrServerClosed) {
			logger.Fatal().Err(errS).Msg("Failed to start server")
		}
	}()

	<-ctx.Done()
	defer tokenWatcher.Wait()
	if err := server.Shutdown(context.Background()); err != nil {
		logger.Fatal().Err(err).Msg("Failed to shutdown server")
	}
}

// newProxy returns a handler that reverse-proxies requests unchanged to target,
// injecting target's bearer token (from token) as the Authorization header. If
// this upstream has no valid token, the request is rejected with 500 rather than
// forwarded (the upstream would reject it anyway). It serves any HTTP method.
func newProxy(target *url.URL, token func() (string, bool), logger zerolog.Logger) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) { // client went away; nothing to send
				return
			}
			logger.Error().Err(err).Msgf("upstream %s error", target.Host)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := token()
		if !ok || t == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.Header.Set("Authorization", "Bearer "+t)
		proxy.ServeHTTP(w, r)
	})
}

// registerStatic wires the static UI build under root into router.
//
// The file tree is walked once at startup and every file becomes an explicit
// route, so the mux dispatches requests directly instead of touching the
// filesystem to decide whether a path exists. The "/" pattern is a subtree
// match, so it doubles as the not-found handler: any path the mux can't map to
// a real file (client-side routes like /clusters, plus the root itself) serves
// index.html, giving the SPA its deep links.
//
// Caching is split by asset type: Vite emits content-hashed files under
// assets/, safe to cache immutably forever, while index.html must never be
// cached so a redeploy is picked up immediately and browsers don't keep
// referencing assets that no longer exist.
func registerStatic(router *http.ServeMux, root string, logger zerolog.Logger) {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
}

// staticFile serves a single file from disk with the given Cache-Control value.
// Static assets are read-only, so only GET and HEAD are served; any other
// method gets 405.
func staticFile(path, cacheControl string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", cacheControl)
		http.ServeFile(w, r, path)
	})
}
