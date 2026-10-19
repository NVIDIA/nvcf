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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const defaultCertRefreshPeriod = 24 * time.Hour

// runTLSCertRotate starts a blocking loop that checks NVCA TLS certs for expiration every certRefreshPeriod.
// If certs will expire within 2 weeks, it attempts to regenerate them and update their secrets
// and the webhook caBundles.
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

	stored, err := secretClient.Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get webhook TLS cert secret: %w", err)
	}
	generated, err := generateWebhookCerts(nb, now)
	if err != nil {
		return fmt.Errorf("generate updated webhook TLS certs: %w", err)
	}
	webhookCert, err := bc.claimOrAdoptWebhookCert(ctx, nb, generated, stored, now)
	if err != nil {
		return err
	}
	if err := bc.setupWebhookSecrets(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("update webhook secrets: %w", err)
	}
	// The API server only trusts the caBundle in the webhook configurations, so a
	// rotated CA that is written only to the Secrets rejects every admission call.
	if err := bc.updateWebhookCABundles(ctx, webhookCert.CACertBytes); err != nil {
		return fmt.Errorf("update webhook caBundles: %w", err)
	}
	return nil
}

// updateWebhookCABundles sets caBundle on every webhook in the NVCA validating and
// mutating webhook configurations and leaves the rest of each configuration as setup
// wrote it. A configuration that does not exist yet is skipped; setup creates it.
func (bc *BackendK8sCache) updateWebhookCABundles(ctx context.Context, caBundle []byte) error {
	admission := bc.clients.K8s.AdmissionregistrationV1()
	name := nvcaoptypes.NVCAModuleName

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vwc, err := admission.ValidatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for i := range vwc.Webhooks {
			vwc.Webhooks[i].ClientConfig.CABundle = caBundle
		}
		_, err = admission.ValidatingWebhookConfigurations().Update(ctx, vwc, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("update %v ValidatingWebhookConfiguration: %w", name, err)
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mwc, err := admission.MutatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for i := range mwc.Webhooks {
			mwc.Webhooks[i].ClientConfig.CABundle = caBundle
		}
		_, err = admission.MutatingWebhookConfigurations().Update(ctx, mwc, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("update %v MutatingWebhookConfiguration: %w", name, err)
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
//
// Concurrent reconciles (or an overlapping operator replica) can all find the
// certificate missing at once. Each mints its own CA, so only one of them may
// publish: the serving cert Secret is claimed with a create, or an update
// preconditioned on the version that was read, and a writer that loses the claim
// adopts the stored certificate instead of overwriting it. That keeps every
// writer publishing the same CA to the webhook caBundles.
func (bc *BackendK8sCache) ensureWebhookCert(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, now time.Time,
) (WebhookCert, error) {
	log := core.GetLogger(ctx)
	existing, stored, ok, err := bc.reusableWebhookCert(ctx, nb, now)
	if err != nil {
		return WebhookCert{}, err
	}
	if ok {
		log.Debug("reusing existing webhook TLS certs")
		return existing, nil
	}

	generated, err := generateWebhookCerts(nb, now)
	if err != nil {
		return WebhookCert{}, err
	}
	return bc.claimOrAdoptWebhookCert(ctx, nb, generated, stored, now)
}

// claimOrAdoptWebhookCert stores generated as the webhook certificate if the serving
// cert Secret is unchanged since stored was read, and returns it. If another writer
// changed the Secret first, it returns the certificate that writer stored instead.
func (bc *BackendK8sCache) claimOrAdoptWebhookCert(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, generated WebhookCert, stored *v1.Secret, now time.Time,
) (WebhookCert, error) {
	claimed, err := bc.claimWebhookCertSecret(ctx, nb, generated, stored)
	if err != nil {
		return WebhookCert{}, err
	}
	if claimed {
		return generated, nil
	}

	existing, _, ok, err := bc.reusableWebhookCert(ctx, nb, now)
	if err != nil {
		return WebhookCert{}, err
	}
	if !ok {
		return WebhookCert{}, fmt.Errorf("%s was replaced concurrently with unusable cert material", NVCAWebhookTLSCertSecretName)
	}
	core.GetLogger(ctx).Info("Adopting webhook TLS certs stored by a concurrent writer")
	return existing, nil
}

// claimWebhookCertSecret writes wc to the serving cert Secret only if the Secret
// is unchanged since it was read: created when stored is nil, otherwise updated at
// stored's resourceVersion. It returns false, without error, when another writer
// changed the Secret first.
func (bc *BackendK8sCache) claimWebhookCertSecret(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, wc WebhookCert, stored *v1.Secret,
) (bool, error) {
	secretClient := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb))
	desired := newTLSCertSecret(nb, wc)

	var err error
	if stored == nil {
		_, err = secretClient.Create(ctx, desired, metav1.CreateOptions{})
	} else {
		desired.ResourceVersion = stored.ResourceVersion
		_, err = secretClient.Update(ctx, desired, metav1.UpdateOptions{})
	}
	switch {
	case err == nil:
		return true, nil
	case k8serrors.IsAlreadyExists(err), k8serrors.IsConflict(err):
		return false, nil
	default:
		return false, fmt.Errorf("store %s: %w", NVCAWebhookTLSCertSecretName, err)
	}
}

// reusableWebhookCert loads the stored webhook certificate and reports whether it
// can be reused as-is. It returns ok=false (nil error) when the certificate is
// absent or no longer trustworthy, so the caller mints a replacement. A non-nil
// error is reserved for transient failures reading the secrets, so a read blip
// never causes the certificate to be regenerated. The serving cert Secret is
// returned whenever it exists, so a replacement can be written conditionally on it.
func (bc *BackendK8sCache) reusableWebhookCert(
	ctx context.Context, nb *nvidiaiov1.NVCFBackend, now time.Time,
) (WebhookCert, *v1.Secret, bool, error) {
	log := core.GetLogger(ctx)
	secretClient := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb))

	tlsSecret, err := secretClient.Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		log.Info("Generating webhook TLS certs: server cert secret not found")
		return WebhookCert{}, nil, false, nil
	}
	if err != nil {
		return WebhookCert{}, nil, false, fmt.Errorf("get %s: %w", NVCAWebhookTLSCertSecretName, err)
	}

	tlsCert, tlsKey, caBytes := tlsSecret.Data[TLSCertName], tlsSecret.Data[TLSKeyName], tlsSecret.Data[TLSCAName]
	// Secrets written before the CA was stored alongside the serving cert only
	// have it in the CA Secret.
	if len(caBytes) == 0 {
		caSecret, err := secretClient.Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			log.Info("Generating webhook TLS certs: CA secret not found")
			return WebhookCert{}, tlsSecret, false, nil
		}
		if err != nil {
			return WebhookCert{}, nil, false, fmt.Errorf("get %s: %w", NVCAWebhookTLSCASecretName, err)
		}
		caBytes = caSecret.Data[TLSCAName]
	}
	if len(tlsCert) == 0 || len(tlsKey) == 0 || len(caBytes) == 0 {
		log.Info("Generating webhook TLS certs: stored cert material is incomplete")
		return WebhookCert{}, tlsSecret, false, nil
	}

	servingCert, err := parseCertPEM(tlsCert)
	if err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored server cert is not parseable")
		return WebhookCert{}, tlsSecret, false, nil
	}
	caCert, err := parseCertPEM(caBytes)
	if err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored CA cert is not parseable")
		return WebhookCert{}, tlsSecret, false, nil
	}
	if !certValidAt(servingCert, now) {
		log.Info("Generating webhook TLS certs: stored server cert is outside its validity window")
		return WebhookCert{}, tlsSecret, false, nil
	}
	// The CA is written to the webhook caBundle; an out-of-window CA is rejected by
	// the API server ("x509: certificate has expired or is not yet valid"), so it
	// must be regenerated even when the serving cert still has time left.
	if !certValidAt(caCert, now) {
		log.Info("Generating webhook TLS certs: stored CA cert is outside its validity window")
		return WebhookCert{}, tlsSecret, false, nil
	}
	// Verify the serving cert chains to the stored CA, is still valid for the
	// expected service DNS name, and carries server-auth usage. A stored pair that
	// fails any of these (broken chain, stale SAN after a cluster/namespace config
	// change, or a corrupted cert) is unusable and must be regenerated rather than
	// reused, otherwise real TLS verification fails downstream.
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if err := verifyWebhookServingCert(servingCert, roots, nb, now); err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored server cert failed verification against the stored CA")
		return WebhookCert{}, tlsSecret, false, nil
	}
	// A stored cert/key pair that doesn't actually match is unusable by the webhook
	// server (tls.X509KeyPair rejects it), so verify the pairing here rather than
	// discovering it only when the server tries to load the Secret.
	if _, err := tls.X509KeyPair(tlsCert, tlsKey); err != nil {
		log.WithError(err).Info("Generating webhook TLS certs: stored server cert does not match the stored key")
		return WebhookCert{}, tlsSecret, false, nil
	}

	return WebhookCert{CACertBytes: caBytes, TLSCert: tlsCert, TLSKey: tlsKey}, tlsSecret, true, nil
}

// verifyWebhookServingCert checks servingCert the way the API server does when it
// calls the webhook: chained to roots, valid at now, for the service DNS name, with
// server-auth usage.
func verifyWebhookServingCert(servingCert *x509.Certificate, roots *x509.CertPool, nb *nvidiaiov1.NVCFBackend, now time.Time) error {
	_, err := servingCert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: now,
		DNSName:     getTLSDNSNames(nb)[0],
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

// certValidAt reports whether now falls within cert's validity window,
// treating the exact NotAfter instant as already expired.
func certValidAt(cert *x509.Certificate, now time.Time) bool {
	return !now.Before(cert.NotBefore) && now.Before(cert.NotAfter)
}
