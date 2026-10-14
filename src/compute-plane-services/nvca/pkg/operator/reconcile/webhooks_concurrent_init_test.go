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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	k8stesting "k8s.io/client-go/testing"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

// createGate holds back the first n creates of one Secret until all n callers have
// arrived, so every caller has read the Secret as absent before any create lands.
// The fake clientset serializes reactors under a single lock, so the gate has to sit
// in front of the client instead.
type createGate struct {
	secretName string
	n          int
	mu         sync.Mutex
	arrived    int
	release    chan struct{}
}

func newCreateGate(secretName string, n int) *createGate {
	return &createGate{secretName: secretName, n: n, release: make(chan struct{})}
}

func (g *createGate) wait(name string) {
	if name != g.secretName {
		return
	}
	g.mu.Lock()
	g.arrived++
	gated := g.arrived <= g.n
	if g.arrived == g.n {
		close(g.release)
	}
	g.mu.Unlock()
	if gated {
		select {
		case <-g.release:
		case <-time.After(30 * time.Second):
		}
	}
}

type gatedClientset struct {
	kubernetes.Interface
	gate *createGate
}

func (c gatedClientset) CoreV1() corev1client.CoreV1Interface {
	return gatedCoreV1{CoreV1Interface: c.Interface.CoreV1(), gate: c.gate}
}

type gatedCoreV1 struct {
	corev1client.CoreV1Interface
	gate *createGate
}

func (c gatedCoreV1) Secrets(namespace string) corev1client.SecretInterface {
	return gatedSecrets{SecretInterface: c.CoreV1Interface.Secrets(namespace), gate: c.gate}
}

type gatedSecrets struct {
	corev1client.SecretInterface
	gate *createGate
}

func (s gatedSecrets) Create(ctx context.Context, sec *v1.Secret, opts metav1.CreateOptions) (*v1.Secret, error) {
	s.gate.wait(sec.Name)
	return s.SecretInterface.Create(ctx, sec, opts)
}

// Two cold-start reconciles of the same backend that both read the certificate as
// absent must converge on one CA: the stored serving cert has to verify against the
// CA Secret and every published caBundle. A reconcile that loses a create race may
// still fail with AlreadyExists, but the other one must complete.
func TestConcurrentColdStartWebhookCertsConverge(t *testing.T) {
	const runs = 10

	for run := 0; run < runs; run++ {
		ctx := newTestContext()
		_, nb := newWebhookCertTestCache()
		bc := &BackendK8sCache{clients: &kubeclients.KubeClients{K8s: gatedClientset{
			Interface: fake.NewSimpleClientset(),
			gate:      newCreateGate(NVCAWebhookTLSCertSecretName, 2),
		}}}
		now := time.Now().UTC()

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = publishWebhookCert(ctx, bc, nb, now)
			}()
		}
		wg.Wait()

		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
				continue
			}
			assert.Contains(t, err.Error(), "already exists", "run %d", run)
		}
		require.Positive(t, succeeded, "run %d: no reconcile completed: %v", run, errs)
		require.NoError(t, webhookCAConsistency(ctx, bc, nb, now), "run %d", run)
	}
}

// publishWebhookCert runs the certificate part of setupNVCAAgentInfra.
func publishWebhookCert(ctx context.Context, bc *BackendK8sCache, nb *nvidiaiov1.NVCFBackend, now time.Time) error {
	cert, err := bc.ensureWebhookCert(ctx, nb, now)
	if err != nil {
		return err
	}
	if err := bc.setupWebhookSecrets(ctx, nb, cert); err != nil {
		return err
	}
	if err := bc.setupMiniServiceValidatingWebhook(ctx, nb, cert); err != nil {
		return err
	}
	return bc.setupNVCAMutatingWebhookConfiguration(ctx, nb, cert)
}

// webhookCAConsistency returns an error unless the stored serving cert verifies
// against the CA Secret and every caBundle published for the NVCA webhooks.
func webhookCAConsistency(ctx context.Context, bc *BackendK8sCache, nb *nvidiaiov1.NVCFBackend, now time.Time) error {
	ns := getSystemNamespace(nb)
	k8s := bc.clients.K8s

	tlsSecret, err := k8s.CoreV1().Secrets(ns).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	servingCert, err := parseCertPEM(tlsSecret.Data[TLSCertName])
	if err != nil {
		return err
	}
	caSecret, err := k8s.CoreV1().Secrets(ns).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	bundles := map[string][]byte{"secret " + NVCAWebhookTLSCASecretName: caSecret.Data[TLSCAName]}
	vwc, err := k8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	for _, w := range vwc.Webhooks {
		bundles["validating webhook "+w.Name] = w.ClientConfig.CABundle
	}
	mwc, err := k8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	for _, w := range mwc.Webhooks {
		bundles["mutating webhook "+w.Name] = w.ClientConfig.CABundle
	}

	var problems []string
	for source, pemBytes := range bundles {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pemBytes) {
			problems = append(problems, source+": no parseable CA")
			continue
		}
		if err := verifyWebhookServingCert(servingCert, roots, nb, now); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", source, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("serving cert does not verify: %s", strings.Join(problems, "; "))
	}
	return nil
}

// When another writer replaces the stored certificate between this reconcile's read
// and its write, the preconditioned update conflicts and the reconcile must adopt the
// other writer's certificate rather than overwrite it.
func TestEnsureWebhookCert_AdoptsCertOnUpdateConflict(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()

	expired, err := generateWebhookCerts(nb, now.AddDate(-2, 0, 0))
	require.NoError(t, err)
	require.NoError(t, bc.setupWebhookSecrets(ctx, nb, expired))
	winner, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)

	cs := bc.clients.K8s.(*fake.Clientset)
	var conflicted atomic.Bool
	cs.PrependReactor("update", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sec := a.(k8stesting.UpdateAction).GetObject().(*v1.Secret)
		if sec.Name != NVCAWebhookTLSCertSecretName || conflicted.Swap(true) {
			return false, nil, nil
		}
		require.NoError(t, cs.Tracker().Update(v1.SchemeGroupVersion.WithResource("secrets"),
			newTLSCertSecret(nb, winner), sec.Namespace))
		return true, nil, k8serrors.NewConflict(schema.GroupResource{Resource: "secrets"}, sec.Name,
			fmt.Errorf("the object has been modified"))
	})

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.True(t, conflicted.Load())
	assert.Equal(t, winner, got)
}

// A newly minted certificate is stored together with its CA in the serving cert
// Secret, so the pair can never be written half by one reconcile and half by another.
func TestEnsureWebhookCert_StoresCAWithServingCert(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)

	tlsSecret, err := bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).
		Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, got.TLSCert, tlsSecret.Data[TLSCertName])
	assert.Equal(t, got.TLSKey, tlsSecret.Data[TLSKeyName])
	assert.Equal(t, got.CACertBytes, tlsSecret.Data[TLSCAName])
}

// Secrets written before the CA was stored alongside the serving cert carry it only
// in the CA Secret; they must still be reused, and are upgraded on the next publish.
func TestEnsureWebhookCert_ReusesLegacySecretsWithoutCAKey(t *testing.T) {
	ctx := newTestContext()
	bc, nb := newWebhookCertTestCache()
	now := time.Now().UTC()
	ns := getSystemNamespace(nb)

	stored, err := generateWebhookCerts(nb, now)
	require.NoError(t, err)
	_, err = bc.clients.K8s.CoreV1().Secrets(ns).Create(ctx, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: NVCAWebhookTLSCertSecretName, Namespace: ns},
		Data:       map[string][]byte{TLSCertName: stored.TLSCert, TLSKeyName: stored.TLSKey},
		Type:       v1.SecretTypeTLS,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, bc.setupTLSCASecret(ctx, nb, stored))

	got, err := bc.ensureWebhookCert(ctx, nb, now)
	require.NoError(t, err)
	assert.Equal(t, stored, got)

	require.NoError(t, publishWebhookCert(ctx, bc, nb, now))
	tlsSecret, err := bc.clients.K8s.CoreV1().Secrets(ns).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, stored.CACertBytes, tlsSecret.Data[TLSCAName])
	require.NoError(t, webhookCAConsistency(ctx, bc, nb, now))
}
