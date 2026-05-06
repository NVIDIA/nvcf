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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
)

// JWKSUpdater periodically checks the K8s OIDC JWKS and pushes updates to ICMS.
type JWKSUpdater struct {
	icmsURL    string
	clusterID  string
	tokenPath  string
	interval   time.Duration
	lastHash   string
	k8sClient  *http.Client
	icmsClient *http.Client
}

// JWKSUpdaterOptions configures a JWKSUpdater.
type JWKSUpdaterOptions struct {
	// ICMSURL is the base URL of the ICMS service to push JWKS to.
	ICMSURL string
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
	k8sClient, err := newK8sHTTPClient()
	if err != nil {
		return nil, fmt.Errorf("build K8s HTTP client for JWKS updater: %w", err)
	}
	return &JWKSUpdater{
		icmsURL:   opts.ICMSURL,
		clusterID: opts.ClusterID,
		tokenPath: opts.TokenPath,
		interval:  30 * time.Minute,
		k8sClient: k8sClient,
		icmsClient: &http.Client{
			Timeout: 10 * time.Second,
		},
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

// newK8sHTTPClient creates an HTTP client that trusts the in-cluster K8s API
// server. The K8s service-account CA cert at k8sCACertPath is required; a
// missing/unreadable CA cert is a hard error so the agent surfaces the
// misconfiguration. There is intentionally no insecure-skip-verify escape
// hatch: every supported deployment target (including local k3d/kind)
// projects this file via the standard service-account volume.
func newK8sHTTPClient() (*http.Client, error) {
	caCert, err := os.ReadFile(k8sCACertPath)
	if err != nil {
		return nil, fmt.Errorf("read K8s CA cert at %s: %w", k8sCACertPath, err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse K8s CA cert at %s: no PEM certificates found", k8sCACertPath)
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

	// Authenticate to K8s API server using the pod's service account token.
	// The /openid/v1/jwks endpoint requires authentication on most clusters.
	saToken, err := os.ReadFile(k8sSATokenPath)
	if err != nil {
		log.WithError(err).Error("Failed to read K8s SA token for JWKS fetch")
		return
	}

	jwksReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kubernetes.default.svc/openid/v1/jwks", nil)
	if err != nil {
		log.WithError(err).Error("Failed to create JWKS fetch request")
		return
	}
	jwksReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(saToken)))

	resp, err := u.k8sClient.Do(jwksReq)
	if err != nil {
		log.WithError(err).Error("Failed to fetch JWKS from K8s API server")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.WithField("status", resp.StatusCode).WithField("body", string(body)).Error("K8s API returned non-200 for JWKS")
		return
	}

	jwksData, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Error("Failed to read JWKS response")
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
