/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Package auth provides fingerprint-based token caching for self-hosted NVCF
// control planes. The CLI probes the control plane's OIDC metadata on each
// `up` run and compares the resulting Fingerprint against the one stored
// alongside the cached admin token. A match means the cached token is still
// valid for this installation and the API-Keys service call can be skipped.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fingerprint identifies a control-plane installation. The CLI persists this
// alongside the cached admin token; on the next `up`, the orchestrator probes
// the control plane's metadata and compares fingerprints to decide whether
// the cached token is valid for the current installation. Mismatch → re-mint.
//
// Fields:
//
//	IssuerURL       — value of `issuer` in /.well-known/openid-configuration
//	JWKSKid         — kid of keys[0] in /.well-known/jwks.json (current signing key)
//	APIKeysEndpoint — URL of the API Keys service derived from issuer + path
type Fingerprint struct {
	IssuerURL       string `json:"issuerURL"`
	JWKSKid         string `json:"jwksKid"`
	APIKeysEndpoint string `json:"apiKeysEndpoint"`
}

// Equal returns true when both fingerprints are non-nil and all 3 fields match.
func (f *Fingerprint) Equal(other *Fingerprint) bool {
	if f == nil || other == nil {
		return false
	}
	return f.IssuerURL == other.IssuerURL &&
		f.JWKSKid == other.JWKSKid &&
		f.APIKeysEndpoint == other.APIKeysEndpoint
}

// Hash returns a stable 12-hex-char digest of the fingerprint, useful for
// log redaction (avoids leaking issuer URLs in log files).
func (f *Fingerprint) Hash() string {
	h := sha256.Sum256([]byte(f.IssuerURL + "|" + f.JWKSKid + "|" + f.APIKeysEndpoint))
	return hex.EncodeToString(h[:])[:12]
}

// Probe fetches OIDC + JWKS metadata from a control plane base URL and
// constructs a Fingerprint. Returns a *ProbeError with Category="cache_corruption"
// if the server returns malformed metadata or a 5xx; Category="network" for
// connection failures.
func Probe(ctx context.Context, baseURL string) (*Fingerprint, error) {
	return ProbeWithClient(ctx, baseURL, http.DefaultClient)
}

// ProbeWithClient is the test seam — accepts an injected HTTP client (e.g.
// httptest.Server.Client()) so probe tests don't need TLS plumbing.
func ProbeWithClient(ctx context.Context, baseURL string, client *http.Client) (*Fingerprint, error) {
	base := strings.TrimRight(baseURL, "/")

	var oidc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	oidcURL := base + "/.well-known/openid-configuration"
	if err := getJSON(ctx, client, oidcURL, &oidc); err != nil {
		return nil, fmt.Errorf("probe oidc: %w", err)
	}
	if oidc.Issuer == "" || oidc.JWKSURI == "" {
		return nil, &ProbeError{Category: "cache_corruption", Msg: "oidc response missing issuer or jwks_uri"}
	}

	// Resolve relative JWKS URIs against the base URL so test servers that
	// return path-only jwks_uri values (e.g. "/.well-known/jwks.json") work
	// without knowing their own address upfront.
	jwksURL := oidc.JWKSURI
	if strings.HasPrefix(jwksURL, "/") {
		jwksURL = base + jwksURL
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, client, jwksURL, &jwks); err != nil {
		return nil, fmt.Errorf("probe jwks: %w", err)
	}
	if len(jwks.Keys) == 0 {
		return nil, &ProbeError{Category: "cache_corruption", Msg: "jwks response has no keys"}
	}

	apiKeys, err := deriveAPIKeysEndpoint(oidc.Issuer)
	if err != nil {
		return nil, err
	}

	return &Fingerprint{
		IssuerURL:       oidc.Issuer,
		JWKSKid:         jwks.Keys[0].Kid,
		APIKeysEndpoint: apiKeys,
	}, nil
}

// getJSON performs a GET request and JSON-decodes the response body into dst.
// Returns a *ProbeError for 5xx responses and a plain error for other non-200s.
func getJSON(ctx context.Context, client *http.Client, urlStr string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return &ProbeError{
			Category: "cache_corruption",
			Msg:      fmt.Sprintf("%d from %s", resp.StatusCode, urlStr),
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%d from %s: %s", resp.StatusCode, urlStr, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// deriveAPIKeysEndpoint maps the OIDC issuer to the corresponding API Keys
// service URL. Uses issuer + "/api-keys" as the convention; the stack's
// ingress routes /api-keys under the same host as the issuer.
func deriveAPIKeysEndpoint(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(u.String(), "/") + "/api-keys", nil
}

// ProbeError tags a probe failure with the orchestrator-error category that
// callers should propagate when they emit phase_failed.
type ProbeError struct {
	Category string // "cache_corruption" | "network"
	Msg      string
}

func (e *ProbeError) Error() string { return e.Msg }

// Cache holds the persistent auth state: token + fingerprint of the issuer
// the token was minted by.
type Cache struct {
	Token       string       `json:"token,omitempty"`
	ExpiresAt   time.Time    `json:"expiresAt,omitempty"`
	Fingerprint *Fingerprint `json:"fingerprint,omitempty"`
}

// Valid returns true when the cache has a non-empty token, isn't expired, and
// the fingerprint matches the supplied current-installation fingerprint.
func (c *Cache) Valid(now time.Time, current *Fingerprint) bool {
	if c == nil || c.Token == "" {
		return false
	}
	if !c.ExpiresAt.IsZero() && now.After(c.ExpiresAt) {
		return false
	}
	return c.Fingerprint.Equal(current)
}
