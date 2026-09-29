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

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/prober"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

const (
	testNamespace     = "models"
	testName          = "llama"
	testUID           = types.UID("uid-llama")
	testService       = "llama-nim"
	testModel         = "meta/llama-3.1-8b-instruct"
	testHealthPath    = "/v1/health/ready"
	testGPU           = "NVIDIA-GB10"
	testToken         = "tok-controller-secret"
	operatorNamespace = "pylon-operator"
	probeInterval     = 10 * time.Second
)

func testConfig() config.Config {
	return config.Config{
		ClusterID:               "spark-berlin",
		RouterGRPCAddress:       "router.gateway.svc:50071",
		PylonImage:              "nvcr.io/nvidia/pylon:0.15.2",
		TransportReplicas:       1,
		ProbeInterval:           probeInterval,
		ClusterCredentialSecret: config.DefaultClusterCredentialSecret,
		OperatorNamespace:       operatorNamespace,
		InitialInputTPS:         config.DefaultInitialInputTPS,
	}
}

var testKey = types.NamespacedName{Namespace: testNamespace, Name: testName}

// fakeBackend answers the health path with a configurable status and
// /v1/models with a configurable body.
type fakeBackend struct {
	*httptest.Server
	health atomic.Int32
	models atomic.Pointer[string]
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	b := &fakeBackend{}
	b.health.Store(http.StatusOK)
	b.setModels(testModel)
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testHealthPath:
			w.WriteHeader(int(b.health.Load()))
		case prober.ModelsPath:
			_, _ = w.Write([]byte(*b.models.Load()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *fakeBackend) setModels(ids ...string) {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, fmt.Sprintf(`{"id":%q,"object":"model"}`, id))
	}
	body := `{"object":"list","data":[` + strings.Join(quoted, ",") + `]}`
	b.models.Store(&body)
}

// fakePylon serves one Pylon exposition for every transport pod.
type fakePylon struct {
	*httptest.Server
	body atomic.Pointer[string]
}

func newFakePylon(t *testing.T) *fakePylon {
	t.Helper()
	p := &fakePylon{}
	p.set("")
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*p.body.Load()))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePylon) set(body string) { p.body.Store(&body) }

type fixture struct {
	t          *testing.T
	client     client.Client
	recorder   *record.FakeRecorder
	metrics    *metrics.Metrics
	registry   *prometheus.Registry
	backend    *fakeBackend
	pylon      *fakePylon
	observer   *registration.Observer
	reconciler *Reconciler
}

type fixtureOption func(*fixtureOptions)

type fixtureOptions struct {
	objects           []client.Object
	interceptors      *interceptor.Funcs
	steps             func(client.Client, *prober.Prober) []Step
	config            func(*config.Config)
	withoutCredential bool
}

func withConfig(mutate func(*config.Config)) fixtureOption {
	return func(o *fixtureOptions) { o.config = mutate }
}

// withoutCredential leaves the cluster credential Secret out of the
// operator namespace.
func withoutCredential() fixtureOption {
	return func(o *fixtureOptions) { o.withoutCredential = true }
}

func withObjects(objs ...client.Object) fixtureOption {
	return func(o *fixtureOptions) { o.objects = append(o.objects, objs...) }
}

func withInterceptors(f interceptor.Funcs) fixtureOption {
	return func(o *fixtureOptions) { o.interceptors = &f }
}

func withSteps(steps ...Step) fixtureOption {
	return func(o *fixtureOptions) {
		o.steps = func(client.Client, *prober.Prober) []Step { return steps }
	}
}

func newFixture(t *testing.T, opts ...fixtureOption) *fixture {
	t.Helper()
	var o fixtureOptions
	for _, opt := range opts {
		opt(&o)
	}
	cfg := testConfig()
	if o.config != nil {
		o.config(&cfg)
	}
	if !o.withoutCredential {
		o.objects = append(o.objects, credential())
	}
	scheme, err := NewScheme()
	require.NoError(t, err)
	b := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&pylonv1alpha1.InferenceEndpoint{}).
		WithIndex(&pylonv1alpha1.InferenceEndpoint{}, ServiceNameIndex, IndexServiceName).
		WithIndex(&discoveryv1.EndpointSlice{}, EndpointSliceNodeIndex, IndexEndpointSliceNodes).
		WithObjects(o.objects...)
	if o.interceptors != nil {
		b = b.WithInterceptorFuncs(*o.interceptors)
	}
	c := b.Build()

	f := &fixture{
		t:        t,
		client:   c,
		recorder: record.NewFakeRecorder(100),
		metrics:  metrics.New(),
		registry: prometheus.NewRegistry(),
		backend:  newFakeBackend(t),
		pylon:    newFakePylon(t),
	}
	require.NoError(t, f.metrics.Register(f.registry))
	p := prober.New(f.metrics)
	p.BaseURL = func(*pylonv1alpha1.InferenceEndpoint) string { return f.backend.URL }
	f.observer = registration.New(c, f.metrics)
	f.observer.MetricsURL = func(*corev1.Pod) string { return f.pylon.URL + registration.MetricsPath }
	steps := DefaultSteps(c, p, &gpu.Resolver{Reader: c}, transport.New(c, cfg), f.observer)
	if o.steps != nil {
		steps = o.steps(c, p)
	}
	f.reconciler = NewReconciler(Options{
		Client:   c,
		Recorder: f.recorder,
		Config:   cfg,
		Metrics:  f.metrics,
		Steps:    steps,
	})
	return f
}

func (f *fixture) reconcile() (reconcile.Result, error) {
	f.t.Helper()
	return f.reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: testKey})
}

func (f *fixture) mustReconcile() reconcile.Result {
	f.t.Helper()
	res, err := f.reconcile()
	require.NoError(f.t, err)
	return res
}

func (f *fixture) endpoint() *pylonv1alpha1.InferenceEndpoint {
	f.t.Helper()
	ep := &pylonv1alpha1.InferenceEndpoint{}
	require.NoError(f.t, f.client.Get(context.Background(), testKey, ep))
	return ep
}

func (f *fixture) condition(t pylonv1alpha1.ConditionType) metav1.Condition {
	f.t.Helper()
	for _, c := range f.endpoint().Status.Conditions {
		if c.Type == string(t) {
			return c
		}
	}
	f.t.Fatalf("condition %s not set", t)
	return metav1.Condition{}
}

// events drains the recorder.
func (f *fixture) events() []string {
	var out []string
	for {
		select {
		case e := <-f.recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// endpointsGauge returns nvcf_pylon_operator_endpoints{condition,status}.
func (f *fixture) endpointsGauge(condition pylonv1alpha1.ConditionType, status metav1.ConditionStatus) float64 {
	f.t.Helper()
	families, err := f.registry.Gather()
	require.NoError(f.t, err)
	for _, fam := range families {
		if fam.GetName() != "nvcf_pylon_operator_endpoints" {
			continue
		}
		for _, m := range fam.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["condition"] == string(condition) && labels["status"] == string(status) {
				return m.GetGauge().GetValue()
			}
		}
	}
	f.t.Fatalf("no endpoints series for %s=%s", condition, status)
	return 0
}

func inferenceEndpoint(generation int64) *pylonv1alpha1.InferenceEndpoint {
	return &pylonv1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace, Generation: generation, UID: testUID},
		Spec: pylonv1alpha1.InferenceEndpointSpec{
			ModelName:          testModel,
			InferenceAPIFormat: pylonv1alpha1.InferenceAPIFormat{Type: pylonv1alpha1.InferenceAPIFormatChat},
			Service:            pylonv1alpha1.ServiceReference{Name: testService, Port: 8000},
			Health:             pylonv1alpha1.HealthCheck{Path: testHealthPath},
		},
	}
}

func service(namespace, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 8000}}},
	}
}

func endpointSlice(namespace, name, svc, nodeName string, ready bool) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Namespace: namespace},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To[int32](8000)}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)},
			NodeName:   ptr.To(nodeName),
		}},
	}
	if svc != "" {
		s.Labels = map[string]string{discoveryv1.LabelServiceName: svc}
	}
	return s
}

func node(name, product string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{gpu.ProductLabel: product}}}
}

// credential is the cluster credential Secret in the operator namespace.
func credential() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: operatorNamespace},
		Data:       map[string][]byte{transport.CredentialKey: []byte(testToken)},
	}
}

// readyTransport is the endpoint's transport Deployment with one ready pod,
// so TransportReady and Registered are left to the registration observer.
func readyTransport() *appsv1.Deployment {
	d := transport.Deployment(inferenceEndpoint(1), testConfig(), 1)
	d.Status.Replicas, d.Status.ReadyReplicas = 1, 1
	return d
}

func healthyObjects() []client.Object {
	return []client.Object{
		inferenceEndpoint(1),
		service(testNamespace, testService),
		endpointSlice(testNamespace, "llama-nim-abc", testService, "spark-1", true),
		node("spark-1", testGPU),
		readyTransport(),
	}
}

func TestReconcileReadyReasons(t *testing.T) {
	tests := []struct {
		name        string
		objects     []client.Object
		health      int
		models      []string
		wantStatus  metav1.ConditionStatus
		wantReason  pylonv1alpha1.ReadyReason
		wantMessage string
		wantGPU     pylonv1alpha1.GPUStatus
		wantEvents  []string
		// wantScaledToZero expects the transport's fast negative instead of
		// the registration observer's verdict.
		wantScaledToZero bool
	}{
		{
			name:        "service not found",
			objects:     []client.Object{inferenceEndpoint(1), readyTransport()},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonServiceNotFound,
			wantMessage: `Service "llama-nim" not found in namespace "models"`,
			wantGPU:     pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown},
			wantEvents: []string{
				`Warning ServiceNotFound Ready is False: Service "llama-nim" not found in namespace "models"`,
				"Warning WaitingForUpstream TransportReady is False: Ready is False, so Pylon idles",
				"Warning WaitingForUpstream Registered is False: Ready is False, so Pylon idles",
			},
		},
		{
			name: "no ready endpoints, GPU still derived from the scheduled pod",
			objects: []client.Object{
				inferenceEndpoint(1),
				service(testNamespace, testService),
				endpointSlice(testNamespace, "llama-nim-abc", testService, "spark-1", false),
				node("spark-1", testGPU),
				readyTransport(),
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonNoReadyEndpoints,
			wantMessage: `Service "llama-nim" has no ready endpoints for port 8000`,
			wantGPU:     pylonv1alpha1.GPUStatus{Product: testGPU, Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{testGPU}},
			wantEvents: []string{
				`Warning NoReadyEndpoints Ready is False: Service "llama-nim" has no ready endpoints for port 8000`,
				"Warning WaitingForUpstream TransportReady is False",
				"Warning WaitingForUpstream Registered is False",
				`Normal GPUProductChanged Effective GPU product changed from "" to "NVIDIA-GB10" (source NodeLabels)`,
			},
		},
		{
			name:        "health probe failed",
			objects:     healthyObjects(),
			health:      http.StatusServiceUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeFailed,
			wantMessage: testHealthPath + " returned HTTP 503",
			wantGPU:     pylonv1alpha1.GPUStatus{Product: testGPU, Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{testGPU}},
		},
		{
			name:             "model name mismatch",
			objects:          healthyObjects(),
			models:           []string{"mistral/mistral-7b"},
			wantStatus:       metav1.ConditionFalse,
			wantReason:       pylonv1alpha1.ReadyReasonModelNameMismatch,
			wantMessage:      `backend does not serve model "meta/llama-3.1-8b-instruct"; GET /v1/models lists "mistral/mistral-7b"`,
			wantGPU:          pylonv1alpha1.GPUStatus{Product: testGPU, Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{testGPU}},
			wantScaledToZero: true,
		},
		{
			name:        "health probe succeeded",
			objects:     healthyObjects(),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeSucceeded,
			wantMessage: testHealthPath + " returned HTTP 200",
			wantGPU:     pylonv1alpha1.GPUStatus{Product: testGPU, Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{testGPU}},
			wantEvents: []string{
				"Normal HealthProbeSucceeded Ready is True: GET ",
				"Warning TunnelNotConnected TransportReady is False: No transport pod has a reverse tunnel connected",
				"Warning Pending Registered is False: Transport pods are starting and opening registration streams",
				`Normal GPUProductChanged Effective GPU product changed from "" to "NVIDIA-GB10" (source NodeLabels)`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, withObjects(tt.objects...))
			if tt.health != 0 {
				f.backend.health.Store(int32(tt.health))
			}
			if tt.models != nil {
				f.backend.setModels(tt.models...)
			}

			res := f.mustReconcile()
			assert.Equal(t, reconcile.Result{RequeueAfter: probeInterval}, res)

			ep := f.endpoint()
			assert.Equal(t, int64(1), ep.Status.ObservedGeneration)
			ready := f.condition(pylonv1alpha1.ConditionReady)
			assert.Equal(t, tt.wantStatus, ready.Status)
			assert.Equal(t, string(tt.wantReason), ready.Reason)
			assert.Contains(t, ready.Message, tt.wantMessage)
			assert.Equal(t, int64(1), ready.ObservedGeneration)
			require.NotNil(t, ep.Status.GPU)
			assert.Equal(t, tt.wantGPU, *ep.Status.GPU)

			// No transport pod exists, so the observer sees no stream.
			wantTransport, wantRegistered := "WaitingForUpstream", "WaitingForUpstream"
			switch {
			case tt.wantScaledToZero:
				wantTransport, wantRegistered = "ScaledToZero", "ScaledToZero"
			case tt.wantStatus == metav1.ConditionTrue:
				wantTransport, wantRegistered = "TunnelNotConnected", "Pending"
			}
			for ct, reason := range map[pylonv1alpha1.ConditionType]string{
				pylonv1alpha1.ConditionTransportReady: wantTransport,
				pylonv1alpha1.ConditionRegistered:     wantRegistered,
			} {
				c := f.condition(ct)
				assert.Equal(t, metav1.ConditionFalse, c.Status, ct)
				assert.Equal(t, reason, c.Reason, ct)
			}
			assert.Equal(t, &pylonv1alpha1.RegistrationStatus{ClusterID: testConfig().ClusterID}, ep.Status.Registration)
			assert.Empty(t, ep.Status.Servers)

			events := f.events()
			if tt.wantEvents != nil {
				require.Len(t, events, len(tt.wantEvents), events)
				for i, want := range tt.wantEvents {
					assert.True(t, strings.HasPrefix(events[i], want), "event %q does not start with %q", events[i], want)
				}
			}
			for _, e := range events {
				assert.NotContains(t, e, "Unknown", "no Events for Unknown conditions")
			}

			assert.Equal(t, 1.0, f.endpointsGauge(pylonv1alpha1.ConditionReady, tt.wantStatus))
			assert.Equal(t, 1.0, f.endpointsGauge(pylonv1alpha1.ConditionRegistered, metav1.ConditionFalse))
		})
	}
}

func TestReconcileEmitsEventsOnTransitionsOnly(t *testing.T) {
	var patches atomic.Int32
	f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patches.Add(1)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}))
	f.backend.health.Store(http.StatusServiceUnavailable)
	f.mustReconcile()
	events := f.events()
	require.Len(t, events, 4)
	assert.True(t, strings.HasPrefix(events[0], "Warning HealthProbeFailed Ready is False"), events[0])
	assert.True(t, strings.HasPrefix(events[1], "Warning WaitingForUpstream TransportReady is False"), events[1])
	assert.True(t, strings.HasPrefix(events[2], "Warning WaitingForUpstream Registered is False"), events[2])
	assert.Equal(t, int32(1), patches.Load())

	// Nothing changed: no patch and no Events.
	f.mustReconcile()
	assert.Empty(t, f.events())
	assert.Equal(t, int32(1), patches.Load())

	// Backend recovers: one Normal Event, and the observer moves on to the
	// connectivity reasons.
	f.backend.health.Store(http.StatusOK)
	f.mustReconcile()
	events = f.events()
	require.Len(t, events, 3)
	assert.True(t, strings.HasPrefix(events[0], "Normal HealthProbeSucceeded Ready is True"), events[0])
	assert.True(t, strings.HasPrefix(events[1], "Warning TunnelNotConnected TransportReady is False"), events[1])
	assert.True(t, strings.HasPrefix(events[2], "Warning Pending Registered is False"), events[2])
	assert.Equal(t, int32(2), patches.Load())
	assert.Equal(t, 1.0, f.endpointsGauge(pylonv1alpha1.ConditionReady, metav1.ConditionTrue))
	assert.Equal(t, 0.0, f.endpointsGauge(pylonv1alpha1.ConditionReady, metav1.ConditionFalse))

	// A change of reason while False is a transition too. The mismatch
	// scales the transport to zero, which the transport conditions report;
	// the next Ready reason restores it and they return to the observer.
	f.backend.setModels("other")
	f.mustReconcile()
	f.backend.health.Store(http.StatusInternalServerError)
	f.mustReconcile()
	events = f.events()
	require.Len(t, events, 6)
	assert.True(t, strings.HasPrefix(events[0], "Warning ModelNameMismatch"), events[0])
	assert.True(t, strings.HasPrefix(events[1], "Warning ScaledToZero TransportReady is False"), events[1])
	assert.True(t, strings.HasPrefix(events[2], "Warning ScaledToZero Registered is False"), events[2])
	assert.True(t, strings.HasPrefix(events[3], "Warning HealthProbeFailed"), events[3])
	assert.True(t, strings.HasPrefix(events[4], "Warning WaitingForUpstream TransportReady is False"), events[4])
	assert.True(t, strings.HasPrefix(events[5], "Warning WaitingForUpstream Registered is False"), events[5])
}

func TestReconcileUpdatesObservedGeneration(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...))
	f.mustReconcile()
	assert.Equal(t, int64(1), f.endpoint().Status.ObservedGeneration)

	ep := f.endpoint()
	ep.Spec.Health.Path = "/health"
	ep.Generation = 2
	require.NoError(t, f.client.Update(context.Background(), ep))
	stored := f.endpoint().Generation
	require.NotEqual(t, int64(1), stored)

	f.mustReconcile()
	ep = f.endpoint()
	assert.Equal(t, stored, ep.Status.ObservedGeneration)
	for _, c := range ep.Status.Conditions {
		assert.Equal(t, stored, c.ObservedGeneration, c.Type)
	}
	// The new path is not served by the backend.
	assert.Equal(t, string(pylonv1alpha1.ReadyReasonHealthProbeFailed), f.condition(pylonv1alpha1.ConditionReady).Reason)
}

func TestReconcilePreservesOtherConditions(t *testing.T) {
	ep := inferenceEndpoint(1)
	ep.Status.Conditions = []metav1.Condition{{
		Type:               "Degraded",
		Status:             metav1.ConditionFalse,
		Reason:             "AsExpected",
		Message:            "written by another controller",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second)),
	}}
	objs := healthyObjects()
	objs[0] = ep
	f := newFixture(t, withObjects(objs...))
	f.mustReconcile()
	got := f.condition("Degraded")
	assert.Equal(t, "written by another controller", got.Message)
	assert.Len(t, f.endpoint().Status.Conditions, 4)
}

func TestReconcileSpecGPUWins(t *testing.T) {
	objs := healthyObjects()
	ep := inferenceEndpoint(1)
	ep.Spec.GPU = &pylonv1alpha1.GPUSpec{Product: "NVIDIA-H100"}
	objs[0] = ep
	f := newFixture(t, withObjects(objs...))
	f.mustReconcile()
	assert.Equal(t, &pylonv1alpha1.GPUStatus{
		Product:      "NVIDIA-H100",
		Source:       pylonv1alpha1.GPUSourceSpec,
		NodeProducts: []string{testGPU},
	}, f.endpoint().Status.GPU)
}

func TestReconcileGPUErrorKeepsPreviousValue(t *testing.T) {
	objs := healthyObjects()
	ep := inferenceEndpoint(1)
	previous := &pylonv1alpha1.GPUStatus{Product: "NVIDIA-A100", Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{"NVIDIA-A100"}}
	ep.Status.GPU = previous.DeepCopy()
	objs[0] = ep
	f := newFixture(t, withObjects(objs...), withInterceptors(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*metav1.PartialObjectMetadata); ok {
				return fmt.Errorf("node cache unavailable")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}))
	f.mustReconcile()
	assert.Equal(t, previous, f.endpoint().Status.GPU)
	assert.Equal(t, metav1.ConditionTrue, f.condition(pylonv1alpha1.ConditionReady).Status)
	for _, e := range f.events() {
		assert.NotContains(t, e, EventReasonGPUProductChanged)
	}
}

func TestReconcileNotFoundForgetsMetrics(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...))
	f.mustReconcile()
	assert.Equal(t, 1.0, f.endpointsGauge(pylonv1alpha1.ConditionReady, metav1.ConditionTrue))

	require.NoError(t, f.client.Delete(context.Background(), f.endpoint()))
	res := f.mustReconcile()
	assert.Equal(t, reconcile.Result{}, res)
	assert.Equal(t, 0.0, f.endpointsGauge(pylonv1alpha1.ConditionReady, metav1.ConditionTrue))
}

func TestReconcileGetError(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*pylonv1alpha1.InferenceEndpoint); ok {
				return fmt.Errorf("apiserver unavailable")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}))
	_, err := f.reconcile()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading InferenceEndpoint: apiserver unavailable")
}

func TestReconcileSkipsDeletingEndpoint(t *testing.T) {
	ep := inferenceEndpoint(1)
	ep.Finalizers = []string{"example.com/keep"}
	ep.DeletionTimestamp = ptr.To(metav1.Now())
	f := newFixture(t, withObjects(ep))
	assert.Equal(t, reconcile.Result{}, f.mustReconcile())
	assert.Empty(t, f.endpoint().Status.Conditions)
}

func TestReconcileBackendStepErrors(t *testing.T) {
	tests := []struct {
		name    string
		funcs   interceptor.Funcs
		wantErr string
	}{
		{
			name: "service read fails",
			funcs: interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Service); ok {
					return fmt.Errorf("timeout")
				}
				return c.Get(ctx, key, obj, opts...)
			}},
			wantErr: `step backend: reading Service "llama-nim": timeout`,
		},
		{
			name: "endpointslice list fails",
			funcs: interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return fmt.Errorf("timeout")
			}},
			wantErr: `step backend: listing EndpointSlices of Service "llama-nim": timeout`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(tt.funcs))
			_, err := f.reconcile()
			require.Error(t, err)
			assert.EqualError(t, err, tt.wantErr)
			assert.Equal(t, int64(0), f.endpoint().Status.ObservedGeneration)
		})
	}
}

// stepFunc adapts a function to Step.
type stepFunc struct {
	name string
	run  func(context.Context, *ReconcileContext) error
}

func (s stepFunc) Name() string                                        { return s.name }
func (s stepFunc) Run(ctx context.Context, rc *ReconcileContext) error { return s.run(ctx, rc) }

func TestReconcileStepErrorStillPatchesStatus(t *testing.T) {
	var ran bool
	f := newFixture(t, withObjects(inferenceEndpoint(3)), withSteps(
		stepFunc{name: "first", run: func(_ context.Context, rc *ReconcileContext) error {
			rc.SetRegistered(metav1.ConditionUnknown, pylonv1alpha1.RegisteredReasonPending, "set before the failure")
			return nil
		}},
		stepFunc{name: "boom", run: func(context.Context, *ReconcileContext) error { return fmt.Errorf("exploded") }},
		stepFunc{name: "after", run: func(context.Context, *ReconcileContext) error { ran = true; return nil }},
	))
	_, err := f.reconcile()
	require.EqualError(t, err, "step boom: exploded")
	assert.False(t, ran, "steps after a failure do not run")
	ep := f.endpoint()
	assert.Equal(t, int64(0), ep.Status.ObservedGeneration, "observedGeneration moves only after every step succeeds")
	assert.Equal(t, metav1.ConditionUnknown, f.condition(pylonv1alpha1.ConditionRegistered).Status)
}

func TestReconcileStepsShapeRequeueAndEvents(t *testing.T) {
	f := newFixture(t, withObjects(inferenceEndpoint(1)), withSteps(
		stepFunc{name: "fast", run: func(_ context.Context, rc *ReconcileContext) error {
			rc.RequeueAfter(time.Minute)
			rc.RequeueAfter(2 * time.Second)
			rc.RequeueAfter(0)
			rc.Event(corev1.EventTypeNormal, "Custom", "step %s ran", "fast")
			rc.SetTransportReady(metav1.ConditionTrue, pylonv1alpha1.TransportReadyReasonPylonConnected, "tunnel up")
			rc.SetRegistered(metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable, "no stream")
			require.NotNil(t, rc.Condition(pylonv1alpha1.ConditionTransportReady))
			assert.Nil(t, rc.Condition(pylonv1alpha1.ConditionReady))
			return nil
		}},
	))
	res := f.mustReconcile()
	assert.Equal(t, 2*time.Second, res.RequeueAfter)
	assert.ElementsMatch(t, []string{
		"Normal PylonConnected TransportReady is True: tunnel up",
		"Warning RouterUnreachable Registered is False: no stream",
		"Normal Custom step fast ran",
	}, f.events())
}

func TestReconcilePatchErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantResult reconcile.Result
		wantErr    string
	}{
		{name: "conflict", err: conflict(), wantResult: reconcile.Result{RequeueAfter: conflictRetryDelay}},
		{name: "not found", err: notFound()},
		{name: "other", err: fmt.Errorf("etcd unavailable"), wantErr: "patching status: etcd unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					return tt.err
				},
			}))
			res, err := f.reconcile()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantResult, res)
			assert.Empty(t, f.events(), "no Events without a successful patch")
		})
	}
}

func TestReconcilePatchUsesOptimisticLock(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			data, err := patch.Data(obj)
			require.NoError(t, err)
			assert.Contains(t, string(data), `"resourceVersion"`)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}))
	f.mustReconcile()
}
