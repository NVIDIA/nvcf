/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Single-pane observability proxy. Reverse-proxies the in-cluster
// Grafana / Jaeger / Prometheus Services so operators get one URL
// (nvsnap-server's external IP) for everything. The NvSnap UI renders
// "Observability" nav links into this proxy based on what
// /api/v1/observability reports as available.
//
// Why proxy (vs. 3 separate LoadBalancers / per-service Ingresses):
//   - one external IP = one cloud LB to pay for + one auth boundary
//   - operators don't juggle port-forwards or 3 URLs
//   - nvsnap-server already terminates external auth; the proxy reuses it

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// observabilityTarget describes a subchart service we can proxy to.
// Names match the helm subchart Service names from
// deploy/helm/nvsnap/values.yaml; if operators rename them they'd need
// to override the discoveryFn.
type observabilityTarget struct {
	// Name is the URL path segment under /observability/.
	// e.g. "grafana" → /observability/grafana/...
	Name string `json:"name"`

	// ServiceName is the in-cluster K8s Service to forward to.
	ServiceName string `json:"-"`

	// PortName is preferred when the Service exposes multiple ports.
	// Empty means "first port on the service."
	PortName string `json:"-"`

	// FallbackPort is used when the service can't be inspected (no
	// K8s API access from the binary) or has no named port matching.
	FallbackPort int `json:"-"`

	// PreservePrefix controls whether the proxy strips /observability/<name>
	// before forwarding. Grafana with serve_from_sub_path=true expects to
	// see the full sub-path in the request URL and handles the strip
	// itself; Jaeger / Prometheus / most other backends expect a rooted
	// request and we strip for them.
	PreservePrefix bool `json:"-"`

	// DisplayName + Description are surfaced via the UI nav.
	DisplayName string `json:"displayName"`
	Description string `json:"description"`

	// Available is filled in by discovery — true when the Service
	// exists in the cluster.
	Available bool `json:"available"`

	// External is the rendered URL the UI links to.
	// Format: /observability/<Name>/  (relative to nvsnap-server).
	External string `json:"url"`
}

// observabilityTargets is the fixed list of subchart integrations.
// Adding a new tool means appending here + bumping the helm chart.
var observabilityTargets = []observabilityTarget{
	{
		Name:           "grafana",
		ServiceName:    "nvsnap-grafana",
		FallbackPort:   80,
		PreservePrefix: true, // serve_from_sub_path=true wants the full prefix in the URL
		DisplayName:    "Dashboards",
		Description:    "Cold-start vs NvSnap-restored pod ready-time, plus internal nvsnap-server / nvsnap-agent metrics.",
	},
	{
		Name:           "jaeger",
		ServiceName:    "nvsnap-jaeger-query",
		FallbackPort:   16686,
		PreservePrefix: true, // QUERY_BASE_PATH similarly wants the full prefix
		DisplayName:    "Traces",
		Description:    "End-to-end OTel traces — admission → reconcile → CRIU dump → cascade fetch.",
	},
	{
		Name:           "prometheus",
		ServiceName:    "nvsnap-prometheus-server",
		FallbackPort:   80,
		PreservePrefix: false, // web.route-prefix=/ expects rooted requests; we strip
		DisplayName:    "Metrics",
		Description:    "Raw Prometheus UI for ad-hoc queries against NVCA + nvsnap metrics.",
	},
}

// listObservabilityHandler serves GET /api/v1/observability. Returns
// every target's discovery + URL so the UI can render nav links for
// what's actually installed.
func (s *Server) listObservabilityHandler(w http.ResponseWriter, r *http.Request) {
	out := s.discoverObservabilityTargets(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"targets": out,
	})
}

// discoverObservabilityTargets returns a fresh snapshot of targets.
// Result is cached for cacheTTL to avoid hammering the K8s API on
// every UI refresh.
func (s *Server) discoverObservabilityTargets(ctx context.Context) []observabilityTarget {
	cached := s.cachedObservabilityTargets()
	if cached != nil {
		return cached
	}

	out := make([]observabilityTarget, len(observabilityTargets))
	for i, t := range observabilityTargets {
		t.External = "/observability/" + t.Name + "/"
		t.Available = s.observabilityServiceExists(ctx, t.ServiceName)
		out[i] = t
	}
	s.storeObservabilityCache(out)
	return out
}

func (s *Server) observabilityServiceExists(ctx context.Context, name string) bool {
	if s.kubeClient == nil {
		return false
	}
	// NvSnap subcharts install into nvsnap-system by convention. If a
	// future helm value lets operators relocate them, plumb the
	// namespace here.
	_, err := s.kubeClient.CoreV1().Services("nvsnap-system").Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	// NotFound = subchart genuinely not installed; that's an expected
	// "Available=false" outcome and not worth logging.
	//
	// Anything else — Forbidden, network blip, throttling — is an
	// operator-visible problem we used to silently swallow. The
	// observability tiles would just go dark with no breadcrumb. Log
	// non-NotFound errors at Warn so the cause is reachable from
	// kubectl logs without enabling debug. See nvsnap issue on
	// missing services:get RBAC, 2026-06-02.
	if !apierrors.IsNotFound(err) {
		s.log.WithError(err).WithField("service", name).
			Warn("observability discovery: K8s Services.Get failed (rendering tile as unavailable)")
	}
	return false
}

// observabilityCache is a tiny TTL cache on the Server struct
// (added inline below in init). We keep it minimal — a single
// snapshot for every UI refresh, 10s TTL.
const observabilityCacheTTL = 10 * time.Second

type observabilityCache struct {
	mu        sync.Mutex
	targets   []observabilityTarget
	expiresAt time.Time
}

func (s *Server) cachedObservabilityTargets() []observabilityTarget {
	if s.obsCache == nil {
		return nil
	}
	s.obsCache.mu.Lock()
	defer s.obsCache.mu.Unlock()
	if time.Now().After(s.obsCache.expiresAt) {
		return nil
	}
	return s.obsCache.targets
}

func (s *Server) storeObservabilityCache(t []observabilityTarget) {
	if s.obsCache == nil {
		return
	}
	s.obsCache.mu.Lock()
	defer s.obsCache.mu.Unlock()
	s.obsCache.targets = t
	s.obsCache.expiresAt = time.Now().Add(observabilityCacheTTL)
}

// observabilityProxy returns the reverse-proxy mux for
// /observability/<target>/... — stripping the prefix and forwarding
// to the appropriate in-cluster Service. Returns the http.Handler so
// the server's main route table can mount it under PathPrefix.
//
// The proxied target's HTML/JS may emit absolute-path URLs (like
// "/api/health"); those would 404 against nvsnap-server. Operators
// must configure the subchart to serve under /observability/<name>/
// — Grafana via grafana.ini.server.{root_url,serve_from_sub_path},
// Jaeger via query.basePath, Prometheus via --web.external-url.
// The helm values in deploy/helm/nvsnap/values.yaml set those.
func (s *Server) observabilityProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /observability/{target}/...
		rest := strings.TrimPrefix(r.URL.Path, "/observability/")
		if rest == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		var targetName, subPath string
		if i := strings.Index(rest, "/"); i >= 0 {
			targetName, subPath = rest[:i], rest[i:]
		} else {
			targetName, subPath = rest, "/"
		}

		t, ok := s.observabilityTargetByName(r.Context(), targetName)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !t.Available {
			http.Error(w, fmt.Sprintf("observability target %q not installed on this cluster", targetName),
				http.StatusServiceUnavailable)
			return
		}

		backend := s.observabilityBackendURL(t)
		proxy := httputil.NewSingleHostReverseProxy(backend)
		// Rewrite the inbound request. PreservePrefix decides whether
		// the backend sees the full /observability/<name>/... path or
		// just the sub-path:
		//   - Grafana (serve_from_sub_path=true), Jaeger (QUERY_BASE_PATH):
		//     PreservePrefix=true. They expect the prefix and strip it
		//     internally; stripping it again here causes redirect loops
		//     (their root_url 301s /<path> → /<prefix>/<path>).
		//   - Prometheus (web.route-prefix=/): PreservePrefix=false.
		//     It wants rooted requests; we strip /observability/<name>.
		origDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			origDirector(req)
			if t.PreservePrefix {
				req.URL.Path = r.URL.Path
			} else {
				req.URL.Path = subPath
			}
			req.URL.RawPath = ""
			req.Host = backend.Host
			// Tell the backend what external URL the operator used,
			// in case it generates absolute redirects.
			req.Header.Set("X-Forwarded-Prefix", "/observability/"+targetName)
		}
		// Backends (Grafana especially) emit redirects whose Location
		// references their internal absolute URL — Grafana defaults to
		// http://localhost/... when its `domain` ini isn't set, which
		// breaks the browser the moment a click hits a route that
		// 301s to home. Rewrite outbound Location headers to be
		// relative so the browser stays on nvsnap-server's external URL.
		proxy.ModifyResponse = func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteRedirect(loc, targetName))
			}
			return nil
		}
		// Error handler so a backend hiccup returns a real status
		// instead of a half-flushed connection.
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("observability backend %q error: %v", targetName, err),
				http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
}

// rewriteRedirect strips the absolute-URL prefix from a Location
// header so the browser keeps its current origin (nvsnap-server). It
// preserves the path and query verbatim. Examples:
//
//	http://localhost/observability/grafana/login        → /observability/grafana/login
//	http://localhost:3000/observability/grafana/        → /observability/grafana/
//	/observability/grafana/login                         → /observability/grafana/login (unchanged)
//	http://localhost/login                              → /observability/grafana/login (prefixes the sub-path)
//
// The last case is the trickiest: Grafana sometimes emits Location
// values that drop the sub-path entirely (e.g., its OAuth flow
// stripping it). Re-add the sub-path so the browser re-enters via
// the proxy instead of 404'ing.
func rewriteRedirect(loc, targetName string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	// Absolute URL → strip scheme + host.
	if u.IsAbs() {
		u.Scheme = ""
		u.Host = ""
		u.User = nil
	}
	// Re-add the /observability/<name> prefix when the path doesn't
	// already start with it. Guards against backends that emit
	// absolute paths relative to their own root (e.g. /login).
	prefix := "/observability/" + targetName
	if !strings.HasPrefix(u.Path, prefix) {
		u.Path = prefix + u.Path
	}
	return u.String()
}

func (s *Server) observabilityTargetByName(ctx context.Context, name string) (observabilityTarget, bool) {
	for _, t := range s.discoverObservabilityTargets(ctx) {
		if t.Name == name {
			return t, true
		}
	}
	return observabilityTarget{}, false
}

func (s *Server) observabilityBackendURL(t observabilityTarget) *url.URL {
	port := t.FallbackPort
	if s.kubeClient != nil {
		svc, err := s.kubeClient.CoreV1().Services("nvsnap-system").Get(context.Background(), t.ServiceName, metav1.GetOptions{})
		if err == nil && len(svc.Spec.Ports) > 0 {
			if t.PortName != "" {
				for _, p := range svc.Spec.Ports {
					if p.Name == t.PortName {
						port = int(p.Port)
						break
					}
				}
			} else {
				port = int(svc.Spec.Ports[0].Port)
			}
		}
	}
	// In-cluster DNS — nvsnap-server itself runs in nvsnap-system, so
	// the short name is fine, but we use the FQDN for clarity and
	// to keep working when the namespace search list isn't standard.
	u, _ := url.Parse(fmt.Sprintf("http://%s.nvsnap-system.svc.cluster.local:%d", t.ServiceName, port))
	return u
}
