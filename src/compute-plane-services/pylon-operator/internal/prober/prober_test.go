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

package prober

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
)

const (
	testModel      = "meta/llama-3.1-8b-instruct"
	testHealthPath = "/v1/health/ready"
)

func endpoint() *pylonv1alpha1.InferenceEndpoint {
	return &pylonv1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "models"},
		Spec: pylonv1alpha1.InferenceEndpointSpec{
			ModelName:          testModel,
			InferenceAPIFormat: pylonv1alpha1.InferenceAPIFormat{Type: pylonv1alpha1.InferenceAPIFormatChat},
			Service:            pylonv1alpha1.ServiceReference{Name: "llama-nim", Port: 8000},
			Health:             pylonv1alpha1.HealthCheck{Path: testHealthPath},
		},
	}
}

func service(ports ...corev1.ServicePort) *corev1.Service {
	if len(ports) == 0 {
		ports = []corev1.ServicePort{{Name: "http", Port: 8000}}
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "llama-nim", Namespace: "models"},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func slice(portName *string, ready ...*bool) discoveryv1.EndpointSlice {
	s := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "llama-nim-abc", Namespace: "models"},
		Ports:      []discoveryv1.EndpointPort{{Name: portName, Port: ptr.To[int32](8000)}},
	}
	for i, r := range ready {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{fmt.Sprintf("10.0.0.%d", i+1)},
			Conditions: discoveryv1.EndpointConditions{Ready: r},
		})
	}
	return s
}

func readySlices() []discoveryv1.EndpointSlice {
	return []discoveryv1.EndpointSlice{slice(ptr.To("http"), ptr.To(true))}
}

// backend serves the health path with healthStatus and /v1/models with
// modelsStatus and modelsBody.
type backend struct {
	healthStatus int
	modelsStatus int
	modelsBody   string
	delay        time.Duration
}

func (b backend) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.delay > 0 {
			select {
			case <-time.After(b.delay):
			case <-r.Context().Done():
				return
			}
		}
		switch r.URL.Path {
		case testHealthPath:
			w.WriteHeader(b.healthStatus)
		case ModelsPath:
			w.WriteHeader(b.modelsStatus)
			_, _ = w.Write([]byte(b.modelsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProber(baseURL string, m *metrics.Metrics) *Prober {
	p := New(m)
	p.BaseURL = func(*pylonv1alpha1.InferenceEndpoint) string { return baseURL }
	return p
}

func modelListJSON(ids ...string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, fmt.Sprintf(`{"id":%q,"object":"model","owned_by":"system"}`, id))
	}
	return `{"object":"list","data":[` + strings.Join(quoted, ",") + `]}`
}

func TestProbeKubernetesPreconditions(t *testing.T) {
	tests := []struct {
		name        string
		svc         *corev1.Service
		slices      []discoveryv1.EndpointSlice
		wantReason  pylonv1alpha1.ReadyReason
		wantMessage string
	}{
		{
			name:        "service missing",
			wantReason:  pylonv1alpha1.ReadyReasonServiceNotFound,
			wantMessage: `Service "llama-nim" not found in namespace "models"`,
		},
		{
			name:        "service lacks the port",
			svc:         service(corev1.ServicePort{Name: "grpc", Port: 9000}),
			slices:      readySlices(),
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeFailed,
			wantMessage: `Service "llama-nim" does not expose port 8000`,
		},
		{
			name:        "no slices",
			svc:         service(),
			wantReason:  pylonv1alpha1.ReadyReasonNoReadyEndpoints,
			wantMessage: `Service "llama-nim" has no ready endpoints for port 8000`,
		},
		{
			name:       "only unready endpoints",
			svc:        service(),
			slices:     []discoveryv1.EndpointSlice{slice(ptr.To("http"), ptr.To(false), ptr.To(false))},
			wantReason: pylonv1alpha1.ReadyReasonNoReadyEndpoints,
		},
		{
			name:       "ready endpoints serve another port",
			svc:        service(),
			slices:     []discoveryv1.EndpointSlice{slice(ptr.To("metrics"), ptr.To(true))},
			wantReason: pylonv1alpha1.ReadyReasonNoReadyEndpoints,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := metrics.New()
			p := newProber("http://127.0.0.1:1", m)
			res := p.Probe(context.Background(), endpoint(), tt.svc, tt.slices)
			assert.Equal(t, metav1.ConditionFalse, res.Status)
			assert.Equal(t, tt.wantReason, res.Reason)
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, res.Message)
			}
			assert.Equal(t, 1.0, failures(t, m, tt.wantReason))
			assert.Equal(t, uint64(0), probes(t, m))
		})
	}
}

func TestProbeHealth(t *testing.T) {
	tests := []struct {
		name        string
		backend     backend
		wantStatus  metav1.ConditionStatus
		wantReason  pylonv1alpha1.ReadyReason
		wantMessage string
	}{
		{
			name:        "200 with the model listed",
			backend:     backend{healthStatus: 200, modelsStatus: 200, modelsBody: modelListJSON(testModel)},
			wantStatus:  metav1.ConditionTrue,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeSucceeded,
			wantMessage: testHealthPath + " returned HTTP 200",
		},
		{
			name:       "204 counts as 2xx",
			backend:    backend{healthStatus: 204, modelsStatus: 200, modelsBody: modelListJSON(testModel)},
			wantStatus: metav1.ConditionTrue,
			wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded,
		},
		{
			name:        "503",
			backend:     backend{healthStatus: 503},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeFailed,
			wantMessage: testHealthPath + " returned HTTP 503",
		},
		{
			name:        "399 is not 2xx",
			backend:     backend{healthStatus: 399},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeFailed,
			wantMessage: "returned HTTP 399",
		},
		{
			name:        "timeout",
			backend:     backend{healthStatus: 200, delay: 5 * time.Second},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  pylonv1alpha1.ReadyReasonHealthProbeFailed,
			wantMessage: "timed out after 50ms",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.backend.start(t)
			m := metrics.New()
			p := newProber(srv.URL, m)
			p.Timeout = 50 * time.Millisecond
			if tt.backend.delay == 0 {
				p.Timeout = 5 * time.Second
			}
			res := p.Probe(context.Background(), endpoint(), service(), readySlices())
			assert.Equal(t, tt.wantStatus, res.Status)
			assert.Equal(t, tt.wantReason, res.Reason)
			assert.Contains(t, res.Message, tt.wantMessage)
			assert.Equal(t, uint64(1), probes(t, m))
		})
	}
}

func TestProbeConnectionRefused(t *testing.T) {
	srv := backend{healthStatus: 200}.start(t)
	url := srv.URL
	srv.Close()
	res := newProber(url, nil).Probe(context.Background(), endpoint(), service(), readySlices())
	assert.Equal(t, metav1.ConditionFalse, res.Status)
	assert.Equal(t, pylonv1alpha1.ReadyReasonHealthProbeFailed, res.Reason)
	assert.Contains(t, res.Message, "failed:")
}

func TestProbeInvalidURL(t *testing.T) {
	res := newProber("http://[::1", nil).Probe(context.Background(), endpoint(), service(), readySlices())
	assert.Equal(t, pylonv1alpha1.ReadyReasonHealthProbeFailed, res.Reason)
	assert.Contains(t, res.Message, "failed:")
}

func TestProbeModelListing(t *testing.T) {
	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	tests := []struct {
		name        string
		status      int
		body        string
		delay       bool
		wantReason  pylonv1alpha1.ReadyReason
		wantMessage string
	}{
		{name: "model listed among others", status: 200, body: modelListJSON("other", testModel), wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{
			name: "model missing", status: 200, body: modelListJSON("other/model"),
			wantReason:  pylonv1alpha1.ReadyReasonModelNameMismatch,
			wantMessage: `backend does not serve model "meta/llama-3.1-8b-instruct"; GET /v1/models lists "other/model"`,
		},
		{
			name: "model missing from a long list", status: 200, body: modelListJSON(many...),
			wantReason:  pylonv1alpha1.ReadyReasonModelNameMismatch,
			wantMessage: `lists "a", "b", "c", "d", "e" and 2 more`,
		},
		{
			name: "empty list", status: 200, body: `{"object":"list","data":[]}`,
			wantReason:  pylonv1alpha1.ReadyReasonModelNameMismatch,
			wantMessage: "lists no models",
		},
		{
			name: "entries without ids are ignored", status: 200, body: `{"data":[{"object":"model"}]}`,
			wantReason:  pylonv1alpha1.ReadyReasonModelNameMismatch,
			wantMessage: "lists no models",
		},
		{name: "case differs", status: 200, body: modelListJSON(strings.ToUpper(testModel)), wantReason: pylonv1alpha1.ReadyReasonModelNameMismatch},
		{name: "listing not found", status: 404, body: "not found", wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing server error", status: 500, body: modelListJSON("other"), wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing is not JSON", status: 200, body: "<html>models</html>", wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing without data", status: 200, body: `{"models":["other"]}`, wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing with null data", status: 200, body: `{"data":null}`, wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing with wrong data type", status: 200, body: `{"data":"other"}`, wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
		{name: "listing times out", delay: true, wantReason: pylonv1alpha1.ReadyReasonHealthProbeSucceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == testHealthPath {
					w.WriteHeader(http.StatusOK)
					return
				}
				if tt.delay {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			p := newProber(srv.URL+"/", nil)
			p.Timeout = 200 * time.Millisecond
			res := p.Probe(context.Background(), endpoint(), service(), readySlices())
			assert.Equal(t, tt.wantReason, res.Reason, res.Message)
			if tt.wantReason == pylonv1alpha1.ReadyReasonHealthProbeSucceeded {
				assert.Equal(t, metav1.ConditionTrue, res.Status)
			} else {
				assert.Equal(t, metav1.ConditionFalse, res.Status)
			}
			assert.Contains(t, res.Message, tt.wantMessage)
		})
	}
}

func TestProbeCountsFailuresByReason(t *testing.T) {
	srv := backend{healthStatus: 200, modelsStatus: 200, modelsBody: modelListJSON("other")}.start(t)
	m := metrics.New()
	p := newProber(srv.URL, m)
	for range 3 {
		p.Probe(context.Background(), endpoint(), service(), readySlices())
	}
	assert.Equal(t, 3.0, failures(t, m, pylonv1alpha1.ReadyReasonModelNameMismatch))
	assert.Equal(t, 0.0, failures(t, m, pylonv1alpha1.ReadyReasonHealthProbeFailed))
	assert.Equal(t, uint64(3), probes(t, m))
}

func TestReadyEndpoints(t *testing.T) {
	port := corev1.ServicePort{Name: "http", Port: 8000}
	tests := []struct {
		name   string
		slices []discoveryv1.EndpointSlice
		want   int
	}{
		{name: "none", want: 0},
		{name: "ready and unready", slices: []discoveryv1.EndpointSlice{slice(ptr.To("http"), ptr.To(true), ptr.To(false))}, want: 1},
		{name: "unknown readiness counts as ready", slices: []discoveryv1.EndpointSlice{slice(ptr.To("http"), nil)}, want: 1},
		{name: "slice port with nil name matches", slices: []discoveryv1.EndpointSlice{slice(nil, ptr.To(true))}, want: 1},
		{name: "other port name", slices: []discoveryv1.EndpointSlice{slice(ptr.To("metrics"), ptr.To(true))}, want: 0},
		{
			name: "slice without ports matches",
			slices: []discoveryv1.EndpointSlice{{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
			}},
			want: 1,
		},
		{
			name: "several slices",
			slices: []discoveryv1.EndpointSlice{
				slice(ptr.To("http"), ptr.To(true)),
				slice(ptr.To("http"), ptr.To(true), ptr.To(true)),
			},
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReadyEndpoints(port, tt.slices))
		})
	}
}

func TestDefaults(t *testing.T) {
	p := New(nil)
	require.NotNil(t, p.Client)
	assert.Equal(t, DefaultTimeout, p.Timeout)
	assert.Equal(t, "http://llama-nim.models.svc:8000", p.BaseURL(endpoint()))
	assert.Equal(t, DefaultTimeout, (&Prober{}).timeout())
}

// gather returns the metric families of m by name.
func gather(t *testing.T, m *metrics.Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))
	families, err := reg.Gather()
	require.NoError(t, err)
	out := map[string]*dto.MetricFamily{}
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

func failures(t *testing.T, m *metrics.Metrics, reason pylonv1alpha1.ReadyReason) float64 {
	t.Helper()
	for _, metric := range gather(t, m)["nvcf_pylon_operator_probe_failures_total"].GetMetric() {
		for _, l := range metric.GetLabel() {
			if l.GetName() == "reason" && l.GetValue() == string(reason) {
				return metric.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("no probe failure series for reason %s", reason)
	return 0
}

func probes(t *testing.T, m *metrics.Metrics) uint64 {
	t.Helper()
	metric := gather(t, m)["nvcf_pylon_operator_probe_duration_seconds"].GetMetric()
	require.Len(t, metric, 1)
	return metric[0].GetHistogram().GetSampleCount()
}
