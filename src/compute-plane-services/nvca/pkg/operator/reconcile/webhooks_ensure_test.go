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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
)

func newWebhookCertTestCache() (*BackendK8sCache, *nvidiaiov1.NVCFBackend) {
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{K8s: fake.NewSimpleClientset()},
	}
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test-backend", Namespace: "default"},
	}
	return bc, nb
}

// A no-op rollout must not rotate the webhook certificate: when the stored cert is
// still valid, ensureWebhookCert returns the exact stored material so the serving
// cert and the caBundle stay stable across reconciles. This is the regression guard
// for the "x509: certificate signed by unknown authority" drift.
func TestEnsureWebhookCert_ReusesStoredCert(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()

	stored, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)
	require.NoError(t, bc.setupWebhookSecrets(ctx, nb, stored))

	for i := 0; i < 3; i++ {
		got, err := bc.ensureWebhookCert(ctx, nb, now.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, stored.TLSCert, got.TLSCert, "serving cert should be reused unchanged")
		assert.Equal(t, stored.TLSKey, got.TLSKey, "serving key should be reused unchanged")
		assert.Equal(t, stored.CACertBytes, got.CACertBytes, "CA should be reused unchanged")
	}
}

func TestEnsureWebhookCert_GeneratesWhenSecretsMissing(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()

	got, err := bc.ensureWebhookCert(ctx, nb, time.Now().UTC())
	require.NoError(t, err)
	assert.NotEmpty(t, got.TLSCert)
	assert.NotEmpty(t, got.TLSKey)
	assert.NotEmpty(t, got.CACertBytes)
}

func TestEnsureWebhookCert_RegeneratesExpiredCert(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()

	// Mint a cert two years in the past so it is already expired (1 year validity).
	expired, err := generateWebhookCerts(nb, now.AddDate(-2, 0, 0))
	require.NoError(t, err)
	require.NoError(t, bc.setupWebhookSecrets(ctx, nb, expired))

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, expired.TLSCert, got.TLSCert, "expired cert must be replaced")

	fresh, err := parseCertPEM(got.TLSCert)
	require.NoError(t, err)
	assert.True(t, fresh.NotAfter.After(now), "replacement cert must be valid at now")
}

// The serving cert and CA are always written as a matched pair, so an inconsistent
// pair (serving cert not signed by the stored CA) is corruption that must be
// repaired rather than reused, otherwise admission keeps failing verification.
func TestEnsureWebhookCert_RegeneratesMismatchedPair(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	certA, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)
	certB, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)

	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: certA.TLSCert, TLSKeyName: certA.TLSKey},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCASecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCAName: certB.CACertBytes},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, certA.TLSCert, got.TLSCert, "mismatched pair must be regenerated")

	// The regenerated pair must be internally consistent.
	servingCert, err := parseCertPEM(got.TLSCert)
	require.NoError(t, err)
	caCert, err := parseCertPEM(got.CACertBytes)
	require.NoError(t, err)
	assert.NoError(t, servingCert.CheckSignatureFrom(caCert))
}

// A transient secret read error must surface as an error (requeue) rather than
// silently regenerating, so a read blip cannot churn the certificate.
func TestEnsureWebhookCert_PropagatesTransientReadError(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()

	bc.clients.K8s.(*fake.Clientset).PrependReactor("get", "secrets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("apiserver unavailable")
		})

	_, err := bc.ensureWebhookCert(ctx, nb, time.Now().UTC())
	require.Error(t, err)
}

// A still-valid serving certificate signed by an already-expired CA must not be
// reused: the expired CA is written to the webhook caBundle and the API server
// then rejects admission calls with "x509: certificate has expired".
func TestEnsureWebhookCert_RegeneratesExpiredCA(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	caPEM, certPEM, keyPEM := makeCertPair(t, now.AddDate(-1, 0, 0), now.AddDate(0, 0, -1), now.AddDate(0, 0, 30))

	_, err := bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: certPEM, TLSKeyName: keyPEM},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCASecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCAName: caPEM},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, certPEM, got.TLSCert, "an expired CA must trigger regeneration")
}

// A serving cert whose validity hasn't started yet (clock skew or a bad
// manually-provisioned secret) must not be reused: the API server rejects an
// admission call signed with a not-yet-valid cert the same way it rejects an
// expired one.
func TestEnsureWebhookCert_RegeneratesFutureNotBefore(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	caPEM, certPEM, keyPEM := makeCertPair(t, now.AddDate(-1, 0, 0), now.AddDate(0, 0, 1), now.AddDate(0, 0, 30))

	_, err := bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: certPEM, TLSKeyName: keyPEM},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCASecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCAName: caPEM},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, certPEM, got.TLSCert, "a not-yet-valid cert must trigger regeneration")
}

// The exact NotAfter instant is treated as already expired, matching the
// inclusive boundary x509 verification uses, so a cert reused right at that
// instant doesn't slip through and get rejected by the API server anyway.
func TestEnsureWebhookCert_RegeneratesAtExactNotAfter(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	caPEM, certPEM, keyPEM := makeCertPair(t, now.AddDate(-1, 0, 0), now.AddDate(-1, 0, 0), now)

	_, err := bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: certPEM, TLSKeyName: keyPEM},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCASecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCAName: caPEM},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, certPEM, got.TLSCert, "a cert at its exact NotAfter instant must trigger regeneration")
}

// A serving cert whose private key doesn't actually match it (e.g. a corrupted
// or manually-mismatched secret) is unusable by the webhook server, which
// rejects it in tls.X509KeyPair. Reusing it would leave the pod unable to
// serve any admission requests, so it must be regenerated.
func TestEnsureWebhookCert_RegeneratesMismatchedKey(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	stored, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)
	other, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)

	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: stored.TLSCert, TLSKeyName: other.TLSKey},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCASecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCAName: stored.CACertBytes},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.NotEqual(t, stored.TLSCert, got.TLSCert, "a cert/key mismatch must trigger regeneration")

	servingCert, err := parseCertPEM(got.TLSCert)
	require.NoError(t, err)
	caCert, err := parseCertPEM(got.CACertBytes)
	require.NoError(t, err)
	assert.NoError(t, servingCert.CheckSignatureFrom(caCert))
}

// makeCertPair builds a CA and a serving certificate signed by it, with
// independently controllable validity windows, so tests can exercise cases
// like a still-valid serving cert paired with an expired CA.
func makeCertPair(
	t *testing.T, caNotAfter, servingNotBefore, servingNotAfter time.Time,
) (caPEM, servingCertPEM, servingKeyPEM []byte) {
	t.Helper()
	caNotBefore := time.Now().UTC().AddDate(-1, 0, 0)

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "webhooks-ca"},
		NotBefore:             caNotBefore,
		NotAfter:              caNotAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	svcKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	svcTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "nvca.nvca-system.svc"},
		NotBefore:    servingNotBefore,
		NotAfter:     servingNotAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"nvca.nvca-system.svc"},
	}
	svcDER, err := x509.CreateCertificate(rand.Reader, svcTmpl, caCert, &svcKey.PublicKey, caKey)
	require.NoError(t, err)
	servingCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: svcDER})
	servingKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(svcKey)})
	return caPEM, servingCertPEM, servingKeyPEM
}
