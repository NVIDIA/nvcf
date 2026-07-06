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

package nvca

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
)

// JWKSUpdater periodically checks the K8s OIDC JWKS and pushes updates to ICMS.
type JWKSUpdater struct {
	icmsURL                string
	icmsHostHeaderOverride string
	clusterID              string
	tokenPath              string
	interval               time.Duration
	lastHash               string
	k8sClient              *http.Client
	oidcClient             *http.Client
	icmsClient             *http.Client
	// k8sSATokenPath defaults to k8sSATokenPath and is overrideable in tests.
	k8sSATokenPath string
}

// JWKSUpdaterOptions configures a JWKSUpdater.
type JWKSUpdaterOptions struct {
	// ICMSURL is the base URL of the ICMS service to push JWKS to.
	ICMSURL string
	// ICMSHostHeaderOverride optionally overrides the Host header for ICMS pushes.
	ICMSHostHeaderOverride string
	// ClusterID identifies this cluster in the ICMS push path.
	ClusterID string
	// TokenPath is the projected SA token file the agent reads to authenticate
	// the ICMS push.
	TokenPath string
}

// NewJWKSUpdater creates a new JWKSUpdater that pushes JWKS changes to ICMS.
//
// Returns an error when the in-cluster K8s CA cert (k8sCACertPath) is
// unreadable: every supported deployment target (k3d, kind, EKS, GKE, AKS,
// kubeadm, OpenShift) projects this file via the standard service-account
// volume, so a missing CA cert reflects a real misconfiguration that must
// fail closed rather than silently degrade.
func NewJWKSUpdater(opts JWKSUpdaterOptions) (*JWKSUpdater, error) {
	return newJWKSUpdater(opts, k8sCACertPath)
}

func newJWKSUpdater(opts JWKSUpdaterOptions, caCertPath string) (*JWKSUpdater, error) {
	k8sClient, err := newK8sHTTPClientFromCAFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("build K8s HTTP client for JWKS updater: %w", err)
	}
	oidcClient, err := newOIDCHTTPClientFromCAFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("build OIDC HTTP client for JWKS updater: %w", err)
	}
	return &JWKSUpdater{
		icmsURL:                opts.ICMSURL,
		icmsHostHeaderOverride: opts.ICMSHostHeaderOverride,
		clusterID:              opts.ClusterID,
		tokenPath:              opts.TokenPath,
		interval:               30 * time.Minute,
		k8sClient:              k8sClient,
		oidcClient:             oidcClient,
		icmsClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		k8sSATokenPath: k8sSATokenPath,
	}, nil
}

// Start runs the JWKS updater loop and blocks until ctx is cancelled.
//
// The signature matches controller-runtime's {@code manager.Runnable}
// interface ({@code Start(ctx context.Context) error}). Today NVCA launches
// the updater as a bare goroutine (see Agent.Start), matching how other
// non-controller periodic loops in this package are wired. The Runnable-shaped
// signature means that if NVCA ever goes multi-replica, this can be
// registered with the controller-runtime manager and gated on leader election
// (Runnable.NeedLeaderElection → true) in one call — without changing the
// updater's own code.
//
// Returns nil on clean shutdown (ctx cancellation); never returns a non-nil
// error today because the poll loop logs and retries transient push failures
// itself rather than terminating.
func (u *JWKSUpdater) Start(ctx context.Context) error {
	log := core.GetLogger(ctx)
	log.WithField("interval", u.interval).Info("Starting JWKS updater")

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	// Initial check
	u.checkAndPush(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Info("JWKS updater stopped")
			return nil
		case <-ticker.C:
			u.checkAndPush(ctx)
		}
	}
}

// jwksPushBody is the JSON envelope for pushing JWKS to ICMS.
// ICMS expects `jwks` as a JSON-encoded string (the JWKS JSON serialized),
// not as a nested JSON object.
type jwksPushBody struct {
	JWKS string `json:"jwks"`
}

// k8sCACertPath is the standard location for the K8s service account CA certificate.
const k8sCACertPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

// k8sSATokenPath is the standard location for the K8s service account token.
const k8sSATokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

const k8sJWKSURL = "https://kubernetes.default.svc/openid/v1/jwks"

const oidcDiscoveryPath = "/.well-known/openid-configuration"

type oidcDiscoveryConfig struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwtIssuerClaims struct {
	Issuer string `json:"iss"`
}

type jwtHeader struct {
	KeyID string `json:"kid"`
}

type projectedTokenMetadata struct {
	Issuer string
	KeyID  string
}

// newK8sHTTPClient creates an HTTP client that trusts the in-cluster K8s API
// server. The K8s service-account CA cert at k8sCACertPath is required; a
// missing/unreadable CA cert is a hard error so the agent surfaces the
// misconfiguration. There is intentionally no insecure-skip-verify escape
// hatch: every supported deployment target (including local k3d/kind)
// projects this file via the standard service-account volume.
func newK8sHTTPClient() (*http.Client, error) {
	return newK8sHTTPClientFromCAFile(k8sCACertPath)
}

func newK8sHTTPClientFromCAFile(caCertPath string) (*http.Client, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read K8s CA cert at %s: %w", caCertPath, err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse K8s CA cert at %s: no PEM certificates found", caCertPath)
	}

	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
	}, nil
}

func newOIDCHTTPClientFromCAFile(caCertPath string) (*http.Client, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read K8s CA cert at %s: %w", caCertPath, err)
	}

	caCertPool, err := x509.SystemCertPool()
	if err != nil || caCertPool == nil {
		caCertPool = x509.NewCertPool()
	}
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse K8s CA cert at %s: no PEM certificates found", caCertPath)
	}

	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
	}, nil
}

func (u *JWKSUpdater) checkAndPush(ctx context.Context) {
	log := core.GetLogger(ctx)

	jwksData, err := u.fetchJWKS(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to fetch JWKS")
		return
	}

	// Check if changed
	hash := sha256.Sum256(jwksData)
	hashStr := hex.EncodeToString(hash[:])
	if hashStr == u.lastHash {
		return // No change
	}

	log.WithField("hash", hashStr[:12]).Info("JWKS changed, pushing to ICMS")

	// Read current token
	token, err := os.ReadFile(u.tokenPath)
	if err != nil {
		log.WithError(err).Error("Failed to read PSAT token for JWKS push")
		return
	}

	// Push to ICMS using structured JSON marshaling to prevent injection
	pushURL := fmt.Sprintf("%s/v1/nvca/clusters/%s/jwks", u.icmsURL, u.clusterID)
	bodyBytes, err := json.Marshal(jwksPushBody{JWKS: string(jwksData)})
	if err != nil {
		log.WithError(err).Error("Failed to marshal JWKS push body")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, pushURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		log.WithError(err).Error("Failed to create JWKS push request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if u.icmsHostHeaderOverride != "" {
		req.Host = u.icmsHostHeaderOverride
	}

	metrics := nvcametrics.FromContext(ctx)

	pushResp, err := u.icmsClient.Do(req)
	if err != nil {
		log.WithError(err).Error("Failed to push JWKS to ICMS")
		metrics.RecordUpstreamRequest(nvcametrics.UpstreamOperationJWKSPush, err)
		return
	}
	defer pushResp.Body.Close()

	if pushResp.StatusCode == http.StatusOK {
		u.lastHash = hashStr
		log.Info("JWKS pushed to ICMS successfully")
		metrics.RecordUpstreamRequest(nvcametrics.UpstreamOperationJWKSPush, nil)
	} else {
		respBody, _ := io.ReadAll(pushResp.Body)
		log.WithField("status", pushResp.StatusCode).WithField("body", string(respBody)).Error("ICMS rejected JWKS push")
		// Record the rejection through the same metric pipeline as other upstream
		// calls so SREs can alert on `nvca_upstream_request_total{operation="jwks-push", status="failure"}`
		// instead of grepping logs for the rejection line.
		metrics.RecordUpstreamRequest(nvcametrics.UpstreamOperationJWKSPush, nvcaerrors.HTTPStatusError(pushResp.StatusCode, nil))
	}
}

// fetchJWKS prefers the projected-token issuer JWKS because that is the key set
// external verifiers should use for the current service-account token. When the
// issuer is the in-cluster Kubernetes API server, discovery/JWKS may require the
// mounted Kubernetes service-account token; NVCA only sends that token to
// recognized in-cluster API server issuers. The in-cluster Kubernetes JWKS is
// only a guarded fallback for issuer reachability failures, and only when it
// contains the current projected token kid. All other issuer errors fail closed
// so NVCA does not push a stale or mismatched JWKS to ICMS.
func (u *JWKSUpdater) fetchJWKS(ctx context.Context) ([]byte, error) {
	log := core.GetLogger(ctx)
	tokenMetadata, err := projectedTokenMetadataFromJWTFile(u.tokenPath)
	if err != nil {
		return nil, err
	}
	if tokenMetadata.Issuer == "" {
		return nil, fmt.Errorf("projected token missing issuer")
	}

	jwksData, err := u.fetchJWKSFromIssuer(ctx, tokenMetadata.Issuer)
	if err == nil {
		return jwksData, nil
	}
	if !isIssuerReachabilityError(err) {
		return nil, fmt.Errorf("fetch JWKS from projected token issuer %q: %w", tokenMetadata.Issuer, err)
	}
	if tokenMetadata.KeyID == "" {
		return nil, fmt.Errorf("fetch JWKS from projected token issuer %q: %w; not falling back to internal K8s JWKS because projected token has no kid", tokenMetadata.Issuer, err)
	}

	log.WithError(err).WithField("kid", tokenMetadata.KeyID).Warn(
		"Projected token issuer JWKS is unreachable; trying guarded K8s API JWKS fallback")

	internalJWKS, fallbackErr := u.fetchJWKSFromK8s(ctx)
	if fallbackErr != nil {
		return nil, fmt.Errorf("fetch JWKS from projected token issuer %q: %w; guarded K8s JWKS fallback failed: %v", tokenMetadata.Issuer, err, fallbackErr)
	}
	containsKid, containsErr := jwksContainsKeyID(internalJWKS, tokenMetadata.KeyID)
	if containsErr != nil {
		return nil, fmt.Errorf("fetch JWKS from projected token issuer %q: %w; validate guarded K8s JWKS fallback: %v", tokenMetadata.Issuer, err, containsErr)
	}
	if !containsKid {
		return nil, fmt.Errorf("fetch JWKS from projected token issuer %q: %w; internal K8s JWKS does not contain projected token kid %q", tokenMetadata.Issuer, err, tokenMetadata.KeyID)
	}

	log.WithField("kid", tokenMetadata.KeyID).Warn(
		"Using internal K8s JWKS fallback because it contains the current projected token kid")
	return internalJWKS, nil
}

func (u *JWKSUpdater) k8sSATokenFile() string {
	if u.k8sSATokenPath != "" {
		return u.k8sSATokenPath
	}
	return k8sSATokenPath
}

func (u *JWKSUpdater) fetchJWKSFromK8s(ctx context.Context) ([]byte, error) {
	saToken, err := os.ReadFile(u.k8sSATokenFile())
	if err != nil {
		return nil, fmt.Errorf("read K8s SA token for JWKS fetch: %w", err)
	}

	jwksReq, err := http.NewRequestWithContext(ctx, http.MethodGet, k8sJWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create K8s JWKS fetch request: %w", err)
	}
	jwksReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(saToken)))

	resp, err := u.k8sClient.Do(jwksReq)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS from K8s API server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("K8s API returned non-200 for JWKS: status=%d body=%s", resp.StatusCode, string(body))
	}

	jwksData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read K8s JWKS response: %w", err)
	}
	return jwksData, nil
}

func (u *JWKSUpdater) fetchJWKSFromIssuer(ctx context.Context, issuerURL string) ([]byte, error) {
	discoveryURL, err := oidcDiscoveryURL(issuerURL)
	if err != nil {
		return nil, err
	}

	client := u.oidcClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, usedK8sAuth, err := u.doIssuerRequest(ctx, client, issuerURL, discoveryURL, false)
	if err != nil {
		return nil, fmt.Errorf("fetch OIDC discovery document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OIDC discovery returned non-200: status=%d body=%s", resp.StatusCode, string(body))
	}

	var discovery oidcDiscoveryConfig
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("decode OIDC discovery document: %w", err)
	}
	if discovery.Issuer == "" {
		return nil, fmt.Errorf("OIDC discovery document missing issuer")
	}
	if err := requireMatchingOIDCIssuer(issuerURL, discovery.Issuer); err != nil {
		return nil, err
	}
	if discovery.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC discovery document missing jwks_uri")
	}
	jwksURL, err := resolveOIDCJWKSURL(issuerURL, discovery.JWKSURI)
	if err != nil {
		return nil, err
	}

	jwksResp, _, err := u.doIssuerRequest(ctx, client, issuerURL, jwksURL, usedK8sAuth)
	if err != nil {
		return nil, fmt.Errorf("fetch issuer JWKS: %w", err)
	}
	defer jwksResp.Body.Close()
	if jwksResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(jwksResp.Body)
		return nil, fmt.Errorf("issuer JWKS endpoint returned non-200: status=%d body=%s", jwksResp.StatusCode, string(body))
	}

	jwksData, err := io.ReadAll(jwksResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read issuer JWKS response: %w", err)
	}
	return jwksData, nil
}

func (u *JWKSUpdater) doIssuerRequest(ctx context.Context, client *http.Client, issuerURL, requestURL string, useK8sAuth bool) (*http.Response, bool, error) {
	resp, err := u.doSingleIssuerRequest(ctx, client, requestURL, useK8sAuth)
	if err != nil || useK8sAuth || !shouldRetryIssuerRequestWithK8sAuth(issuerURL, resp.StatusCode) {
		return resp, useK8sAuth, err
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	resp, err = u.doSingleIssuerRequest(ctx, client, requestURL, true)
	if err != nil {
		return nil, true, fmt.Errorf("retry with K8s service-account token: %w", err)
	}
	return resp, true, nil
}

func (u *JWKSUpdater) doSingleIssuerRequest(ctx context.Context, client *http.Client, requestURL string, useK8sAuth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create OIDC request: %w", err)
	}
	if useK8sAuth {
		if err := u.addK8sAuth(req); err != nil {
			return nil, err
		}
	}
	return client.Do(req)
}

func (u *JWKSUpdater) addK8sAuth(req *http.Request) error {
	saToken, err := os.ReadFile(u.k8sSATokenFile())
	if err != nil {
		return fmt.Errorf("read K8s SA token for issuer fetch: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(saToken)))
	return nil
}

func shouldRetryIssuerRequestWithK8sAuth(issuerURL string, statusCode int) bool {
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return false
	}
	return isInClusterKubernetesIssuer(issuerURL)
}

func isInClusterKubernetesIssuer(issuerURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(issuerURL))
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch host {
	case "kubernetes.default.svc", "kubernetes.default.svc.cluster.local":
		return true
	}

	kubernetesServiceHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))), ".")
	return kubernetesServiceHost != "" && host == kubernetesServiceHost
}

func oidcDiscoveryURL(issuerURL string) (string, error) {
	normalized, err := normalizeOIDCIssuerURL(issuerURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse OIDC issuer URL: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + oidcDiscoveryPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func requireMatchingOIDCIssuer(expectedIssuer, discoveryIssuer string) error {
	expected, err := normalizeOIDCIssuerURL(expectedIssuer)
	if err != nil {
		return err
	}
	actual, err := normalizeOIDCIssuerURL(discoveryIssuer)
	if err != nil {
		return fmt.Errorf("parse OIDC discovery issuer URL: %w", err)
	}
	if expected != actual {
		return fmt.Errorf("OIDC discovery issuer mismatch: token issuer=%q discovery issuer=%q", expected, actual)
	}
	return nil
}

func normalizeOIDCIssuerURL(issuerURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(issuerURL), "/"))
	if err != nil {
		return "", fmt.Errorf("parse OIDC issuer URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("OIDC issuer URL must be absolute")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func resolveOIDCJWKSURL(issuerURL, jwksURI string) (string, error) {
	jwksURL, err := url.Parse(jwksURI)
	if err != nil {
		return "", fmt.Errorf("parse OIDC jwks_uri: %w", err)
	}
	if jwksURL.IsAbs() {
		return jwksURL.String(), nil
	}

	normalizedIssuer, err := normalizeOIDCIssuerURL(issuerURL)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(strings.TrimRight(normalizedIssuer, "/") + "/")
	if err != nil {
		return "", fmt.Errorf("parse OIDC issuer URL: %w", err)
	}
	return base.ResolveReference(jwksURL).String(), nil
}

func isIssuerReachabilityError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	if os.IsTimeout(err) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// Private clusters may be unable to resolve an otherwise valid projected-token issuer.
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isIssuerReachabilityError(urlErr.Err)
	}
	return false
}

func jwksContainsKeyID(jwksData []byte, keyID string) (bool, error) {
	if strings.TrimSpace(keyID) == "" {
		return false, fmt.Errorf("projected token kid is empty")
	}
	var jwks struct {
		Keys []struct {
			KeyID string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwksData, &jwks); err != nil {
		return false, fmt.Errorf("decode JWKS: %w", err)
	}
	for _, key := range jwks.Keys {
		if key.KeyID == keyID {
			return true, nil
		}
	}
	return false, nil
}

func projectedTokenMetadataFromJWTFile(tokenPath string) (projectedTokenMetadata, error) {
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return projectedTokenMetadata{}, fmt.Errorf("read projected token: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(tokenBytes)), ".")
	if len(parts) < 2 {
		return projectedTokenMetadata{}, fmt.Errorf("parse projected token: expected JWT with at least two segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return projectedTokenMetadata{}, fmt.Errorf("decode projected token header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return projectedTokenMetadata{}, fmt.Errorf("unmarshal projected token header: %w", err)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return projectedTokenMetadata{}, fmt.Errorf("decode projected token claims: %w", err)
	}
	var claims jwtIssuerClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return projectedTokenMetadata{}, fmt.Errorf("unmarshal projected token claims: %w", err)
	}
	return projectedTokenMetadata{
		Issuer: strings.TrimSpace(claims.Issuer),
		KeyID:  strings.TrimSpace(header.KeyID),
	}, nil
}
