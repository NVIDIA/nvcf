/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)

func quietLog() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// served reports whether the guarded handler ran, and the status returned.
func served(t *testing.T, mode AuthMode, token, header, path string) (bool, int) {
	t.Helper()
	ran := false
	guard := tokenGuard(mode, token, quietLog())
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	if guard != nil {
		h = guard(h)
	}
	r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	if header != "" {
		r.Header.Set(authHeader, header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return ran, w.Code
}

func TestTokenGuardRequired(t *testing.T) {
	const tok = "s3cret-token"

	cases := []struct {
		name     string
		header   string
		wantRan  bool
		wantCode int
	}{
		{"valid token", "Bearer " + tok, true, http.StatusOK},
		{"no header", "", false, http.StatusUnauthorized},
		{"wrong token", "Bearer nope", false, http.StatusUnauthorized},
		{"missing Bearer prefix", tok, false, http.StatusUnauthorized},
		{"empty bearer", "Bearer ", false, http.StatusUnauthorized},
		// A prefix of the real token must not pass: constant-time compare
		// returns 0 on a length mismatch, but assert it rather than trust it.
		{"token prefix", "Bearer " + tok[:5], false, http.StatusUnauthorized},
		{"wrong scheme", "Basic " + tok, false, http.StatusUnauthorized},
		// RFC 7235: the scheme is case-insensitive, so a conforming client
		// may legitimately send these and must not be turned away.
		{"lowercase scheme", "bearer " + tok, true, http.StatusOK},
		{"mixed-case scheme", "BeArEr " + tok, true, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ran, code := served(t, AuthRequired, tok, c.header, "/v1/checkpoints")
			if ran != c.wantRan || code != c.wantCode {
				t.Errorf("ran=%v code=%d, want ran=%v code=%d", ran, code, c.wantRan, c.wantCode)
			}
		})
	}
}

// The rollout depends on permissive serving the request while still counting
// the failure -- if it rejected, enabling it would be the same outage as
// switching straight to required.
func TestTokenGuardPermissiveServesButCounts(t *testing.T) {
	ran, code := served(t, AuthPermissive, "tok", "", "/v1/restore")
	if !ran || code != http.StatusOK {
		t.Errorf("permissive rejected an unauthenticated request: ran=%v code=%d", ran, code)
	}
}

func TestTokenGuardDisabledInstallsNothing(t *testing.T) {
	if g := tokenGuard(AuthDisabled, "tok", quietLog()); g != nil {
		t.Error("mode=disabled returned a middleware; caller should skip installing one")
	}
	// Permissive with no token could only log every request as
	// unauthenticated, which is noise; skipping it is correct.
	if g := tokenGuard(AuthPermissive, "", quietLog()); g != nil {
		t.Error("permissive with no token returned a middleware")
	}
}

// AuthRequired with no token must FAIL CLOSED. Returning nil here would make
// Agent.Run skip the middleware entirely and serve the privileged API
// unauthenticated -- a misconfiguration silently becoming an open API is the
// exact failure this feature exists to prevent.
func TestTokenGuardRequiredWithoutTokenDeniesAll(t *testing.T) {
	g := tokenGuard(AuthRequired, "", quietLog())
	if g == nil {
		t.Fatal("mode=required with no token returned nil; the API would be served unauthenticated")
	}
	ran := false
	h := g(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/restore", http.NoBody))
	if ran || w.Code != http.StatusUnauthorized {
		t.Errorf("privileged route: ran=%v code=%d, want ran=false code=401", ran, w.Code)
	}
	// Even a well-formed token cannot help: there is nothing to compare to.
	ran = false
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/restore", http.NoBody)
	r.Header.Set(authHeader, "Bearer anything")
	h.ServeHTTP(w, r)
	if ran || w.Code != http.StatusUnauthorized {
		t.Errorf("with a token: ran=%v code=%d, want ran=false code=401", ran, w.Code)
	}
	// Probes must still work, or the pod fails its liveness check and the
	// operator sees a crashloop instead of the actual misconfiguration.
	ran = false
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", http.NoBody))
	if !ran || w.Code != http.StatusOK {
		t.Errorf("/health: ran=%v code=%d, want ran=true code=200", ran, w.Code)
	}
}

// Probes and scraping must not need the token, or enabling auth breaks
// liveness and Prometheus. pprof deliberately is NOT exempt: profiles expose
// memory contents and goroutine state.
func TestTokenGuardExemptPaths(t *testing.T) {
	for _, p := range []string{"/health", "/metrics"} {
		if ran, code := served(t, AuthRequired, "tok", "", p); !ran || code != http.StatusOK {
			t.Errorf("%s required a token: ran=%v code=%d", p, ran, code)
		}
	}
	for _, p := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/profile"} {
		if ran, _ := served(t, AuthRequired, "tok", "", p); ran {
			t.Errorf("%s served without a token", p)
		}
	}
}

func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]AuthMode{
		"":           AuthDisabled,
		"disabled":   AuthDisabled,
		"permissive": AuthPermissive,
		"required":   AuthRequired,
	} {
		got, err := ParseAuthMode(in)
		if err != nil || got != want {
			t.Errorf("ParseAuthMode(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	// A typo must be an error, not a silent fallback to disabled.
	for _, in := range []string{"Required", "enabled", "on", "true", "requird"} {
		if _, err := ParseAuthMode(in); err == nil {
			t.Errorf("ParseAuthMode(%q) = nil error; want a failure so a typo cannot silently leave the API open", in)
		}
	}
}

// recordingRT captures what authTransport handed to the base transport.
type recordingRT struct{ got *http.Request }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
}

func TestAuthTransportSignsOutbound(t *testing.T) {
	t.Cleanup(func() { SetOutboundToken("") })

	base := &recordingRT{}
	c := &http.Client{Transport: &authTransport{base: base}}

	// No token configured: no header, so a cluster running with auth off is
	// byte-for-byte unchanged on the wire.
	SetOutboundToken("")
	req, _ := http.NewRequest(http.MethodGet, "http://peer/v1/checkpoints/x/manifest", http.NoBody)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	if h := base.got.Header.Get(authHeader); h != "" {
		t.Errorf("sent %q with no token configured", h)
	}

	SetOutboundToken("peer-token")
	req, _ = http.NewRequest(http.MethodGet, "http://peer/v1/checkpoints/x/manifest", http.NoBody)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	if got, want := base.got.Header.Get(authHeader), "Bearer peer-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	// RoundTrip must not mutate the caller's request.
	if h := req.Header.Get(authHeader); h != "" {
		t.Errorf("caller's request was mutated: %q", h)
	}
}

// The signed request must actually satisfy the guard. Testing the two halves
// separately would not catch a format mismatch between them.
func TestOutboundTokenSatisfiesGuard(t *testing.T) {
	t.Cleanup(func() { SetOutboundToken("") })
	const tok = "round-trip-token"
	SetOutboundToken(tok)

	base := &recordingRT{}
	c := &http.Client{Transport: &authTransport{base: base}}
	req, _ := http.NewRequest(http.MethodGet, "http://peer/v1/checkpoints/x/manifest", http.NoBody)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	if got := checkToken(base.got, tok); got != authOK {
		t.Errorf("guard rejected our own signed request: %s", got)
	}
}
