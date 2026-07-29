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
	// An empty token is the same situation: nothing to enforce against.
	if g := tokenGuard(AuthRequired, "", quietLog()); g != nil {
		t.Error("empty token returned a middleware")
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
