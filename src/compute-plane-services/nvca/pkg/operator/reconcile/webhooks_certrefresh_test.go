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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcabeinformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
)

func Test_runTLSCertRotate(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	nowMu := &sync.Mutex{}
	now := time.Now().UTC()
	bc := &BackendK8sCache{
		systemNamespace:   DefaultNVCASystemNamespace,
		operatorNamespace: NVCAOperatorNamespace,
		clients:           mockKubeClients(),
		now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
	}

	infFac := nvcabeinformers.NewSharedInformerFactoryWithOptions(
		bc.clients.NVCAOP,
		ResyncInterval,
		nvcabeinformers.WithNamespace(bc.operatorNamespace),
	)

	bc.nvcfBackendLister = infFac.Nvcf().V1().NVCFBackends().Lister()

	go bc.runTLSCertRotate(ctx, 50*time.Millisecond)

	// Wait for 75 millis to trigger an error on informer not ready.
	time.Sleep(75 * time.Millisecond)

	infFac.Start(ctx.Done())
	// Wait another 75 millis to trigger an error on no backend found.
	time.Sleep(75 * time.Millisecond)

	// Create the backend but not cert secrets.
	nb := &nvidiaiov1.NVCFBackend{}
	nb.Name, nb.Namespace = "mycluster", bc.operatorNamespace
	_, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		nbs, err := bc.nvcfBackendLister.List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, nbs, 1)
		}
	}, 5*time.Second, 100*time.Millisecond)

	// Wait another 75 millis to trigger an error on no cert secrets found.
	time.Sleep(75 * time.Millisecond)

	webhookCert, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)
	err = bc.setupWebhookSecrets(ctx, nb, webhookCert)
	require.NoError(t, err)

	// Wait another 75 millis to allow one full loop to determine no updates needed.
	time.Sleep(75 * time.Millisecond)

	fmtTime := func(t time.Time) string { return t.Format(time.RFC3339) }

	secretClient := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb))

	// Store the original certificate serial numbers for comparison
	var originalSerialNumbers []string

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		for secretName, dataKey := range map[string]string{
			NVCAWebhookTLSCertSecretName: TLSCertName,
			NVCAWebhookTLSCASecretName:   TLSCAName,
		} {
			certSecret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
			if !assert.NoError(ct, err) {
				return
			}

			p, _ := pem.Decode(certSecret.Data[dataKey])
			if !assert.NotNil(ct, p) {
				return
			}

			cert, err := x509.ParseCertificate(p.Bytes)
			if !assert.NoError(ct, err) {
				return
			}

			assert.Equal(ct, fmtTime(now), fmtTime(cert.NotBefore))

			// Store serial numbers for later comparison
			if len(originalSerialNumbers) < 2 {
				originalSerialNumbers = append(originalSerialNumbers, cert.SerialNumber.String())
			}
		}
	}, 10*time.Second, 100*time.Millisecond)

	// Update nowFunc to return a 50 weeks and a second in the future to trigger an update.
	nowMu.Lock()
	now = now.AddDate(1, 0, -14).Add(1 * time.Second)
	nowMu.Unlock()

	// Wait for multiple rotation cycles to ensure the rotation loop detects the time change
	// The rotation loop runs every 50ms, so waiting 200ms gives it 4 cycles to detect and act
	time.Sleep(200 * time.Millisecond)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		newSerialNumbers := []string{}
		for secretName, dataKey := range map[string]string{
			NVCAWebhookTLSCertSecretName: TLSCertName,
			NVCAWebhookTLSCASecretName:   TLSCAName,
		} {
			certSecret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
			if !assert.NoError(ct, err) {
				return
			}

			p, _ := pem.Decode(certSecret.Data[dataKey])
			if !assert.NotNil(ct, p) {
				return
			}

			cert, err := x509.ParseCertificate(p.Bytes)
			if !assert.NoError(ct, err) {
				return
			}

			assert.Equal(ct, fmtTime(now), fmtTime(cert.NotBefore))
			newSerialNumbers = append(newSerialNumbers, cert.SerialNumber.String())
		}

		// Verify that the certificates were actually rotated by checking serial numbers changed
		if len(newSerialNumbers) == 2 && len(originalSerialNumbers) == 2 {
			assert.NotEqual(ct, originalSerialNumbers[0], newSerialNumbers[0], "TLS cert should have been rotated")
			assert.NotEqual(ct, originalSerialNumbers[1], newSerialNumbers[1], "CA cert should have been rotated")
		}
	}, 15*time.Second, 100*time.Millisecond)

	cancel()
}
