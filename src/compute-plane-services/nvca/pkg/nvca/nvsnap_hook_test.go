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

package nvca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
)

// withFlagEnabled temporarily flips a feature flag on, returning a
// cleanup func. featureflag.CLIFlag.Set is the only exported way to
// mutate flags programmatically.
func withFlagEnabled(t *testing.T, flagKey string) func() {
	t.Helper()
	cli := &featureflag.CLIFlag{}
	if err := cli.Set("+" + flagKey); err != nil {
		t.Fatalf("enable %s: %v", flagKey, err)
	}
	return func() {
		_ = cli.Set("-" + flagKey)
	}
}

// nvsnapFunctionStateUnstructured builds an unstructured NvSnapFunctionState
// for fake-dynamic seeding.
func nvsnapFunctionStateUnstructured(name string, optOut bool, hash string, state nvsnapv1alpha1.NvSnapFunctionStateLocalCacheState) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
			"kind":       "NvSnapFunctionState",
			"metadata":   map[string]any{"name": name},
			"spec": map[string]any{
				"functionVersionID": name,
				"optOut":            optOut,
			},
			"status": map[string]any{
				"checkpointHash":  hash,
				"localCacheState": string(state),
			},
		},
	}
	return u
}

// newHookTestBackend wires a K8sComputeBackend with only the dynClient
// set — enough for stampNvSnapAnnotations, which doesn't read any other
// field. Seeds the fake dynamic client with the given NvSnapFunctionState
// objects.
func newHookTestBackend(t *testing.T, seed ...*unstructured.Unstructured) K8sComputeBackend {
	t.Helper()
	scheme := runtime.NewScheme()
	// Register the NvSnapFunctionState gvk so fakedynamic can list/get it.
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{
			Group:   nvsnapFunctionStateGVR.Group,
			Version: nvsnapFunctionStateGVR.Version,
			Kind:    "NvSnapFunctionState",
		},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{
			Group:   nvsnapFunctionStateGVR.Group,
			Version: nvsnapFunctionStateGVR.Version,
			Kind:    "NvSnapFunctionStateList",
		},
		&unstructured.UnstructuredList{},
	)
	listKinds := map[schema.GroupVersionResource]string{
		nvsnapFunctionStateGVR: "NvSnapFunctionStateList",
	}
	objs := make([]runtime.Object, 0, len(seed))
	for _, o := range seed {
		objs = append(objs, o)
	}
	dc := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	return K8sComputeBackend{dynClient: dc}
}

func newReq(fvID string) *nvcav2beta1.ICMSRequest {
	return &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionVersionID: fvID,
			},
		},
	}
}

func TestStampNoFlagDoesNothing(t *testing.T) {
	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))
	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped when feature flag is off")
	}
	if _, ok := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; ok {
		t.Error("checkpoint-on-warm should NOT be stamped when feature flag is off")
	}
}

func TestStampWarmHashStampsRestoreFrom(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))
	if got := pod.Annotations[NvSnapRestoreFromAnnotation]; got != "deadbeef" {
		t.Errorf("restore-from = %q, want deadbeef", got)
	}
	if got := pod.Annotations[NvSnapFunctionVersionIDAnnotation]; got != "fv-1" {
		t.Errorf("function-version-id = %q, want fv-1 (Hook B reads FV id from pod)", got)
	}
	// Regression for Greptile P2 on MR !1698: when the cache is
	// already Warm, the pod is being restored — NOT a candidate for
	// a fresh checkpoint. Stamping checkpoint-on-warm here would
	// cause N restored replicas to each POST a redundant
	// CreateCheckpoint to nvsnap-server, overwriting each other's hash.
	if _, ok := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; ok {
		t.Error("checkpoint-on-warm must NOT be stamped on a restore (cache Warm) — would cause redundant re-checkpoint by Hook B")
	}
}

func TestStampColdSkipsRestoreFromKeepsOnWarm(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateCold),
	)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))
	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped when cache state is Cold")
	}
	if got := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; got != "true" {
		t.Errorf("checkpoint-on-warm should still be stamped to drive Hook B, got %q", got)
	}
}

func TestStampOptOutDoesNothing(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", true /*optOut*/, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))
	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped when function-version is opted out")
	}
	if _, ok := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; ok {
		t.Error("checkpoint-on-warm should NOT be stamped when function-version is opted out")
	}
}

func TestStampMissingCFSCreatesItAndStampsCheckpointOnWarm(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	// No NvSnapFunctionState seeded — GET returns NotFound. This is the
	// first-touch path: function-version has never been checkpointed.
	// Hook A must materialize an empty CFS at state=Cold and stamp
	// the Hook B trigger so the reconciler picks the pod up and
	// captures the first checkpoint. Without this, no CFS would ever
	// exist (chicken-and-egg with Hook B).
	c := newHookTestBackend(t)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-never-seen"), logrus.NewEntry(logrus.New()))

	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped — no checkpoint exists yet")
	}
	if got := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; got != "true" {
		t.Errorf("checkpoint-on-warm = %q, want true (first-touch fix)", got)
	}
	if got := pod.Annotations[NvSnapFunctionVersionIDAnnotation]; got != "fv-never-seen" {
		t.Errorf("function-version-id = %q, want fv-never-seen", got)
	}

	// CFS should have been created with state=Cold.
	got, err := c.dynClient.Resource(nvsnapFunctionStateGVR).Get(context.Background(), "fv-never-seen", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("NvSnapFunctionState/fv-never-seen was NOT created: %v (chicken-and-egg regression)", err)
	}
	state, _, _ := unstructured.NestedString(got.Object, "status", "localCacheState")
	if state != string(nvsnapv1alpha1.LocalCacheStateCold) {
		t.Errorf("initial CFS state = %q, want Cold", state)
	}
	fv, _, _ := unstructured.NestedString(got.Object, "spec", "functionVersionID")
	if fv != "fv-never-seen" {
		t.Errorf("CFS spec.functionVersionID = %q, want fv-never-seen", fv)
	}
}

func TestStampEmptyFunctionVersionDoesNothing(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()
	c := newHookTestBackend(t)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq(""), logrus.NewEntry(logrus.New()))
	if len(pod.Annotations) != 0 {
		t.Errorf("no FV id should leave pod unchanged, got %v", pod.Annotations)
	}
}

func TestStampWarmFetchingNotYetReady(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()

	// Hash is set (NGC says it exists) but bytes are still in
	// flight to this cluster. Must not stamp restore-from yet —
	// stamping while Fetching means the webhook tries to resolve a
	// manifest that may not exist locally yet.
	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateFetching),
	)
	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))
	if _, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Error("restore-from should NOT be stamped when cache state is Fetching")
	}
	// checkpoint-on-warm IS stamped — this pod can still be a
	// fresh-capture target if the FV has none, or a re-checkpoint
	// target if NGC has a hash but we want to refresh.
	if got := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; got != "true" {
		t.Errorf("checkpoint-on-warm should still be stamped during Fetching, got %q", got)
	}
}

var _ = metav1.Now // silence unused import; left in scope for future tests that exercise CapturedAt

// nvsnapServerStub returns a fake nvsnap-server that replies with `code`
// to GetCheckpoint (GET /api/v1/checkpoints/{id}).
func nvsnapServerStub(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":"deadbeef","status":"Completed","hash":"deadbeef"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestStampWarm_ArtifactMissing_ResetsAndRecaptures (nvca#190): when CFS
// says Warm but nvsnap-server has no artifact for the hash (deleted out from
// under NVCA), Hook A must NOT stamp restore-from — it must reset CFS to
// Cold and stamp checkpoint-on-warm so Hook B re-captures.
func TestStampWarm_ArtifactMissing_ResetsAndRecaptures(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()
	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	c.nvsnapClient = nvsnap.NewClient(nvsnap.WithBaseURL(nvsnapServerStub(t, http.StatusNotFound)))

	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))

	if got, ok := pod.Annotations[NvSnapRestoreFromAnnotation]; ok {
		t.Errorf("restore-from must NOT be stamped for a deleted artifact, got %q", got)
	}
	if got := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; got != "true" {
		t.Errorf("checkpoint-on-warm should be stamped to re-capture, got %q", got)
	}
	cfs, err := c.dynClient.Resource(nvsnapFunctionStateGVR).Get(context.Background(), "fv-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	state, _, _ := unstructured.NestedString(cfs.Object, "status", "localCacheState")
	if state != string(nvsnapv1alpha1.LocalCacheStateCold) {
		t.Errorf("CFS localCacheState = %q, want Cold (reset)", state)
	}
}

// TestStampWarm_ArtifactCheckErrorFailsOpen: a transient nvsnap-server error
// (non-404) must NOT invalidate a Warm CFS — Hook A fails open and still
// stamps restore-from so valid restores aren't broken by a blip.
func TestStampWarm_ArtifactCheckErrorFailsOpen(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()
	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	c.nvsnapClient = nvsnap.NewClient(nvsnap.WithBaseURL(nvsnapServerStub(t, http.StatusInternalServerError)))

	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))

	if got := pod.Annotations[NvSnapRestoreFromAnnotation]; got != "deadbeef" {
		t.Errorf("fail-open: restore-from should still be stamped on a transient error, got %q", got)
	}
}

// TestStampWarm_ArtifactExists_StampsRestoreFrom: the verification passes
// (artifact present) → normal Warm fast path stamps restore-from.
func TestStampWarm_ArtifactExists_StampsRestoreFrom(t *testing.T) {
	defer withFlagEnabled(t, "NvSnapCheckpointRestore")()
	c := newHookTestBackend(t,
		nvsnapFunctionStateUnstructured("fv-1", false, "deadbeef", nvsnapv1alpha1.LocalCacheStateWarm),
	)
	c.nvsnapClient = nvsnap.NewClient(nvsnap.WithBaseURL(nvsnapServerStub(t, http.StatusOK)))

	pod := &corev1.Pod{}
	c.stampNvSnapAnnotations(context.Background(), pod, newReq("fv-1"), logrus.NewEntry(logrus.New()))

	if got := pod.Annotations[NvSnapRestoreFromAnnotation]; got != "deadbeef" {
		t.Errorf("restore-from = %q, want deadbeef (artifact exists)", got)
	}
	if _, ok := pod.Annotations[NvSnapCheckpointOnWarmAnnotation]; ok {
		t.Error("checkpoint-on-warm must NOT be stamped when restoring")
	}
}
