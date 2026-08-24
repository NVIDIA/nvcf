/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Tests for Hook A content-addressed lookup (nvnvsnap#59 NVCA side):
// the new lookupContentAddressedMatch + helper extractors. Stands up
// a fake nvsnap-server with httptest to exercise the full POST /lookup
// round-trip against a stub catalog.

package nvca

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// fakeLookupServer stands up an httptest.Server that replies to
// POST /api/v1/checkpoints/lookup with the configured matches.
// recordedRequest captures the last received body so tests can
// assert NVCA built the right query.
type fakeLookupServer struct {
	matches         []nvsnap.LookupMatch
	statusCode      int
	recordedRequest nvsnap.LookupRequest
	requestCount    int
	// artifactMissing makes the GetCheckpoint existence probe (the
	// Warm-path self-heal in stampNvSnapAnnotations) return 404. Default
	// false → the probe returns 200 ("artifact exists") so the fvID
	// fast path isn't misread as a deleted artifact.
	artifactMissing bool
}

func (f *fakeLookupServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/checkpoints/lookup" {
			// GET /api/v1/checkpoints/{id} — the Warm-path artifact
			// existence check. Serve 200 (exists) unless artifactMissing.
			if strings.HasPrefix(r.URL.Path, "/api/v1/checkpoints/") && !f.artifactMissing {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			http.NotFound(w, r)
			return
		}
		f.requestCount++
		_ = json.NewDecoder(r.Body).Decode(&f.recordedRequest)
		if f.statusCode != 0 {
			http.Error(w, "boom", f.statusCode)
			return
		}
		_ = json.NewEncoder(w).Encode(nvsnap.LookupResponse{Matches: f.matches})
	})
}

// withNvSnapClient wires a nvsnap.Client pointed at the test server into
// the K8sComputeBackend.
func (b K8sComputeBackend) withNvSnapClient(srv *httptest.Server) K8sComputeBackend {
	b.nvsnapClient = nvsnap.NewClient(nvsnap.WithBaseURL(srv.URL))
	return b
}

func inferencePodWithSpec(name, fvID, image, model string, args []string) *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "inference",
					Image: image,
					Env:   []corev1.EnvVar{{Name: "NIM_MODEL_NAME", Value: model}},
					Args:  args,
				},
			},
		},
	}
}

// Day 2 deploy scenario: fvID=Y is brand new (no CFS), but the
// canonical workload identity matches an existing capture under
// fvID=X. Hook A should query the lookup endpoint, find the X
// checkpoint, stamp restore-from with its hash, and skip cold-start.
func TestStampContentAddressedDedupAcrossFVIDs(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{matches: []nvsnap.LookupMatch{{
		Hash:           "85ec4d75ee57c1be444dd19733f63cfd",
		CheckpointID:   "85ec4d75__20260531-174604",
		CapturedOnNode: "node-1",
		ImageRef:       "nvcr.io/foo:1.2",
	}}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	// No CFS for fv-Y exists; createInitialCFS will run first.
	c := newHookTestBackend(t).withNvSnapClient(srv)
	pod := inferencePodWithSpec("p1", "fv-Y", "nvcr.io/foo:1.2", "meta/llama",
		[]string{"--port", "8000"})

	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-Y"), logrus.NewEntry(logrus.New()))

	if got := pod.Annotations[NvSnapRestoreFromAnnotation]; got != "85ec4d75ee57c1be444dd19733f63cfd" {
		t.Errorf("restore-from = %q, want lookup match hash", got)
	}
	// checkpoint-on-warm should NOT be stamped — we're restoring, not
	// triggering a fresh capture.
	if _, ok := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; ok {
		t.Error("checkpoint-on-warm should NOT be stamped on dedup hit")
	}
	// Server must have been queried with the canonical inputs.
	if fake.recordedRequest.ImageRef != "nvcr.io/foo:1.2" {
		t.Errorf("ImageRef sent = %q", fake.recordedRequest.ImageRef)
	}
	if fake.recordedRequest.ModelID != "meta/llama" {
		t.Errorf("ModelID sent = %q", fake.recordedRequest.ModelID)
	}
	// CFS_Y should now be persisted at Warm with the dedup'd hash so
	// the next admission for fv-Y takes the fvID-keyed fast path.
	got, err := c.dynClient.Resource(nvsnapFunctionStateGVR).
		Get(context.Background(), "fv-Y", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CFS_Y should exist after dedup: %v", err)
	}
	state, _, _ := unstructured.NestedString(got.Object, "status", "localCacheState")
	if state != string(nvsnapv1alpha1.LocalCacheStateWarm) {
		t.Errorf("CFS_Y state = %q, want Warm", state)
	}
	hash, _, _ := unstructured.NestedString(got.Object, "status", "checkpointHash")
	if hash != "85ec4d75ee57c1be444dd19733f63cfd" {
		t.Errorf("CFS_Y hash = %q, want lookup match", hash)
	}
	capturedHere, _, _ := unstructured.NestedBool(got.Object, "status", "capturedHere")
	if capturedHere {
		t.Error("capturedHere should be false — we didn't capture, we borrowed")
	}
}

// Lookup returns no matches → fall through to cold-start (Hook B
// stamps checkpoint-on-warm), no restore-from annotation.
func TestStampNoLookupMatchFallsThroughToCold(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{matches: nil} // empty matches
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newHookTestBackend(t).withNvSnapClient(srv)
	pod := inferencePodWithSpec("p1", "fv-cold", "nvcr.io/never-seen:1.0", "", nil)

	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-cold"), logrus.NewEntry(logrus.New()))

	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped when lookup has no match")
	}
	if pod.Annotations[NvSnapCheckpointOnWarmAnnotation] != "true" {
		t.Error("checkpoint-on-warm should be stamped on cold-start path")
	}
}

// Lookup endpoint errors → fail-open. Pod still gets checkpoint-on-warm
// stamped so it'll be captured by Hook B; NvSnap as an optimization is
// never allowed to block pod creation.
func TestStampLookupErrorFailsOpenToCold(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{statusCode: http.StatusInternalServerError}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newHookTestBackend(t).withNvSnapClient(srv)
	pod := inferencePodWithSpec("p1", "fv-err", "nvcr.io/foo:1.2", "meta/llama", nil)

	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-err"), logrus.NewEntry(logrus.New()))

	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped on lookup error")
	}
	if pod.Annotations[NvSnapCheckpointOnWarmAnnotation] != "true" {
		t.Error("checkpoint-on-warm should be stamped on fail-open path")
	}
}

// fvID-keyed fast path (CFS Warm + hash already set) must NOT
// query the lookup endpoint. Avoids redundant RPC + dedup races on
// the steady state.
func TestStampFvIDFastPathSkipsLookup(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{matches: []nvsnap.LookupMatch{{Hash: "wrong"}}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "fvid-keyed-hash", nvsnapv1alpha1.LocalCacheStateWarm),
	).withNvSnapClient(srv)
	pod := inferencePodWithSpec("p1", "fv-1", "nvcr.io/foo:1.2", "meta/llama", nil)

	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))

	if got := pod.Annotations[NvSnapRestoreFromAnnotation]; got != "fvid-keyed-hash" {
		t.Errorf("restore-from = %q, want fvID-keyed hash (not lookup result)", got)
	}
	if fake.requestCount != 0 {
		t.Errorf("lookup endpoint was called %d times; should be 0 on fvID fast path", fake.requestCount)
	}
}

// nil nvsnapClient (lookup unavailable): fall through to cold-start.
// Mirrors what happens if nvsnap.NewClient construction failed at
// K8sComputeBackend init.
func TestStampNilNvSnapClientFallsThrough(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	c := newHookTestBackend(t) // nvsnapClient stays nil
	pod := inferencePodWithSpec("p1", "fv-no-client", "nvcr.io/foo:1.2", "", nil)

	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-no-client"), logrus.NewEntry(logrus.New()))

	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped with no nvsnap client")
	}
	if pod.Annotations[NvSnapCheckpointOnWarmAnnotation] != "true" {
		t.Error("checkpoint-on-warm should still be stamped (cold-start path)")
	}
}

// Pod with no inference container → skip lookup gracefully. Some
// non-standard pod shapes might land here (test fixtures, custom
// workloads); we shouldn't crash on them.
func TestStampNoInferenceContainerSkipsLookup(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newHookTestBackend(t).withNvSnapClient(srv)
	pod := &corev1.Pod{} // no containers at all
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-bare"), logrus.NewEntry(logrus.New()))

	if fake.requestCount != 0 {
		t.Errorf("should not call lookup for pod with no containers; calls=%d", fake.requestCount)
	}
}

func TestInferenceContainer_Selection(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string // expected container name, "" means nil result
	}{
		{
			name: "named 'inference' wins",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "sidecar"},
				{Name: "inference"},
			}}},
			want: "inference",
		},
		{
			name: "falls back to first GPU-requesting container",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "cpu-only"},
				{Name: "gpu-worker", Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
				}},
			}}},
			want: "gpu-worker",
		},
		{
			name: "falls back to first container if no match",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "first"},
				{Name: "second"},
			}}},
			want: "first",
		},
		{
			name: "nil pod → nil",
			pod:  nil,
			want: "",
		},
		{
			name: "no containers → nil",
			pod:  &corev1.Pod{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferenceContainer(tc.pod)
			if tc.want == "" {
				if got != nil {
					t.Errorf("got %v, want nil", got.Name)
				}
				return
			}
			if got == nil || got.Name != tc.want {
				t.Errorf("got %v, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractModelID_HookA(t *testing.T) {
	cases := []struct {
		name string
		c    corev1.Container
		want string
	}{
		{"NIM env var", corev1.Container{Env: []corev1.EnvVar{{Name: "NIM_MODEL_NAME", Value: "meta/llama"}}}, "meta/llama"},
		{"HF env var", corev1.Container{Env: []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "TinyLlama"}}}, "TinyLlama"},
		{"--model arg", corev1.Container{Args: []string{"--port", "8000", "--model", "meta-llama/Llama"}}, "meta-llama/Llama"},
		{"--model= arg", corev1.Container{Args: []string{"--model=foo/bar"}}, "foo/bar"},
		{"--model-path", corev1.Container{Args: []string{"--model-path", "/models/local"}}, "/models/local"},
		{"--model-path=", corev1.Container{Args: []string{"--model-path=/m"}}, "/m"},
		{"env beats args", corev1.Container{
			Env:  []corev1.EnvVar{{Name: "MODEL_NAME", Value: "from-env"}},
			Args: []string{"--model", "from-args"},
		}, "from-env"},
		{"empty env falls through to args", corev1.Container{
			Env:  []corev1.EnvVar{{Name: "NIM_MODEL_NAME", Value: ""}},
			Args: []string{"--model", "from-args"},
		}, "from-args"},
		{"--model dangling", corev1.Container{Args: []string{"--model"}}, ""},
		{"nothing", corev1.Container{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractModelID(&tc.c); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalizeArgs_HookA(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"strips --model + sorts", []string{"--port", "8000", "--model", "x", "--tp", "1"}, []string{"--port", "--tp", "1", "8000"}},
		{"strips --model=", []string{"--model=x", "--port=8000"}, []string{"--port=8000"}},
		{"strips --model-path", []string{"--model-path", "/x", "--port", "8000"}, []string{"--port", "8000"}},
		{"sorts identical inputs identically", []string{"--b", "--a"}, []string{"--a", "--b"}},
		{"empty in → nil out", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalizeArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Sanity: the request body NVCA POSTs to /lookup contains exactly the
// canonical inputs in the JSON shape the server expects. Round-trip
// via the recorded request lets us catch silent field renames.
func TestLookupRequestShape(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	fake := &fakeLookupServer{matches: nil}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newHookTestBackend(t).withNvSnapClient(srv)
	pod := inferencePodWithSpec("p1", "fv-1",
		"nvcr.io/foo:1.2", "meta/llama",
		[]string{"--port", "8000", "--model", "ignored-in-args", "--tp", "1"})
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))

	if fake.recordedRequest.ImageRef != "nvcr.io/foo:1.2" {
		t.Errorf("ImageRef = %q", fake.recordedRequest.ImageRef)
	}
	if fake.recordedRequest.ModelID != "meta/llama" {
		t.Errorf("ModelID = %q", fake.recordedRequest.ModelID)
	}
	// canonicalized: --model stripped, rest sorted
	wantFlags := []string{"--port", "--tp", "1", "8000"}
	if !reflect.DeepEqual(fake.recordedRequest.EngineFlags, wantFlags) {
		t.Errorf("EngineFlags = %v, want %v", fake.recordedRequest.EngineFlags, wantFlags)
	}
	// DriverMajor stays 0 (we can't resolve at admission cheaply yet —
	// future: cache node labels at NVCA agent startup)
	if fake.recordedRequest.DriverMajor != 0 {
		t.Errorf("DriverMajor = %d, want 0 (unresolved at admission)", fake.recordedRequest.DriverMajor)
	}
}

// Smoke: the prepared request body deserializes as nvsnap.LookupRequest
// on the server side. Failure mode this catches: JSON tag mismatch
// between NVCA's view and nvsnap-server's view of the lookup schema.
func TestLookupRequest_JSONRoundTrip(t *testing.T) {
	src := nvsnap.LookupRequest{
		ImageRef:    "nvcr.io/foo:1.2",
		ModelID:     "meta/llama",
		EngineFlags: []string{"--port", "8000"},
		DriverMajor: 550,
		Limit:       5,
	}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"imageRef":"nvcr.io/foo:1.2"`) {
		t.Errorf("imageRef field missing or renamed; body=%s", string(b))
	}
	if !strings.Contains(string(b), `"engineFlags":["--port","8000"]`) {
		t.Errorf("engineFlags field missing or renamed; body=%s", string(b))
	}
	var dst nvsnap.LookupRequest
	if err := json.Unmarshal(b, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n src=%+v\n dst=%+v", src, dst)
	}
}
