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

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSocketPath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"valid unix", "unix:///run/spire/spire-agent.sock", "/run/spire/spire-agent.sock", false},
		{"valid unix tmp", "unix:///tmp/spire.sock", "/tmp/spire.sock", false},
		{"empty path", "unix://", "", true},
		{"wrong scheme", "tcp://localhost:8080", "", true},
		{"bare path", "/run/spire/sock", "", true},
		{"garbage", "::not a url::", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSocketPath(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCalculateRefreshTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		lifetime  time.Duration
		wantFloor bool
		want      time.Duration
	}{
		{"1 hour lifetime: 80%", time.Hour, false, 48 * time.Minute},
		{"10 minute lifetime: 80%", 10 * time.Minute, false, 8 * time.Minute},
		{"10 second lifetime: clamped to 30s floor", 10 * time.Second, true, 30 * time.Second},
		{"0 lifetime: clamped to 30s floor", 0, true, 30 * time.Second},
		{"negative lifetime: clamped to 30s floor", -time.Hour, true, 30 * time.Second},
		{"exactly 30s floor boundary (37.5s * 0.8 = 30s)", 37500 * time.Millisecond, false, 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateRefreshTime(now, now.Add(tc.lifetime))
			assert.Equal(t, tc.want, got)
		})
	}
}

// makeSignedJWT mints a minimally-valid ES256-signed JWT with the given expiry
// (and optional extra claims) for extractExpiry testing. Returns the compact
// serialization.
func makeSignedJWT(t *testing.T, expiry *jwt.NumericDate) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	claims := jwt.Claims{Expiry: expiry}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

func TestExtractExpiry(t *testing.T) {
	defaults, err := parseSigningAlgorithms("")
	require.NoError(t, err)

	t.Run("valid expiry is returned", func(t *testing.T) {
		expected := time.Now().Add(10 * time.Minute).Truncate(time.Second)
		token := makeSignedJWT(t, jwt.NewNumericDate(expected))

		got, err := extractExpiry(token, defaults)
		require.NoError(t, err)
		assert.Equal(t, expected.Unix(), got.Unix())
	})

	t.Run("missing expiry returns error", func(t *testing.T) {
		token := makeSignedJWT(t, nil)
		_, err := extractExpiry(token, defaults)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no expiry claim")
	})

	t.Run("malformed JWT returns error", func(t *testing.T) {
		_, err := extractExpiry("not-a-jwt", defaults)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse JWT")
	})

	t.Run("rejects token signed with disallowed algorithm", func(t *testing.T) {
		// Token is ES256-signed; the configured set is RS256-only, so the parser
		// must reject it before we even reach the expiry-claim extraction.
		expected := time.Now().Add(10 * time.Minute).Truncate(time.Second)
		token := makeSignedJWT(t, jwt.NewNumericDate(expected))

		rsaOnly, err := parseSigningAlgorithms("RS256")
		require.NoError(t, err)

		_, err = extractExpiry(token, rsaOnly)
		require.Error(t, err, "ES256 token must not be accepted when only RS256 is configured")
	})
}

func TestParseSigningAlgorithms(t *testing.T) {
	t.Run("empty input falls back to defaults", func(t *testing.T) {
		got, err := parseSigningAlgorithms("")
		require.NoError(t, err)
		assert.Len(t, got, len(defaultSigningAlgorithms))
	})
	t.Run("explicit single algorithm", func(t *testing.T) {
		got, err := parseSigningAlgorithms("RS256")
		require.NoError(t, err)
		assert.Equal(t, []jose.SignatureAlgorithm{jose.RS256}, got)
	})
	t.Run("multiple algorithms with whitespace", func(t *testing.T) {
		got, err := parseSigningAlgorithms("RS256, ES256 , EdDSA")
		require.NoError(t, err)
		assert.Equal(t, []jose.SignatureAlgorithm{jose.RS256, jose.ES256, jose.EdDSA}, got)
	})
	t.Run("unknown algorithm rejected", func(t *testing.T) {
		_, err := parseSigningAlgorithms("HS256,RS256")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HS256")
	})
	t.Run("only-commas rejected", func(t *testing.T) {
		_, err := parseSigningAlgorithms(", ,")
		require.Error(t, err)
	})
}

func TestHealthHandler(t *testing.T) {
	t.Run("not ready returns 503", func(t *testing.T) {
		var ready atomic.Bool
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		healthHandler(&ready).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("ready returns 200", func(t *testing.T) {
		var ready atomic.Bool
		ready.Store(true)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		healthHandler(&ready).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

// mockFetcher is a jwtSVIDFetcher whose behavior is driven by the caller.
type mockFetcher struct {
	calls       int
	lastParams  jwtsvid.Params
	failUntil   int
	errToReturn error
	svid        *jwtsvid.SVID
}

func (m *mockFetcher) FetchJWTSVID(_ context.Context, params jwtsvid.Params) (*jwtsvid.SVID, error) {
	m.calls++
	m.lastParams = params
	if m.calls <= m.failUntil {
		return nil, m.errToReturn
	}
	return m.svid, nil
}

func TestFetchJWTWithRetry(t *testing.T) {
	svid := &jwtsvid.SVID{Audience: []string{"nvcf-icms"}}

	t.Run("first attempt succeeds", func(t *testing.T) {
		f := &mockFetcher{svid: svid}
		got, err := fetchJWTWithRetryFunc(context.Background(), f, "nvcf-icms", 3, func(time.Duration) {})
		require.NoError(t, err)
		assert.Same(t, svid, got)
		assert.Equal(t, 1, f.calls)
		assert.Equal(t, "nvcf-icms", f.lastParams.Audience)
	})

	t.Run("succeeds after retries and back-off schedule is linear", func(t *testing.T) {
		f := &mockFetcher{svid: svid, failUntil: 2, errToReturn: errors.New("transient")}
		var sleeps []time.Duration
		got, err := fetchJWTWithRetryFunc(context.Background(), f, "aud", 3, func(d time.Duration) { sleeps = append(sleeps, d) })
		require.NoError(t, err)
		assert.Same(t, svid, got)
		assert.Equal(t, 3, f.calls)
		assert.Equal(t, []time.Duration{1 * time.Second, 2 * time.Second}, sleeps)
	})

	t.Run("exhausts retries and wraps last error", func(t *testing.T) {
		fatal := errors.New("always-fails")
		f := &mockFetcher{failUntil: 99, errToReturn: fatal}
		var sleeps []time.Duration
		_, err := fetchJWTWithRetryFunc(context.Background(), f, "aud", 3, func(d time.Duration) { sleeps = append(sleeps, d) })
		require.Error(t, err)
		assert.ErrorIs(t, err, fatal)
		assert.Equal(t, 3, f.calls)
		// No sleep after the final attempt.
		assert.Equal(t, []time.Duration{1 * time.Second, 2 * time.Second}, sleeps)
	})

	t.Run("maxRetries=1 skips sleep entirely", func(t *testing.T) {
		f := &mockFetcher{failUntil: 99, errToReturn: errors.New("nope")}
		var sleeps []time.Duration
		_, err := fetchJWTWithRetryFunc(context.Background(), f, "aud", 1, func(d time.Duration) { sleeps = append(sleeps, d) })
		require.Error(t, err)
		assert.Equal(t, 1, f.calls)
		assert.Empty(t, sleeps)
	})
}

func TestWriteTokenAtomic_OverwriteAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	require.NoError(t, writeTokenAtomic(path, []byte("first"), 0644))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))

	// Rewrite — overwrites cleanly without leaving temp files behind.
	require.NoError(t, writeTokenAtomic(path, []byte("second"), 0600))
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(got))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"perm bits should reflect the latest call")

	// No leftover .token.tmp.* siblings — the rename should have moved the
	// temp file in-place.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".token.tmp.",
			"expected no leftover temp files; saw %q", e.Name())
	}
}

// TestWriteTokenAtomic_NoPartialReadsUnderRace races N readers against a
// single writer rotating the token. Every read must observe either the empty
// initial state, a complete prior token, or the latest complete token —
// never a partial JWT.
func TestWriteTokenAtomic_NoPartialReadsUnderRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	// Tokens are large enough that a non-atomic O_TRUNC|WRITE would have a
	// visible window where readers see fewer bytes than the destination.
	tokens := [][]byte{
		[]byte("AAAAAAAAAA" + string(make([]byte, 1024))),
		[]byte("BBBBBBBBBB" + string(make([]byte, 1024))),
		[]byte("CCCCCCCCCC" + string(make([]byte, 1024))),
	}
	allowed := map[string]struct{}{"": {}}
	for _, tok := range tokens {
		allowed[string(tok)] = struct{}{}
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	const readers = 8
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				b, err := os.ReadFile(path)
				if err != nil && os.IsNotExist(err) {
					continue // pre-first-write window
				}
				require.NoError(t, err)
				_, ok := allowed[string(b)]
				assert.True(t, ok, "reader observed partial token of length %d", len(b))
			}
		}()
	}

	// Rotate the token many times while the readers race against the writer.
	for round := 0; round < 200; round++ {
		require.NoError(t, writeTokenAtomic(path, tokens[round%len(tokens)], 0644))
	}
	stop.Store(true)
	wg.Wait()
}
