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

package agent

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/sirupsen/logrus"
)

// Authentication for the agent's HTTP API.
//
// The API is the control surface of a privileged process: it restores and
// deletes checkpoints, serves any file inside a checkpoint, and exposes pprof.
// The DaemonSet binds it to the node's IP (hostNetwork + hostPort 8081), and
// NetworkPolicy cannot fence it -- a hostNetwork pod carries node identity, so
// podSelector ingress rules do not match it. Access control therefore has to
// live in the request path.
//
// A shared bearer token rather than mTLS: this same router serves the peer
// fan-out endpoints that move multi-GB checkpoints, and TLS handshakes amortize
// with connection reuse but per-byte encryption does not. A header comparison
// costs nothing on the transfer path. See GH #486.

const authHeader = "Authorization"

// AuthMode selects what happens to a request that does not present a valid
// token.
type AuthMode string

const (
	// AuthDisabled skips the check. The default, so an upgrade that has not
	// yet been given a token behaves exactly as before.
	AuthDisabled AuthMode = "disabled"

	// AuthPermissive checks the token, logs and counts failures, and serves
	// the request anyway. This is the rollout state: agents and callers
	// cannot be updated in the same instant, so operators run permissive
	// until nvsnap_agent_auth_total{result="missing|invalid"} reaches zero,
	// then switch to required.
	AuthPermissive AuthMode = "permissive"

	// AuthRequired rejects with 401.
	AuthRequired AuthMode = "required"
)

// ParseAuthMode validates operator input. An unrecognized mode is refused at
// startup rather than silently treated as disabled, since "we set the flag and
// assumed it was on" is the failure this whole change exists to prevent.
func ParseAuthMode(s string) (AuthMode, error) {
	switch AuthMode(s) {
	case AuthDisabled, AuthPermissive, AuthRequired:
		return AuthMode(s), nil
	case "":
		return AuthDisabled, nil
	default:
		return "", fmt.Errorf("auth mode %q is not one of disabled|permissive|required", s)
	}
}

// unauthenticatedPaths bypass the token check.
//
// Probes and scraping must keep working without distributing the token to the
// kubelet and to Prometheus. Both are information-free: /health reports
// liveness, /metrics reports counters. Everything else, pprof included, is
// gated -- profiles leak memory contents and goroutine state, so an endpoint
// that is merely inconvenient to exploit is still not one to leave open.
var unauthenticatedPaths = map[string]bool{
	"/health":  true,
	"/metrics": true,
}

// tokenGuard returns middleware enforcing mode against token.
//
// Returns nil when there is nothing to enforce, so the caller can skip
// installing it entirely rather than paying for a no-op on every request.
func tokenGuard(mode AuthMode, token string, log *logrus.Logger) func(http.Handler) http.Handler {
	if mode == AuthDisabled || token == "" {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unauthenticatedPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			result := checkToken(r, token)
			metrics.AgentAuthTotal.WithLabelValues(result).Inc()
			if result == authOK {
				next.ServeHTTP(w, r)
				return
			}
			// RemoteAddr and path only: no header values, since the thing
			// being logged is a credential.
			entry := log.WithFields(logrus.Fields{
				"remote": r.RemoteAddr,
				"path":   r.URL.Path,
				"result": result,
			})
			if mode == AuthPermissive {
				entry.Warn("Unauthenticated request served (auth mode is permissive)")
				next.ServeHTTP(w, r)
				return
			}
			entry.Warn("Rejected unauthenticated request")
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

const (
	authOK      = "ok"
	authMissing = "missing"
	authInvalid = "invalid"
)

// outboundToken is the token this agent presents when it calls a peer.
//
// Package-level and atomic because peerHTTPClient is constructed at import
// time, long before flags are parsed, while the token only exists after
// startup. The alternative -- threading a client through every cascade call
// site -- would put the same header logic in a dozen places and leave the
// next call site free to forget it.
var outboundToken atomic.Pointer[string]

// SetOutboundToken records the token used on agent-to-agent requests. Safe to
// call before any request is issued; a nil/empty token sends no header, which
// is what keeps a disabled deployment working unchanged.
func SetOutboundToken(tok string) {
	outboundToken.Store(&tok)
}

// authTransport adds the bearer token to every outbound request.
//
// Wrapping the transport rather than editing call sites means a peer endpoint
// added later is authenticated without anyone remembering to do it -- the same
// reasoning as pathVarGuard on the inbound side.
type authTransport struct{ base http.RoundTripper }

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tok := outboundToken.Load()
	if tok != nil && *tok != "" && r.Header.Get(authHeader) == "" {
		// RoundTrip must not modify the request it is given.
		r = r.Clone(r.Context())
		r.Header.Set(authHeader, "Bearer "+*tok)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// checkToken compares the request's bearer token against the expected value in
// constant time, so a caller cannot recover the token byte by byte from
// response timing.
func checkToken(r *http.Request, want string) string {
	h := r.Header.Get(authHeader)
	if h == "" {
		return authMissing
	}
	got, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return authInvalid
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return authInvalid
	}
	return authOK
}
