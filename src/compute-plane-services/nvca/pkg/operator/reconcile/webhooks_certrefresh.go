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

		p, _ := pem.Decode(certSecret.Data[dataKey])
		if p == nil || p.Type != "CERTIFICATE" {
			return fmt.Errorf("failed to decode webhook TLS cert in key %q: none found", dataKey)
		}

		cert, err := x509.ParseCertificate(p.Bytes)
		if err != nil {
			return err
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
