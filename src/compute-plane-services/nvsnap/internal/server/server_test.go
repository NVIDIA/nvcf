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

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func newTestServer(objects ...runtime.Object) *Server {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	kubeClient := kubefake.NewSimpleClientset(objects...)
	dynClient := fake.NewSimpleDynamicClient(scheme)

	log := logrus.New().WithField("component", "server-test")
	s := &Server{
		config:     Config{Address: ":0", AgentPort: 8081},
		kubeClient: kubeClient,
		dynClient:  dynClient,
		httpClient: http.DefaultClient,
		log:        log,
		demo:       newDemoSession(),
		hub:        newHub(log),
		obsCache:   &observabilityCache{},
	}
	s.setupRoutes()
	return s
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", http.NoBody)
	rr := httptest.NewRecorder()

	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "healthy" {
		t.Fatalf("expected healthy, got %s", resp["status"])
	}
}

func TestListNodesEmpty(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", http.NoBody)
	rr := httptest.NewRecorder()

	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["count"].(float64) != 0 {
		t.Fatalf("expected 0 nodes, got %v", resp["count"])
	}
}

func TestListPodsFiltersGPU(t *testing.T) {
	gpuPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "nvidia/cuda:12.0",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
					},
				},
			}},
		},
	}
	cpuPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "cpu-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "nginx:latest",
			}},
		},
	}

	s := newTestServer(gpuPod, cpuPod)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pods", http.NoBody)
	rr := httptest.NewRecorder()

	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	count := resp["count"].(float64)
	if count != 1 {
		t.Fatalf("expected 1 GPU pod, got %v", count)
	}
}

func TestCreateCheckpointRequiresFields(t *testing.T) {
	s := newTestServer()

	// Missing podName
	body := `{"namespace":"default"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	// Missing namespace
	body = `{"podName":"test"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints", strings.NewReader(body))
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateCheckpointPodNotFound(t *testing.T) {
	s := newTestServer()

	body := `{"podName":"nonexistent","namespace":"default"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCORSHeaders(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/health", http.NoBody)
	rr := httptest.NewRecorder()

	// Use Handler() which includes the CORS wrapper
	s.Handler().ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS header")
	}
}

// flattenCRD must emit BOTH "checkpointHash" (the K8s-idiomatic status
// field) AND "hash" (the API field NVCA's decoder reads). The two
// services drifted on the wire field name; NVCA decoded the
// response into a struct tagged `json:"hash"` and got empty, then
// refused to mark the function Warm and re-fired the checkpoint
// forever. Regression test for nvsnap#84 (GCP-H100-a 2026-06-02
// retry storm). Until the two repos share a contract type, both
// names must be emitted.
func TestFlattenCRD_EmitsHashAlias(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "0-sr-test",
				"namespace":         "nvcf-backend",
				"creationTimestamp": nil,
			},
			"spec": map[string]interface{}{"podName": "p1"},
			"status": map[string]interface{}{
				"phase":          "Completed",
				"checkpointHash": "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b",
			},
		},
	}
	out := flattenCRD(obj)
	if out["hash"] != "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b" {
		t.Errorf("hash alias missing or wrong; got %q", out["hash"])
	}
	if out["checkpointHash"] != "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b" {
		t.Errorf("original checkpointHash got dropped; got %q", out["checkpointHash"])
	}
}

// Empty checkpointHash should NOT emit a "hash":"" alias — that
// would tell NVCA the checkpoint exists with no hash, which is
// exactly the failure mode we're trying to prevent.
func TestFlattenCRD_NoHashAliasWhenEmpty(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "0-sr-test",
				"namespace":         "nvcf-backend",
				"creationTimestamp": nil,
			},
			"spec":   map[string]interface{}{"podName": "p1"},
			"status": map[string]interface{}{"phase": "InProgress"},
		},
	}
	out := flattenCRD(obj)
	if _, ok := out["hash"]; ok {
		t.Errorf("hash alias should be absent when checkpointHash is unset; got %v", out["hash"])
	}
}

// TestIsRootfsRedirect covers the agent's 422 redirect body parser.
// When the agent's BackendRedirectError fires (Riva/Triton hard-required,
// or v0.0.48+ RootfsIsDefault), the body contains a top-level
// "redirect":"rootfs" field plus other diagnostics. runCheckpoint uses
// this to decide between "fail the catalog row" and "hand off to
// runRootfsCheckpoint".
func TestIsRootfsRedirect(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"rootfs redirect (riva)", `{"backend":"riva","reason":"backend riva requires rootfs capture path","redirect":"rootfs"}`, true},
		{"rootfs redirect (default)", `{"backend":"unknown","reason":"global default","redirect":"rootfs"}`, true},
		{"rootfs uppercase", `{"redirect":"ROOTFS"}`, true},
		{"empty body", ``, false},
		{"plain error string (not JSON)", `something bad happened`, false},
		{"different redirect target", `{"redirect":"L1"}`, false},
		{"no redirect field", `{"backend":"vllm","reason":"some other failure"}`, false},
		{"redirect null", `{"redirect":null}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRootfsRedirect([]byte(tc.body)); got != tc.want {
				t.Errorf("isRootfsRedirect(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
