// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLoadcomponents(t *testing.T) {
	t.Parallel()

	const validYAML = `
components:
  - name: web
    namespace: nvcf
    endpoints:
      - example.com
      - api.example.com:8080
  - name: db
    namespace: default
    k8s_workloads:
      - name: postgres
        type: Deployment
`

	tests := []struct {
		name    string
		yaml    string
		noFile  bool
		wantErr bool
		check   func(t *testing.T, m *Monitor)
	}{
		{
			name: "valid http and k8s components",
			yaml: validYAML,
			check: func(t *testing.T, m *Monitor) {
				if len(m.components) != 2 {
					t.Fatalf("got %d components, want 2", len(m.components))
				}
				http := m.components[0]
				if http.ComponentName != "web" || http.Namespace != "nvcf" || http.Status != healthStatusUnhealthy {
					t.Errorf("http component = %+v", http)
				}
				// Timestamp is seeded at load so a never-transitioning unhealthy
				// component reports a real time, not the zero value.
				if http.Timestamp.IsZero() {
					t.Errorf("http component Timestamp not seeded: got zero value")
				}
				if len(http.endpoints) != 2 || http.endpoints[0] != "http://example.com" {
					t.Errorf("endpoints not normalized with scheme: %v", http.endpoints)
				}
				k8s := m.components[1]
				if k8s.Namespace != "default" {
					t.Errorf("k8s component namespace = %q, want default", k8s.Namespace)
				}
				if len(k8s.workloads) != 1 {
					t.Fatalf("got %d workloads, want 1", len(k8s.workloads))
				}
				if _, ok := k8s.workloads[0].obj.(*deploymentWorkload); !ok {
					t.Errorf("workload obj = %T, want *deploymentWorkload", k8s.workloads[0].obj)
				}
			},
		},
		{name: "missing file", noFile: true, wantErr: true},
		{name: "invalid yaml", yaml: "components: [::", wantErr: true},
		{
			name:    "component missing name",
			yaml:    "components:\n  - endpoints: [example.com]\n",
			wantErr: true,
		},
		{
			name:    "component missing namespace",
			yaml:    "components:\n  - name: web\n    endpoints: [example.com]\n",
			wantErr: true,
		},
		{
			name:    "component with neither endpoints nor workloads",
			yaml:    "components:\n  - name: web\n    namespace: nvcf\n",
			wantErr: true,
		},
		{
			name: "component with both endpoints and workloads",
			yaml: "components:\n  - name: web\n    namespace: nvcf\n    endpoints: [example.com]\n    k8s_workloads:\n      - name: pg\n        type: deployment\n",
			check: func(t *testing.T, m *Monitor) {
				if len(m.components) != 1 {
					t.Fatalf("got %d components, want 1", len(m.components))
				}
				c := m.components[0]
				if len(c.endpoints) != 1 || len(c.workloads) != 1 {
					t.Errorf("want both endpoints and workloads populated, got endpoints=%v workloads=%d", c.endpoints, len(c.workloads))
				}
			},
		},
		{
			name:    "endpoint with scheme rejected",
			yaml:    "components:\n  - name: web\n    namespace: nvcf\n    endpoints: [http://example.com]\n",
			wantErr: true,
		},
		{
			name:    "empty endpoint rejected",
			yaml:    "components:\n  - name: web\n    namespace: nvcf\n    endpoints: ['']\n",
			wantErr: true,
		},
		{
			name:    "missing workload name",
			yaml:    "components:\n  - name: db\n    namespace: default\n    k8s_workloads:\n      - type: deployment\n",
			wantErr: true,
		},
		{
			name:    "invalid workload type",
			yaml:    "components:\n  - name: db\n    namespace: default\n    k8s_workloads:\n      - name: pg\n        type: cronjob\n",
			wantErr: true,
		},
		{
			name: "all workload types map to their wrappers",
			yaml: `
components:
  - name: mixed
    namespace: default
    k8s_workloads:
      - {name: dep, type: deployment}
      - {name: sts, type: statefulset}
      - {name: ds, type: daemonset}
      - {name: rs, type: replicaset}
`,
			check: func(t *testing.T, m *Monitor) {
				ws := m.components[0].workloads
				if len(ws) != 4 {
					t.Fatalf("got %d workloads, want 4", len(ws))
				}
				if _, ok := ws[0].obj.(*deploymentWorkload); !ok {
					t.Errorf("ws[0] obj = %T, want *deploymentWorkload", ws[0].obj)
				}
				if _, ok := ws[1].obj.(*statefulSetWorkload); !ok {
					t.Errorf("ws[1] obj = %T, want *statefulSetWorkload", ws[1].obj)
				}
				if _, ok := ws[2].obj.(*daemonSetWorkload); !ok {
					t.Errorf("ws[2] obj = %T, want *daemonSetWorkload", ws[2].obj)
				}
				if _, ok := ws[3].obj.(*replicaSetWorkload); !ok {
					t.Errorf("ws[3] obj = %T, want *replicaSetWorkload", ws[3].obj)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "components.yaml")
			if !tt.noFile {
				if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
					t.Fatalf("write temp file: %v", err)
				}
			}

			m := &Monitor{}
			err := m.Loadcomponents(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Loadcomponents err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && tt.check != nil {
				tt.check(t, m)
			}
		})
	}
}

func TestCheckComponentHealthHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		closed     bool // serve from an already-closed server -> unreachable
		malformed  bool // unparseable endpoint -> request cannot be built
		want       healthStatus
	}{
		{name: "2xx is healthy", statusCode: http.StatusOK, want: healthStatusHealthy},
		{name: "5xx is unhealthy", statusCode: http.StatusInternalServerError, want: healthStatusUnhealthy},
		{name: "unreachable is unhealthy", closed: true, want: healthStatusUnhealthy},
		{name: "malformed endpoint is unhealthy", malformed: true, want: healthStatusUnhealthy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := "://" // unparseable; only used by the malformed case
			if !tt.malformed {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.statusCode)
				}))
				if tt.closed {
					srv.Close()
				} else {
					t.Cleanup(srv.Close)
				}
				endpoint = srv.URL
			}

			m := &Monitor{
				httpClient: &http.Client{Timeout: 2 * time.Second},
				components: []componentHealth{
					{ComponentName: "c", Status: healthStatusUnhealthy, endpoints: []string{endpoint}},
				},
			}

			m.checkComponentHealth(context.Background(), 0)

			if got := m.components[0].Status; got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckComponentHealthWorkloads(t *testing.T) {
	t.Parallel()

	const ns, name = "default", "web"
	selLabels := map[string]string{"app": "web"}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr[int32](1), Selector: &metav1.LabelSelector{MatchLabels: selLabels}},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	readyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web-1", Labels: selLabels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Ready: true}}},
	}

	tests := []struct {
		name    string
		objects []client.Object // seeded cluster state; nil => deployment absent
		initial healthStatus
		want    healthStatus
	}{
		// drives the workloads branch and a healthy transition.
		{name: "healthy workload becomes healthy", objects: []client.Object{readyDeploy, readyPod}, initial: healthStatusUnhealthy, want: healthStatusHealthy},
		// drives the workloads branch and the unhealthy-transition error log.
		{name: "failing workload becomes unhealthy", objects: nil, initial: healthStatusHealthy, want: healthStatusUnhealthy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			m := &Monitor{
				k8sClient: cl,
				components: []componentHealth{{
					ComponentName: "c",
					Namespace:     ns,
					Status:        tt.initial,
					workloads:     []k8sWorkload{{Name: name, Type: "deployment", obj: &deploymentWorkload{}}},
				}},
			}

			m.checkComponentHealth(context.Background(), 0)

			if got := m.components[0].Status; got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckComponentHealthBoth covers a component that defines both endpoints
// and workloads: it is healthy only when both checks pass.
func TestCheckComponentHealthBoth(t *testing.T) {
	t.Parallel()

	const ns, name = "default", "web"
	selLabels := map[string]string{"app": "web"}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	readyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr[int32](1), Selector: &metav1.LabelSelector{MatchLabels: selLabels}},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	readyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web-1", Labels: selLabels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Ready: true}}},
	}

	tests := []struct {
		name           string
		endpointStatus int
		workloadReady  bool
		want           healthStatus
	}{
		{name: "both healthy", endpointStatus: http.StatusOK, workloadReady: true, want: healthStatusHealthy},
		{name: "endpoint healthy but workload missing", endpointStatus: http.StatusOK, workloadReady: false, want: healthStatusUnhealthy},
		{name: "workload healthy but endpoint failing", endpointStatus: http.StatusInternalServerError, workloadReady: true, want: healthStatusUnhealthy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.endpointStatus)
			}))
			t.Cleanup(srv.Close)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.workloadReady {
				builder = builder.WithObjects(readyDeploy, readyPod)
			}

			m := &Monitor{
				k8sClient:  builder.Build(),
				httpClient: &http.Client{Timeout: 2 * time.Second},
				components: []componentHealth{{
					ComponentName: "c",
					Namespace:     ns,
					Status:        healthStatusUnhealthy,
					endpoints:     []string{srv.URL},
					workloads:     []k8sWorkload{{Name: name, Type: "deployment", obj: &deploymentWorkload{}}},
				}},
			}

			m.checkComponentHealth(context.Background(), 0)

			if got := m.components[0].Status; got != tt.want {
				t.Errorf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSaveStatusToConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	tests := []struct {
		name       string
		components []componentHealth
		wantKeys   []string
	}{
		{
			name:     "no components writes empty configmap",
			wantKeys: nil,
		},
		{
			name: "multiple components keyed by name",
			components: []componentHealth{
				{ComponentName: "a", Namespace: "ns-a", Status: healthStatusHealthy, Timestamp: time.Unix(0, 0).UTC()},
				{ComponentName: "b", Namespace: "ns-b", Status: healthStatusUnhealthy},
			},
			wantKeys: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
			m := &Monitor{k8sClient: cl, components: tt.components}

			if err := m.saveStatusToConfigMap(context.Background(), "default", "status"); err != nil {
				t.Fatalf("saveStatusToConfigMap: %v", err)
			}

			var cm corev1.ConfigMap
			key := client.ObjectKey{Namespace: "default", Name: "status"}
			if err := cl.Get(context.Background(), key, &cm); err != nil {
				t.Fatalf("get configmap: %v", err)
			}

			if len(cm.Data) != len(tt.wantKeys) {
				t.Fatalf("configmap has %d keys, want %d (%v)", len(cm.Data), len(tt.wantKeys), cm.Data)
			}
			for _, k := range tt.wantKeys {
				if _, ok := cm.Data[k]; !ok {
					t.Errorf("missing key %q in configmap data", k)
				}
			}
			// status value is YAML-marshalled; confirm it round-trips the status and namespace.
			for _, c := range tt.components {
				if !strings.Contains(cm.Data[c.ComponentName], string(c.Status)) {
					t.Errorf("data[%q] = %q, want it to contain %q", c.ComponentName, cm.Data[c.ComponentName], c.Status)
				}
				if !strings.Contains(cm.Data[c.ComponentName], c.Namespace) {
					t.Errorf("data[%q] = %q, want it to contain namespace %q", c.ComponentName, cm.Data[c.ComponentName], c.Namespace)
				}
			}
		})
	}
}

func TestSeedFromConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	// healthySince is when the component last transitioned; seeding must preserve
	// it rather than resetting to the restart time.
	healthySince := time.Unix(1_600_000_000, 0).UTC()

	marshalEntry := func(t *testing.T, e statusEntry) string {
		t.Helper()
		out, err := yaml.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		return string(out)
	}

	t.Run("restores status and timestamp for matching components", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: controlPlaneHealthCM},
			Data: map[string]string{
				"a": marshalEntry(t, statusEntry{Namespace: "ns-a", Status: healthStatusHealthy, Timestamp: healthySince}),
				// stale entry for a component no longer configured — must be ignored.
				"gone": marshalEntry(t, statusEntry{Namespace: "ns-x", Status: healthStatusHealthy, Timestamp: healthySince}),
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		// Fresh post-restart state: everything unhealthy, stamped "now".
		now := time.Now().UTC()
		m := &Monitor{
			k8sClient: cl,
			components: []componentHealth{
				{ComponentName: "a", Namespace: "ns-a", Status: healthStatusUnhealthy, Timestamp: now},
				{ComponentName: "b", Namespace: "ns-b", Status: healthStatusUnhealthy, Timestamp: now},
			},
		}

		if err := m.seedFromConfigMap(context.Background(), "default", controlPlaneHealthCM); err != nil {
			t.Fatalf("seedFromConfigMap: %v", err)
		}

		if m.components[0].Status != healthStatusHealthy {
			t.Errorf("component a status = %q, want %q", m.components[0].Status, healthStatusHealthy)
		}
		if !m.components[0].Timestamp.Equal(healthySince) {
			t.Errorf("component a timestamp = %v, want restored %v", m.components[0].Timestamp, healthySince)
		}
		// b has no ConfigMap entry — it keeps its fresh default.
		if m.components[1].Status != healthStatusUnhealthy || !m.components[1].Timestamp.Equal(now) {
			t.Errorf("component b = %+v, want unchanged default", m.components[1])
		}
	})

	t.Run("missing configmap is not an error", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		now := time.Now().UTC()
		m := &Monitor{
			k8sClient: cl,
			components: []componentHealth{
				{ComponentName: "a", Status: healthStatusUnhealthy, Timestamp: now},
			},
		}

		if err := m.seedFromConfigMap(context.Background(), "default", controlPlaneHealthCM); err != nil {
			t.Fatalf("seedFromConfigMap on missing configmap: %v", err)
		}
		if m.components[0].Status != healthStatusUnhealthy || !m.components[0].Timestamp.Equal(now) {
			t.Errorf("component a = %+v, want unchanged default", m.components[0])
		}
	})

	t.Run("malformed entry falls back to default", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: controlPlaneHealthCM},
			Data:       map[string]string{"a": "not: [valid: yaml"},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
		now := time.Now().UTC()
		m := &Monitor{
			k8sClient: cl,
			components: []componentHealth{
				{ComponentName: "a", Status: healthStatusUnhealthy, Timestamp: now},
			},
		}

		if err := m.seedFromConfigMap(context.Background(), "default", controlPlaneHealthCM); err != nil {
			t.Fatalf("seedFromConfigMap: %v", err)
		}
		if m.components[0].Status != healthStatusUnhealthy || !m.components[0].Timestamp.Equal(now) {
			t.Errorf("component a = %+v, want unchanged default after malformed entry", m.components[0])
		}
	})
}

func TestNew(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().Build()
	m := New(cl)

	if m == nil {
		t.Fatal("New returned nil")
	}
	if m.k8sClient != cl {
		t.Error("k8sClient is not the provided client")
	}
	if m.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
	if m.httpClient.Timeout != 20*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 20s", m.httpClient.Timeout)
	}
	if len(m.components) != 0 {
		t.Errorf("components = %d, want 0 before Loadcomponents", len(m.components))
	}
}

func TestRunHealthChecks(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	tests := []struct {
		name       string
		statusCode int
		want       healthStatus
	}{
		{name: "healthy endpoint is persisted healthy", statusCode: http.StatusOK, want: healthStatusHealthy},
		{name: "failing endpoint is persisted unhealthy", statusCode: http.StatusInternalServerError, want: healthStatusUnhealthy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			t.Cleanup(srv.Close)

			// Shorten the otherwise 20s cycle so the loop ticks promptly.
			orig := healthCheckInterval
			healthCheckInterval = time.Millisecond
			t.Cleanup(func() { healthCheckInterval = orig })

			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
			m := &Monitor{
				k8sClient:  cl,
				httpClient: &http.Client{Timeout: 2 * time.Second},
				components: []componentHealth{
					{ComponentName: "web", Status: healthStatusUnhealthy, endpoints: []string{srv.URL}},
				},
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				m.RunHealthChecks(ctx)
				close(done)
			}()

			got := pollPersistedStatus(t, cl, "web", tt.want)
			cancel()

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("RunHealthChecks did not return after context cancel")
			}

			if got != tt.want {
				t.Errorf("persisted status = %q, want %q", got, tt.want)
			}
		})
	}
}

// pollPersistedStatus waits for RunHealthChecks to write want into the status
// ConfigMap for the named component, returning the observed status (or failing).
func pollPersistedStatus(t *testing.T, cl client.Client, component string, want healthStatus) healthStatus {
	t.Helper()
	for range 200 {
		var cm corev1.ConfigMap
		if err := cl.Get(context.Background(), client.ObjectKey{Namespace: defaultNamespace, Name: controlPlaneHealthCM}, &cm); err == nil {
			var se statusEntry
			if err := yaml.Unmarshal([]byte(cm.Data[component]), &se); err == nil && se.Status == want {
				return se.Status
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status %q for %q not persisted within timeout", want, component)
	return ""
}
