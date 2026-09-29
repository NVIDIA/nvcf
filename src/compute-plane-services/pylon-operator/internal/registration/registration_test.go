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

package registration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

const (
	testNamespace = "models"
	testName      = "llama"
	testCluster   = "spark-berlin"
)

// t0 is the observer's start; the harness clock starts here.
var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// pylon is what a fake transport pod exposes.
type pylon struct {
	streams  []string
	tunnels  []string
	closures map[Closure]float64
	// status, when set, is the HTTP status instead of 200.
	status int
	// raw, when set, is the body instead of the exposition.
	raw string
}

func (p pylon) text() string {
	var b strings.Builder
	b.WriteString("# HELP pylon_build_info Build information.\n# TYPE pylon_build_info gauge\npylon_build_info{version=\"0.15.2\"} 1\n")
	b.WriteString("# TYPE pylon_registration_stream_connected gauge\n")
	for _, r := range p.streams {
		fmt.Fprintf(&b, "pylon_registration_stream_connected{router=%q} 1\n", r)
	}
	b.WriteString("pylon_registration_stream_connected{router=\"router-down\"} 0\n")
	b.WriteString("# TYPE pylon_reverse_tunnel_connected gauge\n")
	for _, r := range p.tunnels {
		fmt.Fprintf(&b, "pylon_reverse_tunnel_connected{router=%q} 1\n", r)
	}
	b.WriteString("pylon_reverse_tunnel_connected{router=\"router-down\"} 0\n")
	b.WriteString("# TYPE pylon_registration_stream_closures_total counter\n")
	b.WriteString("pylon_registration_stream_closures_total{router=\"router-0\",reason=\"io\"} 7\n")
	for c, v := range p.closures {
		fmt.Fprintf(&b, "pylon_registration_stream_closures_total{router=%q,reason=%q} %g\n", c.Router, c.Reason, v)
	}
	return b.String()
}

type harness struct {
	t        *testing.T
	ctx      context.Context
	client   client.Client
	observer *Observer
	metrics  *metrics.Metrics
	registry *prometheus.Registry
	server   *httptest.Server
	closed   string
	now      time.Time
	ep       *pylonv1alpha1.InferenceEndpoint
	ready    int32

	mu          sync.Mutex
	pylons      map[string]pylon
	unreachable map[string]bool
	logs        []string
}

type harnessOption func(*fake.ClientBuilder)

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{
		t:           t,
		now:         t0,
		ready:       1,
		pylons:      map[string]pylon{},
		unreachable: map[string]bool{},
		ep: &pylonv1alpha1.InferenceEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name: testName, Namespace: testNamespace, UID: "uid-1", Generation: 1,
				CreationTimestamp: metav1.NewTime(t0.Add(-2 * time.Minute)),
			},
			Spec: pylonv1alpha1.InferenceEndpointSpec{ModelName: "meta/llama-3.1-8b-instruct"},
		},
	}
	h.setReady(true)
	h.logs = nil
	h.ctx = log.IntoContext(context.Background(), funcr.New(func(prefix, args string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.logs = append(h.logs, args)
	}, funcr.Options{Verbosity: 1}))

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range opts {
		o(b)
	}
	h.client = b.Build()

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pod := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/pods/"), MetricsPath)
		h.mu.Lock()
		p, ok := h.pylons[pod]
		h.mu.Unlock()
		switch {
		case !ok:
			http.NotFound(w, r)
		case p.status != 0:
			w.WriteHeader(p.status)
		case p.raw != "":
			_, _ = w.Write([]byte(p.raw))
		default:
			_, _ = w.Write([]byte(p.text()))
		}
	}))
	t.Cleanup(h.server.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	h.closed = closed.URL
	closed.Close()

	h.metrics = metrics.New()
	h.registry = prometheus.NewRegistry()
	require.NoError(t, h.metrics.Register(h.registry))
	h.observer = New(h.client, h.metrics)
	h.observer.Client = h.server.Client()
	h.observer.Now = func() time.Time { return h.now }
	h.observer.Started = t0
	h.observer.MetricsURL = func(p *corev1.Pod) string {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.unreachable[p.Name] {
			return h.closed + MetricsPath
		}
		return h.server.URL + "/pods/" + p.Name + MetricsPath
	}
	return h
}

func (h *harness) setReady(ready bool) {
	c := metav1.Condition{Type: string(pylonv1alpha1.ConditionReady), Status: metav1.ConditionTrue, Reason: string(pylonv1alpha1.ReadyReasonHealthProbeSucceeded)}
	if !ready {
		c.Status, c.Reason = metav1.ConditionFalse, string(pylonv1alpha1.ReadyReasonHealthProbeFailed)
	}
	meta.SetStatusCondition(&h.ep.Status.Conditions, c)
}

func (h *harness) expose(pod string, p pylon) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pylons[pod] = p
}

func (h *harness) setUnreachable(pod string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unreachable[pod] = true
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

// addPod creates a running transport pod that became ready at readyAt and
// started a second earlier.
func (h *harness) addPod(name string, readyAt time.Time) *corev1.Pod {
	h.t.Helper()
	p := transportPod(h.ep, name, readyAt)
	require.NoError(h.t, h.client.Create(context.Background(), p))
	return p
}

func (h *harness) createPod(p *corev1.Pod) {
	h.t.Helper()
	require.NoError(h.t, h.client.Create(context.Background(), p))
}

func transportPod(ep *pylonv1alpha1.InferenceEndpoint, name string, readyAt time.Time) *corev1.Pod {
	labels := transport.PodLabels(ep)
	labels[config.ManagedByLabel] = config.ManagedBy
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ep.Namespace, Labels: labels},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.0.0.1",
			StartTime: &metav1.Time{Time: readyAt.Add(-time.Second)},
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(readyAt),
			}},
		},
	}
}

func (h *harness) state() transport.State {
	return transport.State{
		DeploymentName:  transport.DeploymentName(h.ep),
		PodLabels:       transport.PodLabels(h.ep),
		DesiredReplicas: h.ready,
		ReadyReplicas:   h.ready,
	}
}

// observe runs the observer and writes its result to the endpoint the way
// the registration step does, so the next observation sees it.
func (h *harness) observe() Result {
	h.t.Helper()
	res, err := h.observer.Observe(h.ctx, h.ep, h.state(), testCluster)
	require.NoError(h.t, err)
	for _, c := range []metav1.Condition{
		{Type: string(pylonv1alpha1.ConditionTransportReady), Status: res.TransportReady.Status, Reason: string(res.TransportReady.Reason), Message: res.TransportReady.Message},
		{Type: string(pylonv1alpha1.ConditionRegistered), Status: res.Registered.Status, Reason: string(res.Registered.Reason), Message: res.Registered.Message},
	} {
		meta.SetStatusCondition(&h.ep.Status.Conditions, c)
	}
	reg := res.Registration
	h.ep.Status.Registration = &reg
	h.ep.Status.Servers = res.Servers
	return res
}

func assertRegistered(t *testing.T, res Result, status metav1.ConditionStatus, reason pylonv1alpha1.RegisteredReason) {
	t.Helper()
	assert.Equal(t, status, res.Registered.Status, res.Registered.Message)
	assert.Equal(t, reason, res.Registered.Reason, res.Registered.Message)
}

func assertTransportReady(t *testing.T, res Result, status metav1.ConditionStatus, reason pylonv1alpha1.TransportReadyReason) {
	t.Helper()
	assert.Equal(t, status, res.TransportReady.Status, res.TransportReady.Message)
	assert.Equal(t, reason, res.TransportReady.Reason, res.TransportReady.Message)
}

func (h *harness) scrapeFailures(reason metrics.ScrapeFailureReason) float64 {
	h.t.Helper()
	return h.metricValue("nvcf_pylon_operator_scrape_failures_total", "reason", string(reason))
}

func (h *harness) metricValue(name, label, value string) float64 {
	h.t.Helper()
	families, err := h.registry.Gather()
	require.NoError(h.t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	h.t.Fatalf("no series %s{%s=%q}", name, label, value)
	return 0
}

func (h *harness) timeToRegistered() (uint64, float64) {
	h.t.Helper()
	families, err := h.registry.Gather()
	require.NoError(h.t, err)
	for _, f := range families {
		if f.GetName() == "nvcf_pylon_operator_time_to_registered_seconds" {
			hist := f.GetMetric()[0].GetHistogram()
			return hist.GetSampleCount(), hist.GetSampleSum()
		}
	}
	h.t.Fatal("no time_to_registered histogram")
	return 0, 0
}

// Truth table: transport pod just started, stream being opened.
func TestConvergingIsPendingAndTunnelNotConnected(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-5*time.Second))
	h.expose("pylon-llama-a", pylon{})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonPending)
	assert.Equal(t, "Transport pods are starting and opening registration streams", res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
	assert.Equal(t, "No transport pod has a reverse tunnel connected", res.TransportReady.Message)
	assert.Empty(t, res.Rejected)
}

// Truth table: backend healthy, Pylon cannot reach the router.
func TestRouterUnreachableAfterGracePeriod(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0)
	h.expose("pylon-llama-a", pylon{})

	h.advance(RouterUnreachableGrace)
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonPending)

	h.advance(time.Second)
	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)
	assert.Equal(t, "No transport pod has a registration stream open more than 30s after the first became ready; check --router-grpc-address and that the router's gRPC port is reachable from the transport pods",
		res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
}

func TestRouterUnreachableNeedsAKnownReadyTime(t *testing.T) {
	h := newHarness(t)
	notReady := transportPod(h.ep, "pylon-llama-a", t0.Add(-time.Hour))
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	h.createPod(notReady)
	noTimes := transportPod(h.ep, "pylon-llama-b", t0)
	noTimes.Status.Conditions = nil
	noTimes.Status.StartTime = nil
	h.createPod(noTimes)
	h.expose("pylon-llama-a", pylon{})
	h.expose("pylon-llama-b", pylon{})

	h.advance(time.Hour)
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonPending)
}

func TestReadySince(t *testing.T) {
	pod := transportPod(&pylonv1alpha1.InferenceEndpoint{}, "p", t0)
	got, ok := readySince(pod)
	assert.True(t, ok)
	assert.Equal(t, t0, got)

	pod.Status.Conditions[0].LastTransitionTime = metav1.Time{}
	got, ok = readySince(pod)
	assert.True(t, ok)
	assert.Equal(t, t0.Add(-time.Second), got, "start time without a transition time")

	pod.Status.Conditions = nil
	got, ok = readySince(pod)
	assert.True(t, ok)
	assert.Equal(t, t0.Add(-time.Second), got, "start time without a Ready condition")

	pod.Status.StartTime = nil
	_, ok = readySince(pod)
	assert.False(t, ok)
}

// Truth table: registration stream closed by the router with
// unauthenticated or invalid_argument.
func TestRejectedViaClosureCounterIncrease(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	unauthenticated := Closure{Router: "router-1", Reason: ClosureUnauthenticated}
	invalid := Closure{Router: "router-1", Reason: ClosureInvalidArgument}
	idle := Closure{Router: "router-1", Reason: "idle_timeout"}

	// The pod started before the observer: its counters are the baseline.
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{unauthenticated: 2, idle: 1}})
	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)
	assert.Empty(t, res.Rejected)

	// A disconnect reason is not a rejection.
	h.advance(5 * time.Second)
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{unauthenticated: 2, idle: 5}})
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)

	h.advance(5 * time.Second)
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{unauthenticated: 3, idle: 5}})
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)
	assert.Equal(t, `The router rejected the registration: router "router-1" closed the stream of pod pylon-llama-a with unauthenticated (1 new); check that the cluster credential matches the router's and that --cluster-id is the cluster the router expects`,
		res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
	assert.Equal(t, `Registration rejected since the last scrape: router "router-1" closed the stream of pod pylon-llama-a with unauthenticated (1 new)`, res.Rejected)

	// No rise, but within the memory window: still rejected, no new Event.
	h.advance(RejectionMemory)
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)
	assert.Empty(t, res.Rejected)

	h.advance(time.Second)
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)

	// invalid_argument counts too, from zero when the series appears.
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{unauthenticated: 3, idle: 5, invalid: 2}})
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)
	assert.Contains(t, res.Rejected, "with invalid_argument (2 new)")
}

func TestRejectionsOfANewPodCountFromZero(t *testing.T) {
	h := newHarness(t)
	h.advance(10 * time.Second)
	h.addPod("pylon-llama-a", h.now.Add(-2*time.Second))
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{{Router: "router-0", Reason: ClosureUnauthenticated}: 1}})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)
	assert.Contains(t, res.Rejected, "pod pylon-llama-a with unauthenticated (1 new)")
}

func TestRejectionByOneRouterWhileRegisteredWithAnother(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})
	h.observe()

	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"},
		closures: map[Closure]float64{{Router: "router-1", Reason: ClosureUnauthenticated}: 1}})
	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assert.Contains(t, res.Rejected, `router "router-1" closed the stream of pod pylon-llama-a with unauthenticated (1 new)`)
}

func TestManyRejectionsAreSummarised(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{})
	h.observe()
	closures := map[Closure]float64{}
	for i := range 5 {
		closures[Closure{Router: fmt.Sprintf("router-%d", i), Reason: ClosureUnauthenticated}] = 1
	}
	h.expose("pylon-llama-a", pylon{closures: closures})
	res := h.observe()
	assert.True(t, strings.HasSuffix(res.Rejected, "; and 2 more"), res.Rejected)
	assert.Contains(t, res.Rejected, `router "router-0"`)
	assert.NotContains(t, res.Rejected, `router "router-4"`)
}

// A decrease means Pylon restarted: the baseline is zero again.
func TestClosureCounterReset(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Hour))
	c := Closure{Router: "router-0", Reason: ClosureUnauthenticated}
	step := func(v float64, present bool) Result {
		h.t.Helper()
		h.advance(2 * RejectionMemory)
		p := pylon{}
		if present {
			p.closures = map[Closure]float64{c: v}
		}
		h.expose("pylon-llama-a", p)
		return h.observe()
	}

	assert.Empty(t, step(3, true).Rejected, "first sight is the baseline")
	assert.Empty(t, step(0, true).Rejected, "a restart to zero is no rejection")
	assert.Empty(t, step(0, true).Rejected)
	res := step(1, true)
	assert.Contains(t, res.Rejected, "(1 new)", "counted from the reset baseline")
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)

	assert.Contains(t, step(4, true).Rejected, "(3 new)")
	res = step(2, true)
	assert.Contains(t, res.Rejected, "(2 new)", "a restart followed by two rejections before the next scrape")

	assert.Empty(t, step(0, false).Rejected, "a series that disappears resets too")
	assert.Contains(t, step(1, true).Rejected, "(1 new)", "and counts from zero when it returns")
}

// Truth table: stream admitted, reverse tunnel not connected.
func TestStreamUpTunnelDown(t *testing.T) {
	h := newHarness(t)
	h.ready = 2
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.addPod("pylon-llama-b", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}})
	h.expose("pylon-llama-b", pylon{})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
	assert.Equal(t, "No transport pod has a reverse tunnel connected; 1 of 2 pods have a registration stream but no tunnel, which points to a blocked UDP port or a QUIC certificate or SNI mismatch",
		res.TransportReady.Message)
}

// Truth table: steady state.
func TestSteadyState(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assert.Equal(t, `1 of 1 transport pods have a registration stream open to 1 router ("router-0")`, res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionTrue, pylonv1alpha1.TransportReadyReasonPylonConnected)
	assert.Equal(t, `1 of 1 transport pods have a reverse tunnel connected to 1 router ("router-0")`, res.TransportReady.Message)
	assert.Equal(t, pylonv1alpha1.RegistrationStatus{ClusterID: testCluster, RoutersConnected: 1, LastRegisteredTime: &metav1.Time{Time: t0}}, res.Registration)
	assert.Equal(t, []pylonv1alpha1.ServerStatus{{
		InferenceServerID:   "spark-berlin.models.llama.pylon-llama-a",
		Pod:                 "pylon-llama-a",
		RegistrationStreams: 1,
		ReverseTunnels:      1,
	}}, res.Servers)
	assert.Empty(t, res.Rejected)
	assert.Zero(t, h.scrapeFailures(metrics.ScrapeFailureConnect))
}

func TestTunnelWithoutReadyReplicasIsNotConnected(t *testing.T) {
	h := newHarness(t)
	h.ready = 0
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})
	assertTransportReady(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
}

// Truth table: Ready False, Pylon idles.
func TestWaitingForUpstreamWhileReadyIsFalse(t *testing.T) {
	h := newHarness(t)
	h.setReady(false)
	h.addPod("pylon-llama-a", t0.Add(-time.Hour))
	h.expose("pylon-llama-a", pylon{})

	h.advance(time.Hour)
	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonWaitingForUpstream)
	assert.Equal(t, "Ready is False, so Pylon idles until the backend answers its health probe and opens no registration stream", res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonWaitingForUpstream)
	assert.Equal(t, "Ready is False, so Pylon idles until the backend answers its health probe and opens no reverse tunnel", res.TransportReady.Message)

	// WaitingForUpstream precedes RegistrationRejected.
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{{Router: "r", Reason: ClosureUnauthenticated}: 1}})
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonWaitingForUpstream)
	assert.NotEmpty(t, res.Rejected, "the rejection is still reported as an Event")
}

// Truth table: backend fails after registration. The stream stays open, so
// both conditions stay True (registered, unhealthy).
func TestBackendFailsAfterRegistration(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})
	h.observe()

	h.setReady(false)
	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assertTransportReady(t, res, metav1.ConditionTrue, pylonv1alpha1.TransportReadyReasonPylonConnected)

	// Stream up but tunnel gone while Ready is False: WaitingForUpstream,
	// never a connectivity reason.
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}})
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonWaitingForUpstream)
}

func TestMultiplePodsAndRoutersAggregate(t *testing.T) {
	h := newHarness(t)
	h.ready = 2
	h.addPod("pylon-llama-b", t0.Add(-time.Minute))
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	pending := transportPod(h.ep, "pylon-llama-c", t0)
	pending.Status = corev1.PodStatus{Phase: corev1.PodPending}
	h.createPod(pending)
	evicted := transportPod(h.ep, "pylon-llama-d", t0.Add(-time.Hour))
	evicted.Status.Phase = corev1.PodFailed
	h.createPod(evicted)
	other := transportPod(&pylonv1alpha1.InferenceEndpoint{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNamespace}}, "pylon-other-a", t0)
	h.createPod(other)
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0", "router-1"}, tunnels: []string{"router-0"}})
	h.expose("pylon-llama-b", pylon{streams: []string{"router-1", "router-2"}})
	h.expose("pylon-llama-d", pylon{streams: []string{"router-9"}, tunnels: []string{"router-9"}})
	h.expose("pylon-other-a", pylon{streams: []string{"router-9"}, tunnels: []string{"router-9"}})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assert.Equal(t, `2 of 3 transport pods have a registration stream open to 3 routers ("router-0", "router-1", "router-2")`, res.Registered.Message)
	assertTransportReady(t, res, metav1.ConditionTrue, pylonv1alpha1.TransportReadyReasonPylonConnected)
	assert.Equal(t, `1 of 3 transport pods have a reverse tunnel connected to 1 router ("router-0")`, res.TransportReady.Message)
	assert.Equal(t, int32(3), res.Registration.RoutersConnected)
	assert.Equal(t, []pylonv1alpha1.ServerStatus{
		{InferenceServerID: "spark-berlin.models.llama.pylon-llama-a", Pod: "pylon-llama-a", RegistrationStreams: 2, ReverseTunnels: 1},
		{InferenceServerID: "spark-berlin.models.llama.pylon-llama-b", Pod: "pylon-llama-b", RegistrationStreams: 2},
		{InferenceServerID: "spark-berlin.models.llama.pylon-llama-c", Pod: "pylon-llama-c"},
	}, res.Servers)
}

func TestManyRoutersAreSummarised(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"r1", "r2", "r3", "r4", "r5"}, tunnels: []string{"r1"}})
	res := h.observe()
	assert.Equal(t, `1 of 1 transport pods have a registration stream open to 5 routers ("r1", "r2", "r3", and 2 more)`, res.Registered.Message)
	assert.Equal(t, int32(5), res.Registration.RoutersConnected)
}

func TestUnreachablePodCountsAsNoStreams(t *testing.T) {
	h := newHarness(t)
	h.ready = 2
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.addPod("pylon-llama-b", t0.Add(-time.Minute))
	h.setUnreachable("pylon-llama-a")
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})
	h.expose("pylon-llama-b", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assert.Equal(t, `1 of 2 transport pods have a registration stream open to 1 router ("router-0")`, res.Registered.Message)
	assert.Equal(t, []pylonv1alpha1.ServerStatus{
		{InferenceServerID: "spark-berlin.models.llama.pylon-llama-a", Pod: "pylon-llama-a"},
		{InferenceServerID: "spark-berlin.models.llama.pylon-llama-b", Pod: "pylon-llama-b", RegistrationStreams: 1, ReverseTunnels: 1},
	}, res.Servers)
	assert.Equal(t, 1.0, h.scrapeFailures(metrics.ScrapeFailureConnect))

	h.mu.Lock()
	logs := strings.Join(h.logs, "\n")
	h.mu.Unlock()
	assert.Contains(t, logs, `"level"=1`)
	assert.Contains(t, logs, "Scraping a transport pod failed; counting it as no streams and no tunnels")
	assert.Contains(t, logs, `"pod"="pylon-llama-a"`)
	assert.Contains(t, logs, `"reason"="connect"`)

	// Every pod unreachable: no stream, and the messages say why.
	h.setUnreachable("pylon-llama-b")
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)
	assert.Contains(t, res.Registered.Message, "(2 of 2 running pods could not be scraped on port 9089)")
	assert.Contains(t, res.TransportReady.Message, "(2 of 2 running pods could not be scraped on port 9089)")
	assert.Equal(t, 3.0, h.scrapeFailures(metrics.ScrapeFailureConnect))
}

func TestScrapeFailureReasonsAreCounted(t *testing.T) {
	h := newHarness(t)
	h.ready = 3
	h.addPod("pylon-llama-a", t0)
	h.addPod("pylon-llama-b", t0)
	noIP := transportPod(h.ep, "pylon-llama-c", t0)
	noIP.Status.PodIP = ""
	h.createPod(noIP)
	h.expose("pylon-llama-a", pylon{status: http.StatusServiceUnavailable})
	h.expose("pylon-llama-b", pylon{raw: "not the text format {"})

	res := h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonPending)
	assert.Contains(t, res.Registered.Message, "(3 of 3 running pods could not be scraped on port 9089)")
	assert.Equal(t, 1.0, h.scrapeFailures(metrics.ScrapeFailureStatus))
	assert.Equal(t, 1.0, h.scrapeFailures(metrics.ScrapeFailureParse))
	assert.Zero(t, h.scrapeFailures(metrics.ScrapeFailureConnect), "a pod without an IP is not scraped")
	assert.Zero(t, h.scrapeFailures(metrics.ScrapeFailureTimeout))
}

func TestUnscrapedPodKeepsItsBaseline(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Hour))
	c := Closure{Router: "router-0", Reason: ClosureUnauthenticated}
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{c: 2}})
	h.observe()

	h.expose("pylon-llama-a", pylon{status: http.StatusInternalServerError})
	h.advance(5 * time.Second)
	assert.Empty(t, h.observe().Rejected)

	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{c: 3}})
	h.advance(5 * time.Second)
	assert.Contains(t, h.observe().Rejected, "(1 new)", "compared with the last successful scrape")
}

func TestRegistrationFields(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{})
	res := h.observe()
	assert.Equal(t, pylonv1alpha1.RegistrationStatus{ClusterID: testCluster}, res.Registration, "never registered")

	h.now = t0.Add(1500 * time.Millisecond)
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}, tunnels: []string{"router-0"}})
	first := metav1.NewTime(t0.Add(time.Second))
	assert.Equal(t, &first, h.observe().Registration.LastRegisteredTime, "set when Registered becomes True, to the second")

	h.advance(LastRegisteredRefresh - time.Second)
	assert.Equal(t, &first, h.observe().Registration.LastRegisteredTime, "kept while fresh")

	h.advance(time.Second)
	refreshed := metav1.NewTime(h.now.Truncate(time.Second))
	assert.Equal(t, &refreshed, h.observe().Registration.LastRegisteredTime, "refreshed while True")

	h.advance(5 * time.Second)
	h.expose("pylon-llama-a", pylon{})
	res = h.observe()
	assertRegistered(t, res, metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)
	assert.Equal(t, pylonv1alpha1.RegistrationStatus{ClusterID: testCluster, LastRegisteredTime: &refreshed}, res.Registration,
		"kept while False")

	h.advance(5 * time.Second)
	h.expose("pylon-llama-a", pylon{streams: []string{"router-1"}})
	again := metav1.NewTime(h.now.Truncate(time.Second))
	assert.Equal(t, &again, h.observe().Registration.LastRegisteredTime, "set again when Registered becomes True again")
}

func TestServerID(t *testing.T) {
	ep := &pylonv1alpha1.InferenceEndpoint{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "models"}}
	assert.Equal(t, "spark-berlin.models.llama.pylon-llama-7c9d4-xk2p", ServerID(ep, testCluster, "pylon-llama-7c9d4-xk2p"))
}

func TestTimeToRegisteredObservedOnce(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{})
	h.observe()
	count, _ := h.timeToRegistered()
	assert.Zero(t, count)

	h.advance(30 * time.Second)
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}})
	h.observe()
	count, sum := h.timeToRegistered()
	assert.Equal(t, uint64(1), count)
	assert.InDelta(t, 150.0, sum, 1e-9, "from creation two minutes before t0")

	h.advance(time.Minute)
	h.observe()
	h.expose("pylon-llama-a", pylon{})
	h.observe()
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}})
	h.observe()
	count, _ = h.timeToRegistered()
	assert.Equal(t, uint64(1), count, "a flap is not a first registration")

	// An operator restart: the endpoint was registered before.
	restarted := New(h.client, h.metrics)
	restarted.Client, restarted.Now, restarted.MetricsURL, restarted.Started = h.observer.Client, h.observer.Now, h.observer.MetricsURL, h.now
	h.observer = restarted
	h.observe()
	count, _ = h.timeToRegistered()
	assert.Equal(t, uint64(1), count)
}

func TestForgetAndRecreate(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.advance(time.Second)
	h.expose("pylon-llama-a", pylon{})
	h.observe()
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{{Router: "r", Reason: ClosureUnauthenticated}: 1}})
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)

	// Forgotten: the rejection memory and baselines are gone.
	h.observer.Forget(types.NamespacedName{Namespace: testNamespace, Name: testName})
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)

	// Recreated under the same name: a new UID starts over as well.
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{{Router: "r", Reason: ClosureUnauthenticated}: 2}})
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRegistrationRejected)
	h.ep.UID = "uid-2"
	assertRegistered(t, h.observe(), metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonRouterUnreachable)
}

func TestGonePodsAreDropped(t *testing.T) {
	h := newHarness(t)
	p := h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{closures: map[Closure]float64{{Router: "r", Reason: ClosureUnauthenticated}: 1}})
	h.observe()
	key := types.NamespacedName{Namespace: testNamespace, Name: testName}
	h.observer.mu.Lock()
	assert.Len(t, h.observer.endpoints[key].baselines, 1)
	h.observer.mu.Unlock()

	require.NoError(t, h.client.Delete(context.Background(), p))
	res := h.observe()
	assert.Nil(t, res.Servers)
	h.observer.mu.Lock()
	assert.Empty(t, h.observer.endpoints[key].baselines)
	h.observer.mu.Unlock()
}

func TestListErrorFailsTheObservation(t *testing.T) {
	h := newHarness(t, func(b *fake.ClientBuilder) {
		b.WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return fmt.Errorf("cache not synced")
			},
		})
	})
	_, err := h.observer.Observe(h.ctx, h.ep, h.state(), testCluster)
	require.EqualError(t, err, "listing transport pods: cache not synced")
}

func TestDefaultPodLabelsWithoutTransportState(t *testing.T) {
	h := newHarness(t)
	h.addPod("pylon-llama-a", t0.Add(-time.Minute))
	h.expose("pylon-llama-a", pylon{streams: []string{"router-0"}})
	res, err := h.observer.Observe(h.ctx, h.ep, transport.State{}, testCluster)
	require.NoError(t, err)
	assertRegistered(t, res, metav1.ConditionTrue, pylonv1alpha1.RegisteredReasonRegisteredWithRouter)
	assertTransportReady(t, res, metav1.ConditionFalse, pylonv1alpha1.TransportReadyReasonTunnelNotConnected)
}

func TestIdle(t *testing.T) {
	assert.Equal(t, &pylonv1alpha1.RegistrationStatus{ClusterID: testCluster}, Idle(nil, testCluster))
	last := metav1.NewTime(t0)
	assert.Equal(t, &pylonv1alpha1.RegistrationStatus{ClusterID: testCluster, LastRegisteredTime: &last},
		Idle(&pylonv1alpha1.RegistrationStatus{ClusterID: "old", RoutersConnected: 2, LastRegisteredTime: &last}, testCluster))
}

func TestNextScrapeJitterBounds(t *testing.T) {
	const interval = 5 * time.Second
	o := &Observer{}
	assert.Zero(t, o.NextScrape(0))
	assert.Zero(t, o.NextScrape(-time.Second))

	o.Rand = func() float64 { return 0 }
	assert.Equal(t, 4500*time.Millisecond, o.NextScrape(interval))
	o.Rand = func() float64 { return 0.5 }
	assert.Equal(t, interval, o.NextScrape(interval))
	o.Rand = func() float64 { return 0.999999 }
	assert.Less(t, o.NextScrape(interval), 5500*time.Millisecond)

	o = New(nil, nil)
	var got []time.Duration
	for range 1000 {
		d := o.NextScrape(interval)
		require.GreaterOrEqual(t, d, 4500*time.Millisecond)
		require.Less(t, d, 5500*time.Millisecond)
		got = append(got, d)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	assert.Less(t, got[0], interval, "some requeues are earlier")
	assert.Greater(t, got[len(got)-1], interval, "some are later")
}

func TestDefaults(t *testing.T) {
	o := New(nil, nil)
	assert.Equal(t, DefaultTimeout, o.Timeout)
	assert.NotNil(t, o.Client)
	assert.WithinDuration(t, time.Now(), o.now(), time.Minute)
	assert.WithinDuration(t, time.Now(), (&Observer{}).now(), time.Minute)
	assert.Equal(t, "http://10.0.0.9:9089/metrics", o.MetricsURL(&corev1.Pod{Status: corev1.PodStatus{PodIP: "10.0.0.9"}}))
}
