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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// StatusHandler resolves the namespace internally via configMapNamespace, so
// tests place the ConfigMap in that same namespace (env unset → defaultNamespace).
var testNS = defaultNamespace

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return scheme
}

func healthCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: controlPlaneHealthCM, Namespace: testNS},
		Data:       data,
	}
}

func TestStatusHandler(t *testing.T) {
	t.Parallel()

	const (
		healthyYAML   = "namespace: sis\nstatus: healthy\ntimestamp: 2026-06-28T14:30:00Z\n"
		unhealthyYAML = "namespace: cassandra-system\nstatus: unhealthy\ntimestamp: 2026-06-28T14:30:00Z\n"
	)

	tests := []struct {
		name       string
		objects    []client.Object
		getErr     bool
		wantStatus int
		want       []ControlPlaneComponentStatus
	}{
		{
			name:       "returns sorted statuses from configmap",
			objects:    []client.Object{healthCM(map[string]string{"sis-api": healthyYAML, "cassandra": unhealthyYAML})},
			wantStatus: http.StatusOK,
			want: []ControlPlaneComponentStatus{
				{ComponentName: "cassandra", Namespace: "cassandra-system", Status: "unhealthy", Timestamp: "2026-06-28T14:30:00Z"},
				{ComponentName: "sis-api", Namespace: "sis", Status: "healthy", Timestamp: "2026-06-28T14:30:00Z"},
			},
		},
		{
			name:       "empty configmap returns empty array",
			objects:    []client.Object{healthCM(map[string]string{})},
			wantStatus: http.StatusOK,
			want:       []ControlPlaneComponentStatus{},
		},
		{
			name:       "missing configmap returns 500",
			objects:    nil,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "malformed entry returns 500",
			objects:    []client.Object{healthCM(map[string]string{"sis-api": "timestamp: not-a-date\n"})},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "client get error returns 500",
			objects:    []client.Object{healthCM(map[string]string{"sis-api": healthyYAML})},
			getErr:     true,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			builder := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.objects...)
			if tt.getErr {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return context.DeadlineExceeded
					},
				})
			}

			h := StatusHandler(builder.Build())

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/control-plane", nil))

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}

			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var got []ControlPlaneComponentStatus
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statuses, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("status[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
