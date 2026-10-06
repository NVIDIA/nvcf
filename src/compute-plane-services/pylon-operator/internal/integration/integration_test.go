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

// Package integration runs the operator against a real API server started by
// controller-runtime's envtest. The tests skip unless KUBEBUILDER_ASSETS
// points at kube-apiserver and etcd binaries; `make test-envtest` sets it.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/controller"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/prober"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

const (
	testModel      = "meta/llama-3.1-8b-instruct"
	testHealthPath = "/v1/health/ready"
	waitTimeout    = 30 * time.Second
	pollInterval   = 100 * time.Millisecond
)

var (
	restConfig *rest.Config
	scheme     *runtime.Scheme
	k8sClient  client.Client
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		return m.Run()
	}
	ctrl.SetLogger(logr.Discard())

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		return 1
	}
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
		}
	}()

	s, err := controller.NewScheme()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building scheme: %v\n", err)
		return 1
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building client: %v\n", err)
		return 1
	}
	restConfig, scheme, k8sClient = cfg, s, c
	return m.Run()
}

func requireEnv(t *testing.T) {
	t.Helper()
	if k8sClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is not set; skipping the envtest suite")
	}
}

func createNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "pylon-"}}
	require.NoError(t, k8sClient.Create(context.Background(), ns))
	return ns.Name
}

func newEndpoint(namespace, name string) *pylonv1alpha1.InferenceEndpoint {
	return &pylonv1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: pylonv1alpha1.InferenceEndpointSpec{
			ModelName:          testModel,
			InferenceAPIFormat: pylonv1alpha1.InferenceAPIFormat{Type: pylonv1alpha1.InferenceAPIFormatChat},
			Service:            pylonv1alpha1.ServiceReference{Name: "llama-nim", Port: 8000},
			Health:             pylonv1alpha1.HealthCheck{Path: testHealthPath},
		},
	}
}

func TestCRDValidation(t *testing.T) {
	requireEnv(t)
	ctx := context.Background()
	ns := createNamespace(t)

	valid := newEndpoint(ns, "valid")
	valid.Spec.MaxEngineConcurrency = ptr.To[int32](8)
	valid.Spec.GPU = &pylonv1alpha1.GPUSpec{Product: "NVIDIA-GB10"}
	valid.Spec.Canary = &pylonv1alpha1.CanarySpec{TimeoutSeconds: ptr.To[int32](180), IntervalSeconds: ptr.To[int32](60)}
	require.NoError(t, k8sClient.Create(ctx, valid))

	tests := []struct {
		name   string
		mutate func(*pylonv1alpha1.InferenceEndpoint)
		field  string
	}{
		{name: "empty model name", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.ModelName = "" }, field: "spec.modelName"},
		{name: "unknown API format", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.InferenceAPIFormat.Type = "completions" }, field: "spec.inferenceAPIFormat.type"},
		{name: "empty service name", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.Service.Name = "" }, field: "spec.service.name"},
		{name: "port zero", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.Service.Port = 0 }, field: "spec.service.port"},
		{name: "port too large", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.Service.Port = 65536 }, field: "spec.service.port"},
		{name: "relative health path", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.Health.Path = "health" }, field: "spec.health.path"},
		{name: "empty health path", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.Health.Path = "" }, field: "spec.health.path"},
		{name: "zero concurrency", mutate: func(e *pylonv1alpha1.InferenceEndpoint) { e.Spec.MaxEngineConcurrency = ptr.To[int32](0) }, field: "spec.maxEngineConcurrency"},
		{name: "zero canary timeout", mutate: func(e *pylonv1alpha1.InferenceEndpoint) {
			e.Spec.Canary = &pylonv1alpha1.CanarySpec{TimeoutSeconds: ptr.To[int32](0)}
		}, field: "spec.canary.timeoutSeconds"},
		{name: "excessive canary timeout", mutate: func(e *pylonv1alpha1.InferenceEndpoint) {
			e.Spec.Canary = &pylonv1alpha1.CanarySpec{TimeoutSeconds: ptr.To[int32](301)}
		}, field: "spec.canary.timeoutSeconds"},
		{name: "disabled active canary", mutate: func(e *pylonv1alpha1.InferenceEndpoint) {
			e.Spec.Canary = &pylonv1alpha1.CanarySpec{IntervalSeconds: ptr.To[int32](0)}
		}, field: "spec.canary.intervalSeconds"},
		{name: "excessive canary interval", mutate: func(e *pylonv1alpha1.InferenceEndpoint) {
			e.Spec.Canary = &pylonv1alpha1.CanarySpec{IntervalSeconds: ptr.To[int32](3601)}
		}, field: "spec.canary.intervalSeconds"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := newEndpoint(ns, fmt.Sprintf("invalid-%d", i))
			tt.mutate(ep)
			err := k8sClient.Create(ctx, ep)
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), err.Error())
			assert.Contains(t, err.Error(), tt.field)
		})
	}

	t.Run("status enum", func(t *testing.T) {
		ep := &pylonv1alpha1.InferenceEndpoint{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(valid), ep))
		patch := client.MergeFrom(ep.DeepCopy())
		ep.Status.GPU = &pylonv1alpha1.GPUStatus{Source: "Guessed"}
		err := k8sClient.Status().Patch(ctx, ep, patch)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status.gpu.source")
	})
}

func TestStatusSubresourceAndPrinterColumns(t *testing.T) {
	requireEnv(t)
	ctx := context.Background()
	ns := createNamespace(t)

	ep := newEndpoint(ns, "llama")
	require.NoError(t, k8sClient.Create(ctx, ep))
	assert.Equal(t, int64(1), ep.Generation)

	// A spec change through the main resource ignores status.
	ep.Status.ObservedGeneration = 42
	ep.Spec.Health.Path = "/health"
	require.NoError(t, k8sClient.Update(ctx, ep))
	assert.Equal(t, int64(2), ep.Generation)
	assert.Zero(t, ep.Status.ObservedGeneration)

	patch := client.MergeFrom(ep.DeepCopy())
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	ep.Status = pylonv1alpha1.InferenceEndpointStatus{
		ObservedGeneration: 2,
		Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "HealthProbeSucceeded", Message: "ok", LastTransitionTime: now, ObservedGeneration: 2},
			{Type: "TransportReady", Status: metav1.ConditionUnknown, Reason: "Pending", Message: "pending", LastTransitionTime: now, ObservedGeneration: 2},
			{Type: "Registered", Status: metav1.ConditionUnknown, Reason: "Pending", Message: "pending", LastTransitionTime: now, ObservedGeneration: 2},
		},
		Registration: &pylonv1alpha1.RegistrationStatus{ClusterID: "spark-berlin", RoutersConnected: 1, LastRegisteredTime: &now},
		GPU:          &pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels, NodeProducts: []string{"NVIDIA-GB10"}},
		Servers: []pylonv1alpha1.ServerStatus{{
			InferenceServerID:   "spark-berlin." + ns + ".llama.0",
			Pod:                 "pylon-llama-0",
			RegistrationStreams: 1,
			ReverseTunnels:      1,
		}},
	}
	want := ep.Status.DeepCopy()
	require.NoError(t, k8sClient.Status().Patch(ctx, ep, patch))

	got := &pylonv1alpha1.InferenceEndpoint{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(ep), got))
	assert.Equal(t, int64(2), got.Generation, "a status patch does not bump generation")
	assert.Equal(t, "/health", got.Spec.Health.Path)
	assert.Equal(t, *want, got.Status)

	table := getTable(t, ns)
	var columns []string
	for _, c := range table.ColumnDefinitions {
		columns = append(columns, c.Name)
	}
	assert.Equal(t, []string{"Name", "Model", "GPU", "Ready", "Registered", "Servers", "Age"}, columns)
	require.Len(t, table.Rows, 1)
	cells := table.Rows[0].Cells
	require.Len(t, cells, 7)
	assert.Equal(t, "llama", cells[0])
	assert.Equal(t, testModel, cells[1])
	assert.Equal(t, "NVIDIA-GB10", cells[2])
	assert.Equal(t, "True", cells[3])
	assert.Equal(t, "Unknown", cells[4])
	assert.EqualValues(t, 1, cells[5])
}

// getTable lists InferenceEndpoints in namespace as a server-side Table, the
// representation kubectl get prints.
func getTable(t *testing.T, namespace string) *metav1.Table {
	t.Helper()
	cfg := rest.CopyConfig(restConfig)
	cfg.GroupVersion = &pylonv1alpha1.GroupVersion
	cfg.APIPath = "/apis"
	cfg.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()
	rc, err := rest.RESTClientFor(cfg)
	require.NoError(t, err)
	body, err := rc.Get().Namespace(namespace).Resource("inferenceendpoints").
		SetHeader("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io").
		DoRaw(context.Background())
	require.NoError(t, err)
	table := &metav1.Table{}
	require.NoError(t, json.Unmarshal(body, table))
	return table
}

// setupRecorder is a step that records that the reconciler handed it the
// controller builder.
type setupRecorder struct {
	called atomic.Bool
}

func (s *setupRecorder) Name() string { return "setup-recorder" }

func (s *setupRecorder) Run(context.Context, *controller.ReconcileContext) error { return nil }

func (s *setupRecorder) SetupWithManager(_ context.Context, _ ctrl.Manager, b *builder.Builder) error {
	if b == nil {
		return fmt.Errorf("no builder")
	}
	s.called.Store(true)
	return nil
}

func TestControllerReconcilesThroughWatches(t *testing.T) {
	requireEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns := createNamespace(t)
	otherNS := createNamespace(t)
	operatorNS := createNamespace(t)

	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: operatorNS},
		Data:       map[string][]byte{transport.CredentialKey: []byte("t0-token")},
	}
	require.NoError(t, k8sClient.Create(ctx, source))

	var healthy atomic.Bool
	healthy.Store(true)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testHealthPath:
			if !healthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case prober.ModelsPath:
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, testModel)
		}
	}))
	t.Cleanup(backend.Close)
	// Every transport pod's metrics: one open stream and tunnel.
	pylon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "pylon_registration_stream_connected{router=\"router-0\"} 1\npylon_reverse_tunnel_connected{router=\"router-0\"} 1\n")
	}))
	t.Cleanup(pylon.Close)

	cfg := config.Config{
		ClusterID:               "spark-berlin",
		RouterGRPCAddress:       "router:50071",
		PylonImage:              "nvcr.io/nvidia/pylon:0.15.2",
		OperatorNamespace:       operatorNS,
		InitialInputTPS:         config.DefaultInitialInputTPS,
		WatchNamespaces:         []string{ns},
		TransportReplicas:       1,
		ProbeInterval:           time.Hour,
		ScrapeInterval:          5 * time.Second,
		MetricsBindAddress:      "0",
		HealthProbeBindAddress:  "0",
		ClusterCredentialSecret: config.DefaultClusterCredentialSecret,
	}
	require.NoError(t, cfg.Validate())
	opts := cfg.ManagerOptions(scheme)
	opts.Logger = testr.New(t)
	mgr, err := ctrl.NewManager(restConfig, opts)
	require.NoError(t, err)

	m := metrics.New()
	require.NoError(t, m.Register(prometheus.NewRegistry()))
	p := prober.New(m)
	p.BaseURL = func(*pylonv1alpha1.InferenceEndpoint) string { return backend.URL }
	o := registration.New(mgr.GetClient(), m)
	o.MetricsURL = func(*corev1.Pod) string { return pylon.URL + registration.MetricsPath }
	extra := &setupRecorder{}
	steps := append(controller.DefaultSteps(mgr.GetClient(), p, &gpu.Resolver{Reader: mgr.GetClient()},
		transport.New(mgr.GetClient(), cfg), o), extra)
	r := controller.NewReconciler(controller.Options{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("pylon-operator"),
		Config:   cfg,
		Metrics:  m,
		Steps:    steps,
	})
	require.NoError(t, r.SetupWithManager(ctx, mgr))
	assert.True(t, extra.called.Load(), "ManagerSetup steps are set up with the controller")

	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "spark-",
		Labels:       map[string]string{gpu.ProductLabel: "NVIDIA-GB10"},
	}}
	require.NoError(t, k8sClient.Create(ctx, node))
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), node) })

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "llama-nim", Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "llama"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8000}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, svc))
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llama-nim-abc",
			Namespace: ns,
			Labels:    map[string]string{discoveryv1.LabelServiceName: "llama-nim"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To[int32](8000)}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			NodeName:   ptr.To(node.Name),
		}},
	}
	require.NoError(t, k8sClient.Create(ctx, slice))

	ep := newEndpoint(ns, "llama")
	require.NoError(t, k8sClient.Create(ctx, ep))
	unwatched := newEndpoint(otherNS, "llama")
	require.NoError(t, k8sClient.Create(ctx, unwatched))

	waitFor(t, ep, "Ready True with the node's GPU, transport pods not running", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		assertCondition(c, got, pylonv1alpha1.ConditionReady, metav1.ConditionTrue, string(pylonv1alpha1.ReadyReasonHealthProbeSucceeded))
		assertCondition(c, got, pylonv1alpha1.ConditionTransportReady, metav1.ConditionFalse, string(pylonv1alpha1.TransportReadyReasonTransportPodsNotRunning))
		assertCondition(c, got, pylonv1alpha1.ConditionRegistered, metav1.ConditionFalse, string(pylonv1alpha1.RegisteredReasonTransportPodsNotRunning))
		assert.Equal(c, got.Generation, got.Status.ObservedGeneration)
		if assert.NotNil(c, got.Status.GPU) {
			assert.Equal(c, "NVIDIA-GB10", got.Status.GPU.Product)
			assert.Equal(c, pylonv1alpha1.GPUSourceNodeLabels, got.Status.GPU.Source)
		}
	})

	// The API server accepted the rendered Deployment, owned by the endpoint.
	deployKey := client.ObjectKey{Namespace: ns, Name: "pylon-llama"}
	deploy := &appsv1.Deployment{}
	require.NoError(t, k8sClient.Get(ctx, deployKey, deploy))
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(ep), ep))
	assert.True(t, metav1.IsControlledBy(deploy, ep))
	assert.Equal(t, "pylon-operator", deploy.Labels["app.kubernetes.io/managed-by"])
	templateHash := deploy.Spec.Template.Annotations[transport.SpecHashAnnotation]
	assert.NotEmpty(t, templateHash)
	replica := &corev1.Secret{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: config.DefaultClusterCredentialSecret}, replica))
	assert.Equal(t, []byte("t0-token"), replica.Data[transport.CredentialKey])

	// Deployment status reaches the endpoint through the owner watch; the
	// probe interval is an hour, so nothing else requeues it.
	deploy.Status.Replicas, deploy.Status.ReadyReplicas = 1, 1
	require.NoError(t, k8sClient.Status().Update(ctx, deploy))
	waitFor(t, ep, "transport ready, no transport pod listed yet", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		assertCondition(c, got, pylonv1alpha1.ConditionTransportReady, metav1.ConditionFalse, string(pylonv1alpha1.TransportReadyReasonTunnelNotConnected))
		assertCondition(c, got, pylonv1alpha1.ConditionRegistered, metav1.ConditionFalse, string(pylonv1alpha1.RegisteredReasonPending))
	})

	// A running transport pod reaches the observer through the Pod cache,
	// which holds only pods with the managed-by label, and the scrape
	// interval requeue. A pod without that label is never seen.
	podLabels := transport.PodLabels(ep)
	impostor := runningPod(t, ns, "pylon-llama-impostor", podLabels)
	podLabels[config.ManagedByLabel] = config.ManagedBy
	pod := runningPod(t, ns, "pylon-llama-abc12", podLabels)
	waitFor(t, ep, "registered through the transport pod's metrics", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		assertCondition(c, got, pylonv1alpha1.ConditionTransportReady, metav1.ConditionTrue, string(pylonv1alpha1.TransportReadyReasonPylonConnected))
		assertCondition(c, got, pylonv1alpha1.ConditionRegistered, metav1.ConditionTrue, string(pylonv1alpha1.RegisteredReasonRegisteredWithRouter))
		if assert.NotNil(c, got.Status.Registration) {
			assert.Equal(c, int32(1), got.Status.Registration.RoutersConnected)
			assert.NotNil(c, got.Status.Registration.LastRegisteredTime)
		}
		assert.Equal(c, []pylonv1alpha1.ServerStatus{{
			InferenceServerID:   "spark-berlin." + ns + ".llama." + pod.Name,
			Pod:                 pod.Name,
			RegistrationStreams: 1,
			ReverseTunnels:      1,
		}}, got.Status.Servers, "not %s", impostor.Name)
	})

	// A rotated source reaches the copy through the Secret watch.
	sourcePatch := client.MergeFrom(source.DeepCopy())
	source.Data[transport.CredentialKey] = []byte("t0-rotated")
	require.NoError(t, k8sClient.Patch(ctx, source, sourcePatch))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := &corev1.Secret{}
		if assert.NoError(c, k8sClient.Get(ctx, client.ObjectKeyFromObject(replica), got)) {
			assert.Equal(c, []byte("t0-rotated"), got.Data[transport.CredentialKey])
		}
	}, waitTimeout, pollInterval, "replica follows the source")

	// A node label change reaches the endpoint through the Node watch. The GPU
	// type lives in status only, so the transport keeps its pods.
	nodePatch := client.MergeFrom(node.DeepCopy())
	node.Labels[gpu.ProductLabel] = "NVIDIA-GB300"
	require.NoError(t, k8sClient.Patch(ctx, node, nodePatch))
	waitFor(t, ep, "GPU follows the node label", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		if assert.NotNil(c, got.Status.GPU) {
			assert.Equal(c, "NVIDIA-GB300", got.Status.GPU.Product)
		}
	})
	require.NoError(t, k8sClient.Get(ctx, deployKey, deploy))
	assert.Equal(t, templateHash, deploy.Spec.Template.Annotations[transport.SpecHashAnnotation], "a GPU type change does not roll the transport")

	// An EndpointSlice change reaches it through the EndpointSlice watch.
	slicePatch := client.MergeFrom(slice.DeepCopy())
	slice.Endpoints[0].Conditions.Ready = ptr.To(false)
	require.NoError(t, k8sClient.Patch(ctx, slice, slicePatch))
	waitFor(t, ep, "NoReadyEndpoints", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		assertCondition(c, got, pylonv1alpha1.ConditionReady, metav1.ConditionFalse, string(pylonv1alpha1.ReadyReasonNoReadyEndpoints))
	})

	// A Service deletion reaches it through the Service watch.
	require.NoError(t, k8sClient.Delete(ctx, svc))
	waitFor(t, ep, "ServiceNotFound", func(c *assert.CollectT, got *pylonv1alpha1.InferenceEndpoint) {
		assertCondition(c, got, pylonv1alpha1.ConditionReady, metav1.ConditionFalse, string(pylonv1alpha1.ReadyReasonServiceNotFound))
	})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var events corev1.EventList
		if !assert.NoError(c, k8sClient.List(ctx, &events, client.InNamespace(ns))) {
			return
		}
		reasons := map[string]string{}
		for _, e := range events.Items {
			if e.InvolvedObject.Name == ep.Name {
				reasons[e.Reason] = e.Type
			}
		}
		assert.Equal(c, corev1.EventTypeNormal, reasons["HealthProbeSucceeded"])
		assert.Equal(c, corev1.EventTypeNormal, reasons[controller.EventReasonGPUProductChanged])
		assert.Equal(c, corev1.EventTypeWarning, reasons["NoReadyEndpoints"])
		assert.Equal(c, corev1.EventTypeWarning, reasons["ServiceNotFound"])
		assert.Equal(c, corev1.EventTypeWarning, reasons["TransportPodsNotRunning"])
		assert.Equal(c, corev1.EventTypeNormal, reasons["RegisteredWithRouter"])
	}, waitTimeout, pollInterval)

	// The endpoint outside --watch-namespaces is never reconciled.
	got := &pylonv1alpha1.InferenceEndpoint{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(unwatched), got))
	assert.Empty(t, got.Status.Conditions)
	err = k8sClient.Get(ctx, client.ObjectKey{Namespace: otherNS, Name: "pylon-llama"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err), "no transport outside --watch-namespaces")
}

// runningPod creates a pod and reports it running and ready, as the kubelet
// would.
func runningPod(t *testing.T, namespace, name string, labels map[string]string) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pylon", Image: "nvcr.io/nvidia/pylon:0.15.2"}}},
	}
	require.NoError(t, k8sClient.Create(ctx, pod))
	now := metav1.Now()
	pod.Status = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		PodIP:      "10.0.0.5",
		PodIPs:     []corev1.PodIP{{IP: "10.0.0.5"}},
		StartTime:  &now,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: now}},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))
	return pod
}

func waitFor(t *testing.T, ep *pylonv1alpha1.InferenceEndpoint, what string, check func(*assert.CollectT, *pylonv1alpha1.InferenceEndpoint)) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := &pylonv1alpha1.InferenceEndpoint{}
		if !assert.NoError(c, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(ep), got)) {
			return
		}
		check(c, got)
	}, waitTimeout, pollInterval, what)
}

func assertCondition(c *assert.CollectT, ep *pylonv1alpha1.InferenceEndpoint, t pylonv1alpha1.ConditionType, status metav1.ConditionStatus, reason string) {
	cond := meta.FindStatusCondition(ep.Status.Conditions, string(t))
	if !assert.NotNil(c, cond, "condition %s", t) {
		return
	}
	assert.Equal(c, status, cond.Status, "condition %s status", t)
	assert.Equal(c, reason, cond.Reason, "condition %s reason", t)
}
