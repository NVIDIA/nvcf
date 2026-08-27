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

package operator

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const defaultCertRefreshPeriod = 24 * time.Hour

// runTLSCertRotate starts a blocking loop that checks NVCA TLS certs for expiration every certRefreshPeriod.
// If certs will expire within 2 weeks, it attempts to regenerate them and update their secrets.
// NVCA's webhook server is configured with "--tls-secret-name" so secret updates are handled.
func (bc *BackendK8sCache) runTLSCertRotate(ctx context.Context, certRefreshPeriod time.Duration) {
	log := core.GetLogger(ctx)
	ticker := time.NewTicker(certRefreshPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nbs, err := bc.nvcfBackendLister.List(labels.Everything())
			if err != nil {
				log.WithError(err).Error("Failed to list NVCFBackends for TLS cert refresh")
				continue
			}
			if len(nbs) != 1 {
				log.Errorf("Unexpected number of NVCFBackends found for TLS cert refresh: %d", len(nbs))
				continue
			}
			if err := bc.rotateTLSCert(ctx, nbs[0], bc.now()); err != nil {
				log.WithError(err).Error("Failed to refresh TLS cert, must be done manually")
			}
		}
	}
}

func (bc *BackendK8sCache) rotateTLSCert(ctx context.Context, nb *nvidiaiov1.NVCFBackend, now time.Time) error {
	log := core.GetLogger(ctx)

	secretClient := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb))
	needsUpdate := false
	for secretName, dataKey := range map[string]string{
		NVCAWebhookTLSCertSecretName: TLSCertName,
		NVCAWebhookTLSCASecretName:   TLSCAName,
	} {
		certSecret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get webhook TLS cert secret: %w", err)
		}

		cert, err := parseCertPEM(certSecret.Data[dataKey])
		if err != nil {
			return fmt.Errorf("decode webhook TLS cert in key %q: %w", dataKey, err)
		}

		// Check if cert expires in less than two weeks.
		if now.AddDate(0, 0, 14).After(cert.NotAfter) {
			needsUpdate = true
			break
		}
	}

	if !needsUpdate {
		log.Debug("Webhook TLS certs are valid for at least another two weeks")
		return nil
	}

	log.Info("Rotating webhook TLS certs")

	webhookCert, err := generateWebhookCerts(nb, now)
	if err != nil {
		return fmt.Errorf("generate updated webhook TLS certs: %w", err)
	}
	if err := bc.setupWebhookSecrets(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("update webhook secrets: %w", err)
	}
	return nil
}

// parseCertPEM decodes a single PEM-encoded CERTIFICATE block and parses it into
// an x509.Certificate.
func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("no CERTIFICATE PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ensureWebhookCert returns the webhook serving certificate and its CA, reusing the
// material already stored in the TLS secrets whenever it is still present and
// internally consistent, and only minting a fresh certificate when the stored
// material is missing, unparseable, mismatched, or expired.
//
// generateWebhookCerts mints a brand-new self-signed CA on every call, so invoking
// it on every rollout continuously rewrites both the serving cert secret and the
// webhook caBundle. The nvca pod only picks up a new serving cert after it
// restarts, so during that window the served cert no longer chains to the freshly
// written caBundle and admission calls fail with "x509: certificate signed by
// unknown authority". Reusing the stored certificate keeps the serving cert and
// the caBundle stable across reconciles and eliminates that drift. Proactive
// renewal ahead of expiry is handled separately by rotateTLSCert.
func (bc *BackendK8sCache) ensureWebhookCert(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, now time.Time,
) (WebhookCert, error) {
	existing, ok, err := bc.reusableWebhookCert(ctx, nb, now)
	if err != nil {
		return WebhookCert{}, err
	}
	if ok {
		core.GetLogger(ctx).Debug("reusing existing webhook TLS certs")
		return existing, nil
	}
	return generateWebhookCerts(nb, now)
}

// reusableWebhookCert loads the stored webhook certificate and reports whether it
// can be reused as-is. It returns ok=false (nil error) when the certificate is
// absent or no longer trustworthy, so the caller mints a replacement. A non-nil
// error is reserved for transient failures reading the secrets, so a read blip
// never causes the certificate to be regenerated.
func (bc *BackendK8sCache) reusableWebhookCert(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, now time.Time,
) (WebhookCert, bool, error) {
	log := core.GetLogger(ctx)
	secretClient := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb))

	tlsSecret, err := secretClient.Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		log.Info("Generating webhook TLS certs: server cert secret not found")
		return WebhookCert{}, false, nil
	}
	if err != nil {
		return WebhookCert{}, false, fmt.Errorf("get %s: %w", NVCAWebhookTLSCertSecretName, err)
	}

	caSecret, err := secretClient.Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		log.Info("Generating webhook TLS certs: CA secret not found")
		return WebhookCert{}, false, nil
	}
	if err != nil {
		return WebhookCert{}, false, fmt.Errorf("get %s: %w", NVCAWebhookTLSCASecretName, err)
	}

	tlsCert, tlsKey, caBytes := tlsSecret.Data[TLSCertName], tlsSecret.Data[TLSKeyName], caSecret.Data[TLSCAName]
	if len(tlsCert) == 0 || len(tlsKey) == 0 || len(caBytes) == 0 {
		log.Info("Generating webhook TLS certs: stored cert material is incomplete")
		return WebhookCert{}, false, nil
	}

	servingCert, err := parseCertPEM(tlsCert)
	if err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored server cert is not parseable")
		return WebhookCert{}, false, nil
	}
	caCert, err := parseCertPEM(caBytes)
	if err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored CA cert is not parseable")
		return WebhookCert{}, false, nil
	}
	if now.After(servingCert.NotAfter) {
		log.Info("Generating webhook TLS certs: stored server cert has expired")
		return WebhookCert{}, false, nil
	}
	// The CA is written to the webhook caBundle; an expired CA is rejected by the
	// API server ("x509: certificate has expired"), so it must be regenerated even
	// when the serving cert still has time left.
	if now.After(caCert.NotAfter) {
		log.Info("Generating webhook TLS certs: stored CA cert has expired")
		return WebhookCert{}, false, nil
	}
	// The serving cert and CA are always written together, so a broken chain means
	// the stored material is inconsistent; regenerate a matching pair to repair it.
	if err := servingCert.CheckSignatureFrom(caCert); err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored server cert is not signed by the stored CA")
		return WebhookCert{}, false, nil
	}

	return WebhookCert{CACertBytes: caBytes, TLSCert: tlsCert, TLSKey: tlsKey}, true, nil
}
