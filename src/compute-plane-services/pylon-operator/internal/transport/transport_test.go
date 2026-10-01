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

package transport

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

const (
	testToken    = "tok-8c1f2e-very-secret"
	rotatedToken = "tok-rotated-also-secret"
	testCA       = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
)

func credential(token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: operatorNamespace},
		Data:       map[string][]byte{CredentialKey: []byte(token), "unrelated": []byte("not replicated")},
	}
}

func trustBundle(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: operatorNamespace},
		Data:       map[string]string{TrustBundleCAKey: testCA},
		BinaryData: map[string][]byte{"extra.der": {1, 2, 3}},
	}
}

// fixture runs the Reconciler against a fake client, records every write
// and captures every log line.
type fixture struct {
	t      *testing.T
	client client.WithWatch
	r      *Reconciler
	cfg    config.Config

	mu     sync.Mutex
	writes []string
	logs   strings.Builder
	// fail injects an error for an operation ("get", "create", "update",
	// "patch", "delete") on an object kind in a namespace; nil passes
	// through.
	fail func(op, kind, namespace string) error
}

func newFixture(t *testing.T, cfg config.Config, objs ...client.Object) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, pylonv1alpha1.AddToScheme(scheme))
	f := &fixture{t: t, cfg: cfg}
	f.client = fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := f.hookIn("get", obj, key.Namespace); err != nil {
					return err
				}
				return c.Get(ctx, key, obj, opts...)
			},
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if err := f.hook("create", obj); err != nil {
					return err
				}
				return c.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if err := f.hook("update", obj); err != nil {
					return err
				}
				return c.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if err := f.hook("patch", obj); err != nil {
					return err
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if err := f.hook("delete", obj); err != nil {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).
		Build()
	f.r = New(f.client, cfg)
	return f
}

func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *appsv1.Deployment:
		return "Deployment"
	case *corev1.Secret:
		return "Secret"
	case *corev1.ConfigMap:
		return "ConfigMap"
	}
	return fmt.Sprintf("%T", obj)
}

func (f *fixture) hook(op string, obj client.Object) error {
	return f.hookIn(op, obj, obj.GetNamespace())
}

func (f *fixture) hookIn(op string, obj client.Object, namespace string) error {
	kind := kindOf(obj)
	if f.fail != nil {
		if err := f.fail(op, kind, namespace); err != nil {
			return err
		}
	}
	if op != "get" {
		f.mu.Lock()
		f.writes = append(f.writes, fmt.Sprintf("%s %s %s/%s", op, kind, obj.GetNamespace(), obj.GetName()))
		f.mu.Unlock()
	}
	return nil
}

// takeWrites returns and clears the recorded writes.
func (f *fixture) takeWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}

func (f *fixture) ctx() context.Context {
	logger := funcr.New(func(prefix, args string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 10})
	return log.IntoContext(context.Background(), logger)
}

func (f *fixture) reconcile(ep *pylonv1alpha1.InferenceEndpoint) State {
	f.t.Helper()
	st, err := f.r.Reconcile(f.ctx(), ep)
	require.NoError(f.t, err)
	return st
}

func (f *fixture) deployment(ep *pylonv1alpha1.InferenceEndpoint) *appsv1.Deployment {
	f.t.Helper()
	d := &appsv1.Deployment{}
	require.NoError(f.t, f.client.Get(context.Background(), client.ObjectKey{Namespace: ep.Namespace, Name: DeploymentName(ep)}, d))
	return d
}

func (f *fixture) noDeployment(ep *pylonv1alpha1.InferenceEndpoint) {
	f.t.Helper()
	err := f.client.Get(context.Background(), client.ObjectKey{Namespace: ep.Namespace, Name: DeploymentName(ep)}, &appsv1.Deployment{})
	assert.True(f.t, apierrors.IsNotFound(err), "Deployment must not exist: %v", err)
}

// setReadyReplicas plays the Deployment controller.
func (f *fixture) setReadyReplicas(ep *pylonv1alpha1.InferenceEndpoint, n int32) {
	f.t.Helper()
	d := f.deployment(ep)
	d.Status.Replicas, d.Status.ReadyReplicas = n, n
	require.NoError(f.t, f.client.Status().Update(context.Background(), d))
}

func (f *fixture) secret(namespace string) *corev1.Secret {
	f.t.Helper()
	s := &corev1.Secret{}
	require.NoError(f.t, f.client.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: f.cfg.ClusterCredentialSecret}, s))
	return s
}

func (f *fixture) assertNoSecretMaterialLogged() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, secret := range []string{testToken, rotatedToken, "MIIB"} {
		assert.NotContains(f.t, f.logs.String(), secret, "log output")
	}
}

func setReady(ep *pylonv1alpha1.InferenceEndpoint, status metav1.ConditionStatus, reason pylonv1alpha1.ReadyReason) {
	ep.Status.Conditions = []metav1.Condition{{
		Type: string(pylonv1alpha1.ConditionReady), Status: status, Reason: string(reason), Message: "m",
	}}
}

func TestReconcileCreatesDeploymentAndReplica(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()

	st := f.reconcile(ep)
	assert.Equal(t, State{
		DeploymentName:  "pylon-llama",
		PodLabels:       map[string]string{NameLabel: "pylon", EndpointLabel: "llama"},
		DesiredReplicas: 1,
	}, st)
	assert.Equal(t, []string{
		"create Secret models/pylon-operator-cluster-credential",
		"create Deployment models/pylon-llama",
	}, f.takeWrites(), "the credential exists before the pods that mount it")

	want := Deployment(ep, testConfig(), 1)
	got := f.deployment(ep)
	assert.Equal(t, want.Labels, got.Labels)
	assert.Equal(t, want.OwnerReferences, got.OwnerReferences)
	assert.Equal(t, want.Spec, got.Spec)

	replica := f.secret(testNamespace)
	assert.Equal(t, map[string][]byte{CredentialKey: []byte(testToken)}, replica.Data, "only the cluster-token key")
	assert.Equal(t, corev1.SecretTypeOpaque, replica.Type)
	assert.Equal(t, map[string]string{"app.kubernetes.io/managed-by": "pylon-operator"}, replica.Labels)
	assert.Equal(t, []metav1.OwnerReference{{
		APIVersion: "pylon.nvidia.com/v1alpha1", Kind: "InferenceEndpoint", Name: testName, UID: testUID,
	}}, replica.OwnerReferences, "an owner, not the controller")

	source := f.secret(operatorNamespace)
	assert.Empty(t, source.Labels, "the source is never modified")
	assert.Empty(t, source.OwnerReferences)
	f.assertNoSecretMaterialLogged()
}

func TestReconcileWritesOnlyOnChange(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()
	f.reconcile(ep)
	f.takeWrites()

	// The API server adds defaults to the stored template; they must not
	// look like a change.
	d := f.deployment(ep)
	d.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
	d.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyAlways
	d.Spec.Template.Spec.Containers[0].TerminationMessagePath = "/dev/termination-log"
	d.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	d.Spec.RevisionHistoryLimit = ptr.To[int32](10)
	require.NoError(t, f.client.Update(context.Background(), d))
	f.takeWrites()
	f.setReadyReplicas(ep, 1)

	for range 3 {
		st := f.reconcile(ep)
		assert.Equal(t, int32(1), st.ReadyReplicas)
		_, negative := st.FastNegative()
		assert.False(t, negative)
	}
	assert.Empty(t, f.takeWrites(), "nothing changed, nothing written")
	assert.Equal(t, corev1.DNSClusterFirst, f.deployment(ep).Spec.Template.Spec.DNSPolicy)
}

func TestReconcileRollsOnRelevantChange(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*pylonv1alpha1.InferenceEndpoint, *config.Config)
		wantArgs []string
		wantEnv  int
	}{
		{
			name:     "service",
			mutate:   func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Service.Name = "llama-v2" },
			wantArgs: []string{"--upstream-http-base-url=http://llama-v2.models.svc.cluster.local:8000"},
		},
		{
			name:     "port",
			mutate:   func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Service.Port = 8080 },
			wantArgs: []string{"--upstream-http-base-url=http://llama-nim.models.svc.cluster.local:8080"},
		},
		{
			name:     "model name",
			mutate:   func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.ModelName = "meta/llama-3.3-70b" },
			wantArgs: []string{"--model-name=meta/llama-3.3-70b"},
		},
		{
			name:     "health path",
			mutate:   func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Health.Path = "/health" },
			wantArgs: []string{"--upstream-health-path=/health"},
		},
		{
			name: "image",
			mutate: func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) {
				c.PylonImage = "nvcr.io/nvidia/pylon:0.16.0"
			},
		},
		{
			name:     "flags",
			mutate:   func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.DevInsecureTransport = true },
			wantArgs: []string{"--quic-insecure"},
		},
		{
			name:    "trust bundle",
			mutate:  func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.TrustBundleConfigMap = "router-ca" },
			wantEnv: 2,
		},
		{
			name: "trust bundle with a TLS router",
			mutate: func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) {
				c.TrustBundleConfigMap = "router-ca"
				c.RouterGRPCAddress = "https://" + testRouter
			},
			wantEnv: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			f := newFixture(t, cfg, credential(testToken), trustBundle("router-ca"))
			ep := endpoint()
			f.reconcile(ep)
			f.setReadyReplicas(ep, 1)
			before := f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation]
			f.takeWrites()

			tt.mutate(ep, &cfg)
			f.r = New(f.client, cfg)
			st := f.reconcile(ep)

			writes := f.takeWrites()
			assert.Contains(t, writes, "patch Deployment models/pylon-llama")
			d := f.deployment(ep)
			assert.NotEqual(t, before, d.Spec.Template.Annotations[SpecHashAnnotation], "rolled")
			assert.Equal(t, Deployment(ep, cfg, 1).Spec.Template, d.Spec.Template)
			assert.Equal(t, cfg.PylonImage, d.Spec.Template.Spec.Containers[0].Image)
			for _, a := range tt.wantArgs {
				assert.Contains(t, d.Spec.Template.Spec.Containers[0].Args, a)
			}
			if tt.wantEnv != 0 {
				assert.Len(t, d.Spec.Template.Spec.Containers[0].Env, tt.wantEnv)
			}
			assert.Equal(t, int32(1), d.Status.ReadyReplicas, "status is the Deployment controller's")
			assert.Equal(t, int32(1), st.ReadyReplicas, "old pods count until they go")

			// Converged: a second pass writes nothing.
			f.reconcile(ep)
			assert.Empty(t, f.takeWrites())
		})
	}
}

// A GPU type change updates status.gpu only; the transport keeps its pods.
func TestReconcileKeepsPodsOnGPUTypeChange(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()
	f.reconcile(ep)
	f.setReadyReplicas(ep, 1)
	hash := f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation]
	f.takeWrites()

	for _, product := range []string{"NVIDIA-GB10", "NVIDIA-GB300"} {
		ep.Status.GPU = &pylonv1alpha1.GPUStatus{Product: product, Source: pylonv1alpha1.GPUSourceNodeLabels}
		f.reconcile(ep)
		assert.Empty(t, f.takeWrites(), product)
		assert.Equal(t, hash, f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation], product)
	}
}

func TestReconcileReplicasFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.TransportReplicas = 3
	f := newFixture(t, cfg, credential(testToken))
	ep := endpoint()
	st := f.reconcile(ep)
	assert.Equal(t, int32(3), *f.deployment(ep).Spec.Replicas)
	assert.Equal(t, int32(3), st.DesiredReplicas)
	verdict, ok := st.FastNegative()
	require.True(t, ok)
	assert.Equal(t, `Transport Deployment "pylon-llama" has no ready pods (0 of 3)`, verdict.Message)

	// A changed replica count is written without rolling the pods.
	hash := f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation]
	cfg.TransportReplicas = 2
	f.r = New(f.client, cfg)
	f.takeWrites()
	f.reconcile(ep)
	assert.Equal(t, []string{"patch Deployment models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, int32(2), *f.deployment(ep).Spec.Replicas)
	assert.Equal(t, hash, f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation])
}

func TestReconcileScalesToZeroOnModelNameMismatch(t *testing.T) {
	cfg := testConfig()
	cfg.TransportReplicas = 2
	f := newFixture(t, cfg, credential(testToken))
	ep := endpoint()
	setReady(ep, metav1.ConditionTrue, pylonv1alpha1.ReadyReasonHealthProbeSucceeded)
	f.reconcile(ep)
	f.setReadyReplicas(ep, 2)
	hash := f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation]

	// Mismatch: scale to zero without rolling.
	setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
	f.takeWrites()
	st := f.reconcile(ep)
	assert.Equal(t, []string{"patch Deployment models/pylon-llama"}, f.takeWrites())
	assert.True(t, st.ScaledToZero)
	assert.Equal(t, int32(0), st.DesiredReplicas)
	assert.Equal(t, int32(0), *f.deployment(ep).Spec.Replicas)
	assert.Equal(t, hash, f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation])
	verdict, ok := st.FastNegative()
	require.True(t, ok)
	assert.Equal(t, FastNegative{
		TransportReady: pylonv1alpha1.TransportReadyReasonScaledToZero,
		Registered:     pylonv1alpha1.RegisteredReasonScaledToZero,
		Message:        `Transport Deployment "pylon-llama" is scaled to zero while Ready is False with reason ModelNameMismatch`,
	}, verdict)

	// Still mismatched: nothing to write.
	f.reconcile(ep)
	assert.Empty(t, f.takeWrites())

	// Corrected (another Ready reason, or True): the count comes back.
	for _, reason := range []pylonv1alpha1.ReadyReason{pylonv1alpha1.ReadyReasonHealthProbeFailed, pylonv1alpha1.ReadyReasonHealthProbeSucceeded} {
		status := metav1.ConditionFalse
		if reason == pylonv1alpha1.ReadyReasonHealthProbeSucceeded {
			status = metav1.ConditionTrue
		}
		setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
		f.reconcile(ep)
		f.setReadyReplicas(ep, 0)
		setReady(ep, status, reason)
		st = f.reconcile(ep)
		assert.False(t, st.ScaledToZero, reason)
		assert.Equal(t, int32(2), *f.deployment(ep).Spec.Replicas, reason)
		assert.Equal(t, hash, f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation], reason)
		verdict, ok = st.FastNegative()
		require.True(t, ok, reason)
		assert.Equal(t, pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning, verdict.TransportReady, reason)
	}
}

func TestReconcileCreatesScaledToZero(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()
	setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
	st := f.reconcile(ep)
	assert.Equal(t, int32(0), *f.deployment(ep).Spec.Replicas)
	assert.True(t, st.ScaledToZero)
}

func TestReconcileMissingCredential(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		wantMsg string
	}{
		{
			name:    "no Secret",
			wantMsg: "Cluster credential Secret pylon-operator/pylon-operator-cluster-credential not found; the transport Deployment is not created or updated until it exists",
		},
		{
			name: "no cluster-token key",
			objects: []client.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: operatorNamespace},
				Data:       map[string][]byte{"token": []byte(testToken)},
			}},
			wantMsg: "Cluster credential Secret pylon-operator/pylon-operator-cluster-credential has no cluster-token key; the transport Deployment is not created or updated until it exists",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, testConfig(), tt.objects...)
			ep := endpoint()
			st := f.reconcile(ep)
			require.NotNil(t, st.Problem)
			assert.Equal(t, Problem{EventReason: EventReasonClusterCredentialMissing, Message: tt.wantMsg}, *st.Problem)
			assert.Empty(t, f.takeWrites())
			f.noDeployment(ep)
			verdict, ok := st.FastNegative()
			require.True(t, ok)
			assert.Equal(t, FastNegative{
				TransportReady: pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning,
				Registered:     pylonv1alpha1.RegisteredReasonTransportPodsNotRunning,
				Message:        tt.wantMsg,
			}, verdict)

			// The Secret arrives: the next reconcile creates everything.
			require.NoError(t, client.IgnoreNotFound(f.client.Delete(context.Background(), credential(""))))
			require.NoError(t, f.client.Create(context.Background(), credential(testToken)))
			st = f.reconcile(ep)
			assert.Nil(t, st.Problem)
			f.deployment(ep)
			f.assertNoSecretMaterialLogged()
		})
	}
}

func TestReconcileMissingCredentialKeepsExistingDeploymentButScalesToZero(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()
	f.reconcile(ep)
	f.setReadyReplicas(ep, 1)
	require.NoError(t, f.client.Delete(context.Background(), credential(testToken)))
	f.takeWrites()

	// A spec change is not applied while the credential is missing.
	ep.Spec.Health.Path = "/other"
	st := f.reconcile(ep)
	require.NotNil(t, st.Problem)
	assert.Empty(t, f.takeWrites())
	assert.Equal(t, int32(1), st.DesiredReplicas)
	assert.Equal(t, int32(1), st.ReadyReplicas)
	assert.NotContains(t, f.deployment(ep).Spec.Template.Spec.Containers[0].Args, "--upstream-health-path=/other")

	// Scaling to zero still happens.
	setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
	st = f.reconcile(ep)
	assert.Equal(t, []string{"patch Deployment models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, int32(0), *f.deployment(ep).Spec.Replicas)
	assert.Equal(t, int32(0), st.DesiredReplicas)
	verdict, ok := st.FastNegative()
	require.True(t, ok)
	assert.Equal(t, pylonv1alpha1.TransportReadyReasonScaledToZero, verdict.TransportReady)
	assert.True(t, strings.HasSuffix(verdict.Message, "; "+st.Problem.Message), verdict.Message)
}

func TestReconcileCredentialRotation(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	ep := endpoint()
	f.reconcile(ep)
	f.takeWrites()

	src := f.secret(operatorNamespace)
	src.Data[CredentialKey] = []byte(rotatedToken)
	require.NoError(t, f.client.Update(context.Background(), src))
	f.takeWrites()

	f.reconcile(ep)
	assert.Equal(t, []string{"update Secret models/pylon-operator-cluster-credential"}, f.takeWrites(), "no Deployment roll")
	assert.Equal(t, []byte(rotatedToken), f.secret(testNamespace).Data[CredentialKey])

	// A key added to the copy by hand is dropped again.
	replica := f.secret(testNamespace)
	replica.Data["stray"] = []byte("x")
	require.NoError(t, f.client.Update(context.Background(), replica))
	f.takeWrites()
	f.reconcile(ep)
	assert.Equal(t, map[string][]byte{CredentialKey: []byte(rotatedToken)}, f.secret(testNamespace).Data)
	f.assertNoSecretMaterialLogged()
}

func TestReconcileReplicaOwnedByEveryEndpoint(t *testing.T) {
	f := newFixture(t, testConfig(), credential(testToken))
	a := endpoint()
	b := endpointNamed(testNamespace, "mistral", "uid-mistral")
	f.reconcile(a)
	f.reconcile(b)
	f.reconcile(a)
	f.reconcile(b)
	refs := f.secret(testNamespace).OwnerReferences
	require.Len(t, refs, 2)
	assert.Equal(t, testUID, refs[0].UID)
	assert.Equal(t, "mistral", refs[1].Name)
	for _, ref := range refs {
		assert.Nil(t, ref.Controller)
	}
}

func TestReconcileInOperatorNamespaceMountsTheSource(t *testing.T) {
	cfg := testConfig()
	cfg.TrustBundleConfigMap = "router-ca"
	f := newFixture(t, cfg, credential(testToken), trustBundle("router-ca"))
	ep := endpointNamed(operatorNamespace, testName, testUID)
	st := f.reconcile(ep)
	assert.Nil(t, st.Problem)
	assert.Equal(t, []string{"create Deployment pylon-operator/pylon-llama"}, f.takeWrites())
	assert.Empty(t, f.secret(operatorNamespace).OwnerReferences)
}

func TestReconcileTrustBundle(t *testing.T) {
	cfg := testConfig()
	cfg.TrustBundleConfigMap = "router-ca"

	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, cfg, credential(testToken))
		ep := endpoint()
		st := f.reconcile(ep)
		require.NotNil(t, st.Problem)
		assert.Equal(t, Problem{
			EventReason: EventReasonTrustBundleMissing,
			Message:     "Trust bundle ConfigMap pylon-operator/router-ca not found; the transport Deployment is not created or updated until it exists",
		}, *st.Problem)
		f.noDeployment(ep)
	})

	t.Run("no ca.crt key", func(t *testing.T) {
		cm := trustBundle("router-ca")
		cm.Data = map[string]string{"tls.crt": "x"}
		f := newFixture(t, cfg, credential(testToken), cm)
		st := f.reconcile(endpoint())
		require.NotNil(t, st.Problem)
		assert.Equal(t, "Trust bundle ConfigMap pylon-operator/router-ca has no ca.crt key; the transport Deployment is not created or updated until it exists", st.Problem.Message)
	})

	t.Run("ca.crt as binary data", func(t *testing.T) {
		cm := trustBundle("router-ca")
		cm.Data = nil
		cm.BinaryData = map[string][]byte{TrustBundleCAKey: []byte(testCA)}
		f := newFixture(t, cfg, credential(testToken), cm)
		st := f.reconcile(endpoint())
		assert.Nil(t, st.Problem)
	})

	t.Run("replicated and updated", func(t *testing.T) {
		f := newFixture(t, cfg, credential(testToken), trustBundle("router-ca"))
		ep := endpoint()
		st := f.reconcile(ep)
		assert.Nil(t, st.Problem)
		assert.Equal(t, []string{
			"create Secret models/pylon-operator-cluster-credential",
			"create ConfigMap models/router-ca",
			"create Deployment models/pylon-llama",
		}, f.takeWrites())

		replica := &corev1.ConfigMap{}
		require.NoError(t, f.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "router-ca"}, replica))
		assert.Equal(t, map[string]string{TrustBundleCAKey: testCA}, replica.Data)
		assert.Equal(t, map[string][]byte{"extra.der": {1, 2, 3}}, replica.BinaryData)
		assert.Equal(t, config.ManagedBy, replica.Labels[config.ManagedByLabel])
		require.Len(t, replica.OwnerReferences, 1)

		f.reconcile(ep)
		assert.Empty(t, f.takeWrites())

		src := trustBundle("router-ca")
		require.NoError(t, f.client.Get(context.Background(), client.ObjectKeyFromObject(src), src))
		src.Data[TrustBundleCAKey] = "rotated"
		src.BinaryData = nil
		require.NoError(t, f.client.Update(context.Background(), src))
		f.takeWrites()
		f.reconcile(ep)
		assert.Equal(t, []string{"update ConfigMap models/router-ca"}, f.takeWrites())
		require.NoError(t, f.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "router-ca"}, replica))
		assert.Equal(t, "rotated", replica.Data[TrustBundleCAKey])
		assert.Empty(t, replica.BinaryData)
		f.assertNoSecretMaterialLogged()
	})
}

func TestReconcileConflicts(t *testing.T) {
	foreignSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: testNamespace},
		Data:       map[string][]byte{CredentialKey: []byte("users-own")},
	}
	foreignConfigMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "router-ca", Namespace: testNamespace}}
	foreignDeployment := Deployment(endpoint(), testConfig(), 1)
	foreignDeployment.OwnerReferences[0].UID = "someone-else"
	tests := []struct {
		name    string
		objects []client.Object
		wantMsg string
	}{
		{
			name:    "Secret not managed by the operator",
			objects: []client.Object{foreignSecret},
			wantMsg: "Secret models/pylon-operator-cluster-credential exists and is not managed by pylon-operator; the transport Deployment is not created or updated",
		},
		{
			name:    "ConfigMap not managed by the operator",
			objects: []client.Object{foreignConfigMap},
			wantMsg: "ConfigMap models/router-ca exists and is not managed by pylon-operator; the transport Deployment is not created or updated",
		},
		{
			name:    "Deployment controlled by something else",
			objects: []client.Object{foreignDeployment},
			wantMsg: `Deployment "pylon-llama" exists and is not controlled by this InferenceEndpoint`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.TrustBundleConfigMap = "router-ca"
			f := newFixture(t, cfg, append(tt.objects, credential(testToken), trustBundle("router-ca"))...)
			st := f.reconcile(endpoint())
			require.NotNil(t, st.Problem)
			assert.Equal(t, Problem{EventReason: EventReasonTransportObjectConflict, Message: tt.wantMsg}, *st.Problem)
			for _, w := range f.takeWrites() {
				assert.NotContains(t, w, "Deployment", "no Deployment write")
				assert.NotContains(t, w, "update", "foreign objects are left alone")
			}
			if tt.objects[0] == foreignSecret {
				assert.Equal(t, []byte("users-own"), f.secret(testNamespace).Data[CredentialKey])
			}
		})
	}

	t.Run("Deployment invisible to the cache", func(t *testing.T) {
		f := newFixture(t, testConfig(), credential(testToken))
		f.fail = func(op, kind, _ string) error {
			if op == "create" && kind == "Deployment" {
				return apierrors.NewAlreadyExists(schema.GroupResource{Group: "apps", Resource: "deployments"}, "pylon-llama")
			}
			return nil
		}
		st := f.reconcile(endpoint())
		require.NotNil(t, st.Problem)
		assert.Equal(t, `Deployment "pylon-llama" exists and is not managed by pylon-operator`, st.Problem.Message)
		assert.Equal(t, int32(0), st.DesiredReplicas)
	})
}

func TestReconcileReplacesDeploymentWithOtherSelector(t *testing.T) {
	old := Deployment(endpoint(), testConfig(), 1)
	old.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pylon-llama"}}
	old.Status.ReadyReplicas = 1
	f := newFixture(t, testConfig(), credential(testToken), old)
	ep := endpoint()
	st := f.reconcile(ep)
	assert.Equal(t, []string{
		"create Secret models/pylon-operator-cluster-credential",
		"delete Deployment models/pylon-llama",
	}, f.takeWrites())
	assert.Equal(t, int32(0), st.ReadyReplicas)
	f.noDeployment(ep)

	f.reconcile(ep)
	assert.Equal(t, []string{"create Deployment models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, PodLabels(ep), f.deployment(ep).Spec.Selector.MatchLabels)
}

func TestReconcileRestoresLabels(t *testing.T) {
	d := Deployment(endpoint(), testConfig(), 1)
	d.Labels = map[string]string{config.ManagedByLabel: config.ManagedBy, "team": "a"}
	f := newFixture(t, testConfig(), credential(testToken), d)
	ep := endpoint()
	f.reconcile(ep)
	assert.Equal(t, []string{
		"create Secret models/pylon-operator-cluster-credential",
		"patch Deployment models/pylon-llama",
	}, f.takeWrites())
	got := f.deployment(ep).Labels
	assert.Equal(t, "a", got["team"], "foreign labels stay")
	assert.Equal(t, "llama", got[EndpointLabel])

	d = Deployment(endpoint(), testConfig(), 1)
	d.Labels = nil
	f = newFixture(t, testConfig(), credential(testToken), d)
	f.reconcile(ep)
	assert.Equal(t, objectLabels(ep), f.deployment(ep).Labels)
}

func TestReconcileErrors(t *testing.T) {
	boom := fmt.Errorf("apiserver says no")
	cfg := testConfig()
	cfg.TrustBundleConfigMap = "router-ca"
	tests := []struct {
		name      string
		op        string
		kind      string
		namespace string
		setup     func(*fixture, *pylonv1alpha1.InferenceEndpoint)
		wantErr   string
	}{
		{name: "read Deployment", op: "get", kind: "Deployment", wantErr: `reading Deployment "pylon-llama": apiserver says no`},
		{name: "read replica Secret", op: "get", kind: "Secret", namespace: testNamespace, wantErr: "reading Secret models/pylon-operator-cluster-credential: apiserver says no"},
		{name: "read Secret", op: "get", kind: "Secret", wantErr: "reading cluster credential Secret pylon-operator/pylon-operator-cluster-credential: apiserver says no"},
		{name: "read ConfigMap", op: "get", kind: "ConfigMap", wantErr: "reading trust bundle ConfigMap pylon-operator/router-ca: apiserver says no"},
		{name: "create Secret", op: "create", kind: "Secret", wantErr: "creating Secret models/pylon-operator-cluster-credential: apiserver says no"},
		{name: "create ConfigMap", op: "create", kind: "ConfigMap", wantErr: "creating ConfigMap models/router-ca: apiserver says no"},
		{name: "create Deployment", op: "create", kind: "Deployment", wantErr: `creating Deployment "pylon-llama": apiserver says no`},
		{
			name: "update Secret", op: "update", kind: "Secret",
			setup: func(f *fixture, ep *pylonv1alpha1.InferenceEndpoint) {
				f.reconcile(ep)
				src := f.secret(operatorNamespace)
				src.Data[CredentialKey] = []byte(rotatedToken)
				require.NoError(f.t, f.client.Update(context.Background(), src))
			},
			wantErr: "updating Secret models/pylon-operator-cluster-credential: apiserver says no",
		},
		{
			name: "patch Deployment", op: "patch", kind: "Deployment",
			setup: func(f *fixture, ep *pylonv1alpha1.InferenceEndpoint) {
				f.reconcile(ep)
				ep.Spec.ModelName = "other"
			},
			wantErr: `updating Deployment "pylon-llama": apiserver says no`,
		},
		{
			name: "scale down while blocked", op: "patch", kind: "Deployment",
			setup: func(f *fixture, ep *pylonv1alpha1.InferenceEndpoint) {
				f.reconcile(ep)
				require.NoError(f.t, f.client.Delete(context.Background(), credential("")))
				setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
			},
			wantErr: `updating Deployment "pylon-llama": apiserver says no`,
		},
		{
			name: "delete Deployment", op: "delete", kind: "Deployment",
			setup: func(f *fixture, ep *pylonv1alpha1.InferenceEndpoint) {
				f.reconcile(ep)
				d := f.deployment(ep)
				require.NoError(f.t, f.client.Delete(context.Background(), d))
				d.ResourceVersion = ""
				d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "old"}}
				require.NoError(f.t, f.client.Create(context.Background(), d))
			},
			wantErr: `deleting Deployment "pylon-llama" to change its selector: apiserver says no`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, cfg, credential(testToken), trustBundle("router-ca"))
			ep := endpoint()
			if tt.setup != nil {
				tt.setup(f, ep)
			}
			f.fail = func(op, kind, namespace string) error {
				if op == tt.op && kind == tt.kind && (tt.namespace == "" || tt.namespace == namespace) {
					return boom
				}
				return nil
			}
			st, err := f.r.Reconcile(f.ctx(), ep)
			require.Error(t, err)
			assert.EqualError(t, err, tt.wantErr)
			assert.Equal(t, "pylon-llama", st.DeploymentName)
			for _, secret := range []string{testToken, rotatedToken} {
				assert.NotContains(t, err.Error(), secret)
			}
			f.assertNoSecretMaterialLogged()
		})
	}
}

func TestFastNegative(t *testing.T) {
	problem := &Problem{EventReason: EventReasonClusterCredentialMissing, Message: "credential missing"}
	tests := []struct {
		name  string
		state State
		want  *FastNegative
	}{
		{name: "ready pods", state: State{DeploymentName: "d", DesiredReplicas: 1, ReadyReplicas: 1}},
		{name: "some ready", state: State{DeploymentName: "d", DesiredReplicas: 3, ReadyReplicas: 1}},
		{name: "nothing wanted", state: State{DeploymentName: "d"}},
		{
			name:  "no ready pods",
			state: State{DeploymentName: "d", DesiredReplicas: 2},
			want: &FastNegative{
				TransportReady: pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning,
				Registered:     pylonv1alpha1.RegisteredReasonTransportPodsNotRunning,
				Message:        `Transport Deployment "d" has no ready pods (0 of 2)`,
			},
		},
		{
			name:  "problem wins over ready pods",
			state: State{DeploymentName: "d", DesiredReplicas: 1, ReadyReplicas: 1, Problem: problem},
			want: &FastNegative{
				TransportReady: pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning,
				Registered:     pylonv1alpha1.RegisteredReasonTransportPodsNotRunning,
				Message:        "credential missing",
			},
		},
		{
			name:  "scaled to zero comes first",
			state: State{DeploymentName: "d", ScaledToZero: true, Problem: problem},
			want: &FastNegative{
				TransportReady: pylonv1alpha1.TransportReadyReasonScaledToZero,
				Registered:     pylonv1alpha1.RegisteredReasonScaledToZero,
				Message:        `Transport Deployment "d" is scaled to zero while Ready is False with reason ModelNameMismatch; credential missing`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.state.FastNegative()
			if tt.want == nil {
				assert.False(t, ok)
				assert.Equal(t, FastNegative{}, got)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, *tt.want, got)
		})
	}
}

func TestConfigAccessor(t *testing.T) {
	cfg := testConfig()
	assert.Equal(t, cfg, New(nil, cfg).Config())
}
