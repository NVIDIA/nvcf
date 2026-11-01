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
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// writeTokenAtomic writes the token to outputPath atomically: it writes to a
// same-directory temp file then renames over the destination. This prevents
// concurrent readers (the agent, NATS, and ReVal token fetchers all read this
// path) from observing an empty or partial JWT during a refresh.
//
// Same-directory placement is required so the rename(2) is atomic on the same
// filesystem.
func writeTokenAtomic(outputPath string, tokenData []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(outputPath)
	tmp, err := os.CreateTemp(dir, ".token.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp token file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup of the temp file on any error path that returns
	// before the rename succeeds. After a successful rename the temp file is
	// gone, so Remove no-ops.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(tokenData); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp token file: %w", err)
	}
	if err = tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp token file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp token file: %w", err)
	}
	if err = os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("rename temp token file to %s: %w", outputPath, err)
	}
	return nil
}

// parseSocketPath validates and parses the socket URL.
func parseSocketPath(socketPath string) (string, error) {
	u, err := url.Parse(socketPath)
	if err != nil {
		return "", fmt.Errorf("invalid socket URL: %w", err)
	}
	if u.Scheme != "unix" {
		return "", fmt.Errorf("unsupported scheme '%s', only 'unix' is supported", u.Scheme)
	}
	if u.Path == "" {
		return "", fmt.Errorf("socket path cannot be empty")
	}
	return u.Path, nil
}

// calculateRefreshTime returns 80% of the token lifetime, with a 30s floor.
func calculateRefreshTime(issuedAt, expiresAt time.Time) time.Duration {
	lifetime := expiresAt.Sub(issuedAt)
	refreshIn := time.Duration(float64(lifetime) * 0.8)
	if refreshIn < 30*time.Second {
		refreshIn = 30 * time.Second
	}
	return refreshIn
}

// defaultSigningAlgorithms lists the JWT signature algorithms accepted when
// parsing a JWT-SVID's expiry claim. Pinning to the upstream SPIRE issuer's
// actual algorithm tightens the attack surface; this list is the conservative
// superset of RSA + ECDSA variants the upstream SPIRE server may issue today.
var defaultSigningAlgorithms = []string{
	string(jose.RS256), string(jose.RS384), string(jose.RS512),
	string(jose.ES256), string(jose.ES384), string(jose.ES512),
}

// parseSigningAlgorithms converts a comma-separated CLI string into the typed
// jose.SignatureAlgorithm list go-jose's parser expects. Empty input falls
// back to defaultSigningAlgorithms so a chart that doesn't set the knob keeps
// working.
func parseSigningAlgorithms(csv string) ([]jose.SignatureAlgorithm, error) {
	if csv == "" {
		out := make([]jose.SignatureAlgorithm, 0, len(defaultSigningAlgorithms))
		for _, a := range defaultSigningAlgorithms {
			out = append(out, jose.SignatureAlgorithm(a))
		}
		return out, nil
	}
	known := map[string]jose.SignatureAlgorithm{
		"RS256": jose.RS256, "RS384": jose.RS384, "RS512": jose.RS512,
		"PS256": jose.PS256, "PS384": jose.PS384, "PS512": jose.PS512,
		"ES256": jose.ES256, "ES384": jose.ES384, "ES512": jose.ES512,
		"EdDSA": jose.EdDSA,
	}
	parts := strings.Split(csv, ",")
	out := make([]jose.SignatureAlgorithm, 0, len(parts))
	for _, raw := range parts {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		alg, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("unknown JWT signing algorithm %q (valid: RS256/384/512, PS256/384/512, ES256/384/512, EdDSA)", name)
		}
		out = append(out, alg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one JWT signing algorithm must be configured")
	}
	return out, nil
}

// extractExpiry parses the JWT expiry claim without signature verification.
// algorithms restricts the set of acceptable JWS algorithms; pinning at the
// chart layer (via --signing-algorithms) lets a deployment narrow the
// accepted set to whatever the upstream SPIRE issuer actually emits.
func extractExpiry(token string, algorithms []jose.SignatureAlgorithm) (time.Time, error) {
	parsedJWT, err := jwt.ParseSigned(token, algorithms)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse JWT: %w", err)
	}
	var claims jwt.Claims
	if err := parsedJWT.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return time.Time{}, fmt.Errorf("failed to extract claims: %w", err)
	}
	if claims.Expiry == nil {
		return time.Time{}, fmt.Errorf("JWT has no expiry claim")
	}
	return claims.Expiry.Time(), nil
}

// jwtSVIDFetcher abstracts workloadapi.JWTSource.FetchJWTSVID so fetchJWTWithRetry
// can be unit-tested without a running SPIRE agent.
type jwtSVIDFetcher interface {
	FetchJWTSVID(ctx context.Context, params jwtsvid.Params) (*jwtsvid.SVID, error)
}

// fetchJWTWithRetry fetches a JWT-SVID with linear back-off (1s, 2s, ...).
func fetchJWTWithRetry(ctx context.Context, fetcher jwtSVIDFetcher, audience string, maxRetries int) (*jwtsvid.SVID, error) {
	return fetchJWTWithRetryFunc(ctx, fetcher, audience, maxRetries, time.Sleep)
}

// fetchJWTWithRetryFunc is the injectable-sleep variant used by tests.
func fetchJWTWithRetryFunc(
	ctx context.Context,
	fetcher jwtSVIDFetcher,
	audience string,
	maxRetries int,
	sleepFn func(time.Duration),
) (*jwtsvid.SVID, error) {
	var lastErr error
	params := jwtsvid.Params{Audience: audience}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		svid, err := fetcher.FetchJWTSVID(ctx, params)
		if err == nil {
			return svid, nil
		}
		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			log.Printf("Attempt %d failed: %v. Retrying in %v...", attempt, err, backoff)
			sleepFn(backoff)
		}
	}
	return nil, fmt.Errorf("failed to fetch JWT after %d attempts: %w", maxRetries, lastErr)
}

// healthHandler returns an HTTP handler that reports readiness.
func healthHandler(ready *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
}

func main() {
	var (
		socketPath        = flag.String("socket", "unix:///run/spire/spire-agent.sock", "Path to SPIRE Workload API socket (unix://...)")
		audience          = flag.String("audience", "nvcf-icms", "JWT audience")
		outputPath        = flag.String("output", "/var/run/secrets/tokens/token", "Output file path for JWT token")
		maxRetries        = flag.Int("retries", 3, "Maximum number of retry attempts")
		verbose           = flag.Bool("verbose", true, "Enable verbose logging")
		healthPort        = flag.Int("health-port", 8081, "Port for the /healthz endpoint")
		signingAlgorithms = flag.String("signing-algorithms", strings.Join(defaultSigningAlgorithms, ","),
			"Comma-separated JWT signature algorithms to accept when parsing the SVID's expiry claim. "+
				"Pin to whatever the upstream SPIRE issuer actually emits to tighten the parser's accepted set "+
				"(e.g., RS256). Valid: RS256/384/512, PS256/384/512, ES256/384/512, EdDSA.")
	)
	flag.Parse()

	algorithms, err := parseSigningAlgorithms(*signingAlgorithms)
	if err != nil {
		log.Fatalf("Invalid --signing-algorithms: %v", err)
	}

	var ready atomic.Bool

	if *verbose {
		log.Printf("Starting SPIFFE JWT token fetcher")
		log.Printf("Socket: %s", *socketPath)
		log.Printf("Audience: %s", *audience)
		log.Printf("Output: %s", *outputPath)
		log.Printf("Health port: %d", *healthPort)
	}

	// Start health endpoint.
	mux := http.NewServeMux()
	mux.Handle("/healthz", healthHandler(&ready))
	healthServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", *healthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Health server failed: %v", err)
		}
	}()

	ctx := context.Background()

	// Parse and validate socket path, then wait for it to appear. The err
	// shadow vs. parseSigningAlgorithms above is intentional; the loop below
	// still uses this binding.
	socketFilePath, err := parseSocketPath(*socketPath) //nolint:govet
	if err != nil {
		log.Fatalf("Invalid socket path: %v", err)
	}
	for {
		if _, err := os.Stat(socketFilePath); err == nil {
			break
		}
		if *verbose {
			log.Println("Waiting for SPIRE agent socket...")
		}
		time.Sleep(2 * time.Second)
	}
	if *verbose {
		log.Println("SPIRE agent socket found, connecting...")
	}

	// Ensure output directory exists before we open the JWT source.
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0755); err != nil {
		log.Fatalf("Unable to create output directory: %v", err)
	}

	// Create JWT source for fetching JWT-SVIDs. This process runs until killed, so the
	// JWT source lives for the entire process lifetime; no defer is needed.
	jwtSource, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(*socketPath)))
	if err != nil {
		log.Fatalf("Unable to create JWT source: %v", err)
	}

	// Main fetch-write-sleep loop.
	for {
		if *verbose {
			log.Println("Fetching SPIFFE JWT-SVID...")
		}

		spiffeJWT, err := fetchJWTWithRetry(ctx, jwtSource, *audience, *maxRetries)
		if err != nil {
			log.Fatalf("Unable to fetch JWT-SVID after %d attempts: %v", *maxRetries, err)
		}

		// Write raw SVID token to file (no exchange). Use an atomic temp+rename
		// so concurrent readers never see a partial token mid-refresh.
		tokenData := spiffeJWT.Marshal()
		if err := writeTokenAtomic(*outputPath, []byte(tokenData), 0644); err != nil {
			log.Fatalf("Failed to write JWT: %v", err)
		}

		if *verbose {
			log.Printf("JWT-SVID written to %s (%d chars, SPIFFE ID: %s)", *outputPath, len(tokenData), spiffeJWT.ID)
		}

		if !ready.Swap(true) && *verbose {
			log.Println("Health check is now ready")
		}

		// Determine refresh time from JWT expiry (80% of lifetime).
		issuedAt := time.Now()
		expiresAt, err := extractExpiry(tokenData, algorithms)
		if err != nil {
			log.Printf("Warning: could not extract expiry from JWT, defaulting to 5m refresh: %v", err)
			expiresAt = issuedAt.Add(5 * time.Minute)
		}
		refreshInterval := calculateRefreshTime(issuedAt, expiresAt)

		if *verbose {
			log.Printf("JWT expires at: %s", expiresAt.Format(time.RFC3339))
			log.Printf("Next refresh in: %v (80%% of lifetime)", refreshInterval)
		}

		time.Sleep(refreshInterval)
	}
}
