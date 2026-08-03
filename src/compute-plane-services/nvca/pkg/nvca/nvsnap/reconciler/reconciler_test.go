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

package reconciler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// newFakeDynamic returns a fake dynamic client wired for the
// NvSnapFunctionState resource. The unstructured scheme registration
// must match what fakedynamic.NewSimpleDynamicClientWithCustomListKinds
// expects: GVR → list-kind name.
func newFakeDynamic(seed ...*unstructured.Unstructured) *fakedynamic.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   CFSResource.Group,
		Version: CFSResource.Version,
		Kind:    "NvSnapFunctionState",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   CFSResource.Group,
		Version: CFSResource.Version,
		Kind:    "NvSnapFunctionStateList",
	}, &unstructured.UnstructuredList{})
	objs := make([]runtime.Object, 0, len(seed))
	for _, o := range seed {
		objs = append(objs, o)
	}
	return fakedynamic.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{CFSResource: "NvSnapFunctionStateList"},
		objs...,
	)
}

// inferencePod builds a pod with the annotations Hook A would have
// stamped, the inference container, and PodReady=True (which the
// reconciler now trusts as the workload-warm signal).
func inferencePod(podName, namespace, fvID string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Annotations: map[string]string{
				CheckpointOnWarmAnnotation:  "true",
				FunctionVersionIDAnnotation: fvID,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "inference"},
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// nvsnapServerStub simulates nvsnap-server's POST/GET /api/v1/checkpoints.
// First POST returns InProgress; subsequent GETs return Completed
// after pollsBeforeCompleted polls.
type nvsnapServerStub struct {
	pollsBeforeCompleted int32
	polls                int32
	hash                 string
	size                 int64
	failAt               string // empty | "create" | "status" | "pvc-state"
	statusCode           int    // optional override (defaults to 500 when failAt=="status")

	// nvca#179: L2 /pvc-state simulation.
	promotePolls               int32
	promotePollsBeforeTerminal int32  // 0 = first poll is terminal
	promoteTerminalState       string // "ready" | "failed" | "" (default ready)
	promote404                 bool   // if true, /pvc-state returns 404 (no-L2 case)
}

func (s *nvsnapServerStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// nvca#179: /pvc-state must be matched BEFORE the catchall
		// status handler — the path also contains "/api/v1/checkpoints/".
		isPVCState := r.Method == http.MethodGet &&
			strings.Contains(r.URL.Path, "/api/v1/checkpoints/by-hash/") &&
			strings.HasSuffix(r.URL.Path, "/pvc-state")
		switch {
		case isPVCState:
			if s.failAt == "pvc-state" {
				code := s.statusCode
				if code == 0 {
					code = http.StatusInternalServerError
				}
				http.Error(w, "boom", code)
				return
			}
			if s.promote404 {
				http.NotFound(w, r)
				return
			}
			n := atomic.AddInt32(&s.promotePolls, 1)
			state := "pending"
			if n > s.promotePollsBeforeTerminal {
				state = s.promoteTerminalState
				if state == "" {
					state = "ready"
				}
			}
			body := struct {
				Hash    string `json:"hash"`
				State   string `json:"state"`
				PVCName string `json:"pvc_name,omitempty"`
			}{Hash: s.hash, State: state}
			if state == "ready" {
				// Mirror agent naming: rox-<first 8 hex>; guard for
				// short test hashes so the stub doesn't panic.
				short := s.hash
				if len(short) > 8 {
					short = short[:8]
				}
				body.PVCName = "rox-" + short
			}
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v1/checkpoints"):
			if s.failAt == "create" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(nvsnap.Checkpoint{ID: "ck-1", Phase: nvsnap.PhaseInProgress})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/v1/checkpoints/"):
			if s.failAt == "status" {
				code := s.statusCode
				if code == 0 {
					code = http.StatusInternalServerError
				}
				http.Error(w, "boom", code)
				return
			}
			n := atomic.AddInt32(&s.polls, 1)
			c := nvsnap.Checkpoint{ID: "ck-1", Phase: nvsnap.PhaseInProgress}
			if n >= s.pollsBeforeCompleted {
				c.Phase = nvsnap.PhaseCompleted
				c.Hash = s.hash
				c.Size = s.size
			}
			_ = json.NewEncoder(w).Encode(c)
		default:
			http.NotFound(w, r)
		}
	})
}

func newTestReconciler(t *testing.T, pod *corev1.Pod, srv *httptest.Server, dyn *fakedynamic.FakeDynamicClient) *Reconciler {
	t.Helper()
	return &Reconciler{
		KubeClient:   fake.NewSimpleClientset(pod),
		DynClient:    dyn,
		NvSnapClient: nvsnap.NewClient(nvsnap.WithBaseURL(srv.URL)),
		// applyDefaults treats 0 as "unset"; use a tiny non-zero value
		// so the test doesn't wait the production default (10s).
		WarmupBuffer:           1 * time.Microsecond,
		CheckpointPollInterval: 10 * time.Millisecond,
		CheckpointTimeout:      2 * time.Second,
		// nvca#179 L2 promote-state poll. Tests use tiny intervals
		// so they don't wait the production default (15 min). The
		// happy-path stub returns "ready" on first poll, so 100 ms
		// timeout is plenty.
		PromotePollInterval: 10 * time.Millisecond,
		PromotePollTimeout:  500 * time.Millisecond,
		Log:                 logrus.NewEntry(logrus.New()),
	}
}

func TestReconcileHappyPath(t *testing.T) {
	// Stand up the inference /health server and override the pod's
	// PodIP+port to point at it.
	pod := inferencePod("p1", "ns1", "fv-1")

	stub := &nvsnapServerStub{pollsBeforeCompleted: 2, hash: "abcdef", size: 12345}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic() // CFS doesn't exist yet — reconciler creates it
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// CFS should be created and status filled.
	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.CheckpointHash != "abcdef" {
		t.Errorf("hash = %q, want abcdef", s.CheckpointHash)
	}
	if !s.CapturedHere {
		t.Error("CapturedHere should be true after a self-driven capture")
	}
	if s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = %q, want Warm", s.LocalCacheState)
	}
	if s.AttemptCount != 0 {
		t.Errorf("AttemptCount = %d, want 0 (reset on success)", s.AttemptCount)
	}

	// Annotation must be removed so a re-reconcile is a no-op.
	updatedPod, _ := r.KubeClient.CoreV1().Pods("ns1").Get(context.Background(), "p1", metav1.GetOptions{})
	if _, present := updatedPod.Annotations[CheckpointOnWarmAnnotation]; present {
		t.Error("nvsnap.io/checkpoint-on-warm annotation should be removed after success")
	}
}

// Greptile P2 on nvca!1748: pollCheckpointTerminal used to silently
// retry every error (including a permanent 404) until the full
// CheckpointTimeout (30 min default). A nvsnap-server that lost the
// row, or a retention sweep that deleted the in-flight checkpoint,
// would spin instead of failing fast. The fix routes 4xx (non-429)
// errors through isNonTransientAPIError → recordFailure.
func TestPollCheckpointTerminal_BreaksOn404(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-poll404")
	stub := &nvsnapServerStub{failAt: "status", statusCode: http.StatusNotFound}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)
	// Set a long timeout — the test should NOT take that long.
	// If pollCheckpointTerminal spins on the 404, the test times out
	// at the t.Deadline; if it breaks fast, we finish in milliseconds.
	r.CheckpointTimeout = 30 * time.Second

	start := time.Now()
	err := r.Reconcile(context.Background(), pod)
	elapsed := time.Since(start)

	// Reconcile MUST return nil (no controller-runtime requeue). The
	// failure detail lives on CFS.status.lastError.
	if err != nil {
		t.Fatalf("Reconcile must return nil (no requeue on swallowed failure); got %v", err)
	}
	got, gerr := dyn.Resource(CFSResource).Get(context.Background(), "fv-poll404", metav1.GetOptions{})
	if gerr != nil {
		t.Fatalf("get CFS: %v", gerr)
	}
	lastErr, _, _ := unstructured.NestedString(got.Object, "status", "lastError")
	if !strings.Contains(lastErr, "non-transient") {
		t.Errorf("CFS.status.lastError should mention non-transient; got: %q", lastErr)
	}
	// Must fail fast, not wait for CheckpointTimeout — fast-fail on
	// 404 is the behavior under test; the error-swallowing fix didn't
	// change that.
	if elapsed > 2*time.Second {
		t.Errorf("took %v — should fast-fail well under CheckpointTimeout=30s", elapsed)
	}
}

// 5xx errors are transient and SHOULD keep polling. Confirms we
// didn't over-eagerly mark every error as non-transient.
func TestPollCheckpointTerminal_RetriesOn5xx(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-poll5xx")
	stub := &nvsnapServerStub{failAt: "status", statusCode: http.StatusBadGateway}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)
	// Tight deadline — we expect this to spin and time out (proving
	// 5xx is treated as transient, not fast-failed like 404).
	r.CheckpointTimeout = 100 * time.Millisecond

	// Reconcile MUST return nil (no requeue). Behavior under test
	// (transient retry until timeout) is reflected in CFS.lastError.
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must return nil (no requeue); got %v", err)
	}
	got, gerr := dyn.Resource(CFSResource).Get(context.Background(), "fv-poll5xx", metav1.GetOptions{})
	if gerr != nil {
		t.Fatalf("get CFS: %v", gerr)
	}
	lastErr, _, _ := unstructured.NestedString(got.Object, "status", "lastError")
	// Should be a context-deadline-style error (transient retry path),
	// not a non-transient fast-fail.
	if strings.Contains(lastErr, "non-transient") {
		t.Errorf("5xx should NOT be marked non-transient in CFS.lastError; got: %q", lastErr)
	}
	if !strings.Contains(lastErr, "context deadline") {
		t.Errorf("CFS.lastError should mention context deadline (transient retry hit timeout); got: %q", lastErr)
	}
}

// 429 (Too Many Requests) is transient — server is asking us to back
// off, not telling us the request is permanently rejected.
func TestPollCheckpointTerminal_RetriesOn429(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-poll429")
	stub := &nvsnapServerStub{failAt: "status", statusCode: http.StatusTooManyRequests}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)
	r.CheckpointTimeout = 100 * time.Millisecond

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must return nil (no requeue); got %v", err)
	}
	got, gerr := dyn.Resource(CFSResource).Get(context.Background(), "fv-poll429", metav1.GetOptions{})
	if gerr != nil {
		t.Fatalf("get CFS: %v", gerr)
	}
	lastErr, _, _ := unstructured.NestedString(got.Object, "status", "lastError")
	if strings.Contains(lastErr, "non-transient") {
		t.Errorf("429 should NOT be marked non-transient in CFS.lastError; got: %q", lastErr)
	}
	if !strings.Contains(lastErr, "context deadline") {
		t.Errorf("CFS.lastError should mention context deadline (transient retry hit timeout); got: %q", lastErr)
	}
}

// nvca#14 + nvca#15: nvsnap-server returning Completed + empty hash is
// a control-plane bug (likely agent hash-propagation, nvnvsnap#61). The
// reconciler must refuse to mark Warm — that defense in depth stays —
// but it must also break the retry storm: returning an error here
// re-queues, which on nvsnap-h100-a (2026-05-31) caused 3 back-to-back
// 80 GB captures before manual intervention. New contract:
//
//   - return nil (no requeue)
//   - CFS state untouched (no Warm transition, no Failed flag-flip)
//   - increment nvca_nvsnap_capture_protocol_error_total{reason="empty_hash"}
//     so the operator sees an alertable signal
func TestReconcileSwallowsEmptyHashOnCompleted(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-empty-hash")

	// Stub returns Completed terminal phase but with an empty hash.
	stub := &nvsnapServerStub{pollsBeforeCompleted: 1, hash: "", size: 12345}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	// Snapshot the counter before so we can assert the increment is
	// attributable to this Reconcile call.
	before := testutil.ToFloat64(captureProtocolErrors.WithLabelValues("empty_hash"))

	err := r.Reconcile(context.Background(), pod)
	if err != nil {
		t.Fatalf("Reconcile should swallow empty-hash (return nil) to break retry storm; got: %v", err)
	}

	after := testutil.ToFloat64(captureProtocolErrors.WithLabelValues("empty_hash"))
	if after-before != 1 {
		t.Errorf("captureProtocolErrors{reason=empty_hash} delta = %v, want 1", after-before)
	}

	// CFS must NOT transition to Warm (defense in depth).
	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-empty-hash", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState == nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = Warm despite empty hash; defense in depth failed")
	}
	if s.CheckpointHash != "" {
		t.Errorf("CheckpointHash = %q; should remain empty", s.CheckpointHash)
	}
	// CFS should also NOT have a Failed flag-flip or LastError set —
	// the reconciler treats this as "capture never happened," so the
	// CFS stays in whatever state it was. Pod-ready event triggers a
	// fresh attempt.
	if s.LocalCacheState == nvsnapv1alpha1.LocalCacheStateFailed {
		t.Errorf("LocalCacheState = Failed; should be untouched when swallowing empty-hash")
	}
	if s.LastError != "" {
		t.Errorf("LastError should stay empty (we don't surface as a CFS error); got %q", s.LastError)
	}
}

func TestReconcileSkipsOptOut(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-1")

	// Seed an opted-out CFS.
	cfs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": "fv-1"},
		"spec":       map[string]any{"functionVersionID": "fv-1", "optOut": true},
		"status":     map[string]any{},
	}}
	dyn := newFakeDynamic(cfs)
	nvsnapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nvsnap-server should NOT be called for opted-out FV; got %s %s", r.Method, r.URL.Path)
	}))
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Annotation must be removed even when skipping, so the reconciler
	// doesn't re-attempt every time the pod's status updates.
	updatedPod, _ := r.KubeClient.CoreV1().Pods("ns1").Get(context.Background(), "p1", metav1.GetOptions{})
	if _, present := updatedPod.Annotations[CheckpointOnWarmAnnotation]; present {
		t.Error("annotation should be removed on opt-out skip")
	}
}

// TestReconcileSkipsWhenCFSAlreadyWarm is the regression test for the
// duplicate-capture race observed on nvsnap-h100-a 2026-06-03 (nvca#178):
// after capture #1 succeeded, CRIU's freeze→thaw caused kubelet to
// flip Ready=true a second time; controller-runtime had already
// queued the second event; when the worker picked it up, the
// informer-cached pod still had the annotation present (the
// annotation-removal patch hadn't propagated). Without a CFS-Warm
// gate, the reconciler proceeded with a full duplicate ~3 min
// capture.
//
// The fix: read CFS fresh (already does via dynamic client direct
// Get), and skip the rest of Reconcile when LocalCacheState=Warm.
// Annotation removal is still called as a safety net to drain the
// queued trigger.
func TestReconcileSkipsWhenCFSAlreadyWarm(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-1")

	// Seed a Warm CFS to simulate "capture #1 already finished".
	cfs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": "fv-1"},
		"spec":       map[string]any{"functionVersionID": "fv-1"},
		"status": map[string]any{
			"localCacheState": string(nvsnapv1alpha1.LocalCacheStateWarm),
			"checkpointHash":  "abcdef0123456789",
			"capturedHere":    true,
			"capturedAt":      time.Now().UTC().Format(time.RFC3339),
			"attemptCount":    int64(0),
		},
	}}
	dyn := newFakeDynamic(cfs)

	// nvsnap-server MUST NOT be contacted for a Warm function — that's
	// the entire point of the gate. Any request fails the test.
	nvsnapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nvsnap-server should NOT be called for Warm CFS; got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	// Take a baseline of the counter so we can assert it incremented
	// exactly once after Reconcile.
	beforeWarm := testutil.ToFloat64(checkpointAttemptsSkippedWarm)
	beforeSuppressed := testutil.ToFloat64(checkpointAttemptsSuppressed)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must return nil on Warm skip; got %v", err)
	}

	// Metric assertions:
	//   - checkpointAttemptsSkippedWarm: exactly +1 (the new gate)
	//   - checkpointAttemptsSuppressed: unchanged (different gate, must not double-count)
	afterWarm := testutil.ToFloat64(checkpointAttemptsSkippedWarm)
	if afterWarm-beforeWarm != 1 {
		t.Errorf("checkpointAttemptsSkippedWarm: +%v, want +1", afterWarm-beforeWarm)
	}
	afterSuppressed := testutil.ToFloat64(checkpointAttemptsSuppressed)
	if afterSuppressed != beforeSuppressed {
		t.Errorf("checkpointAttemptsSuppressed should NOT increment on Warm skip; got delta=%v", afterSuppressed-beforeSuppressed)
	}

	// CFS state must be untouched — no AttemptCount bump, no
	// LastError set, no LastAttemptAt timestamp. The Warm skip is a
	// no-op on state, only on the trigger.
	got, _ := dyn.Resource(CFSResource).Get(context.Background(), "fv-1", metav1.GetOptions{})
	s := readStatus(got)
	if s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState changed: got %q, want Warm", s.LocalCacheState)
	}
	if s.AttemptCount != 0 {
		t.Errorf("AttemptCount bumped to %d on Warm skip — should be 0", s.AttemptCount)
	}
	if s.LastError != "" {
		t.Errorf("LastError set to %q on Warm skip — should remain empty", s.LastError)
	}
	if s.CheckpointHash != "abcdef0123456789" {
		t.Errorf("CheckpointHash changed: got %q, want abcdef0123456789", s.CheckpointHash)
	}

	// Annotation MUST be removed even on the Warm skip — this is the
	// stale-annotation drain that stops the workqueue from re-firing
	// indefinitely. Same semantics as the opt-out skip path.
	updatedPod, _ := r.KubeClient.CoreV1().Pods("ns1").Get(context.Background(), "p1", metav1.GetOptions{})
	if _, present := updatedPod.Annotations[CheckpointOnWarmAnnotation]; present {
		t.Error("annotation should be removed on Warm skip so the workqueue stops re-firing for this pod")
	}
}

// TestReconcileWarmGate_DoesNotFireOnCold confirms the gate is
// specific: a Cold CFS must NOT take the Warm short-circuit. Without
// this assertion the new gate could regress into "skip everything"
// if LocalCacheState's zero-value matched the comparison.
func TestReconcileWarmGate_DoesNotFireOnCold(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-1")
	dyn := newFakeDynamic() // CFS doesn't exist; reconciler creates Cold

	stub := &nvsnapServerStub{pollsBeforeCompleted: 1, hash: "newhash", size: 1}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	beforeWarm := testutil.ToFloat64(checkpointAttemptsSkippedWarm)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Counter MUST NOT increment on Cold path. CFS must end Warm
	// (the happy path).
	afterWarm := testutil.ToFloat64(checkpointAttemptsSkippedWarm)
	if afterWarm != beforeWarm {
		t.Errorf("checkpointAttemptsSkippedWarm fired on Cold CFS — gate is too eager (delta=%v)", afterWarm-beforeWarm)
	}
	got, _ := dyn.Resource(CFSResource).Get(context.Background(), "fv-1", metav1.GetOptions{})
	if state := readStatus(got).LocalCacheState; state != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("CFS not transitioned to Warm; got %q", state)
	}
}

func TestReconcileRecordsFailureOnCreateError(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-1")

	stub := &nvsnapServerStub{failAt: "create"}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	// MUST return nil. Returning the error would re-queue via
	// controller.go:255 (AddRateLimited) and trigger the nvsnap-h100-a
	// 2026-06-03 retry storm. recordFailure swallows + counts + logs.
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must return nil to suppress controller-runtime requeue; got %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.AttemptCount != 1 {
		t.Errorf("AttemptCount = %d, want 1 (first failure)", s.AttemptCount)
	}
	if !strings.Contains(s.LastError, "nvsnap CreateCheckpoint") {
		t.Errorf("LastError = %q, want CreateCheckpoint mention", s.LastError)
	}
	if s.LocalCacheState != "" && s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateCold {
		t.Errorf("LocalCacheState = %q, want empty or Cold (failure preserves prior state)", s.LocalCacheState)
	}

	// Annotation stays so a future pod-ready event (e.g., a sibling
	// pod for the same function) can reconsider. We deliberately do
	// NOT requeue THIS work item — the next legitimate event will.
	// Same pattern as the nvca#15 empty-hash path.
	updatedPod, _ := r.KubeClient.CoreV1().Pods("ns1").Get(context.Background(), "p1", metav1.GetOptions{})
	if updatedPod.Annotations[CheckpointOnWarmAnnotation] != "true" {
		t.Error("annotation should remain on failure so next pod-ready event can reconsider (no auto-retry, but eligible for re-attempt)")
	}
}

func TestReconcileMissingFunctionVersionAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "ns1",
			Annotations: map[string]string{
				CheckpointOnWarmAnnotation: "true",
				// no FunctionVersionIDAnnotation
			},
		},
	}
	nvsnapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nvsnap-server should NOT be called when FV annotation is missing")
	}))
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, newFakeDynamic())
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile should silently skip, got %v", err)
	}
}

func TestReconcileSkipsWhenAnnotationAbsent(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns1"}}
	nvsnapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, newFakeDynamic())
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile should be a no-op without the annotation: %v", err)
	}
}

// TestReconcileSkipsWhenNotReady covers the defensive guard added when
// Hook B was refactored to trust K8s PodReady instead of self-polling
// /health. The controller already enqueues only Ready pods, but a
// direct Reconcile() call with a not-Ready pod must early-return
// cleanly instead of barreling into a checkpoint POST.
func TestReconcileSkipsWhenNotReady(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-1")
	// Force NotReady.
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
	}
	nvsnapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("nvsnap-server must NOT be called when pod is not Ready")
		http.NotFound(w, nil)
	}))
	defer nvsnapSrv.Close()
	r := newTestReconciler(t, pod, nvsnapSrv, newFakeDynamic())
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile should be a no-op when pod not Ready: %v", err)
	}
}

// TestRecordFailurePreservesCapturedAt is a regression test for
// Greptile P1 on MR !1698. writeStatus does a per-key patch (the
// fix landed in the same commit), but recordFailure must still
// forward prev.CapturedAt explicitly so an operator debugging a
// CFS that was once Warm sees the original capture timestamp after
// a failed re-checkpoint attempt.
func TestRecordFailurePreservesCapturedAt(t *testing.T) {
	captured := metav1.NewTime(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	seed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": "fv-failtest"},
		"spec":       map[string]any{"functionVersionID": "fv-failtest"},
		"status": map[string]any{
			"checkpointHash":  "abc123",
			"capturedHere":    true,
			"capturedAt":      captured.UTC().Format(time.RFC3339),
			"localCacheState": string(nvsnapv1alpha1.LocalCacheStateWarm),
			"attemptCount":    int64(1),
		},
	}}
	dyn := newFakeDynamic(seed)
	r := &Reconciler{DynClient: dyn}

	prev := cfsStatus{
		CheckpointHash:  "abc123",
		CapturedHere:    true,
		CapturedAt:      &captured,
		LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
		AttemptCount:    1,
	}
	_ = r.recordFailure(context.Background(), "fv-failtest", "completed_failed", prev,
		fmtErrorf("simulated"), logrus.NewEntry(logrus.New()))

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-failtest", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	gotCapturedAt, found, _ := unstructured.NestedString(got.Object, "status", "capturedAt")
	if !found || gotCapturedAt == "" {
		t.Fatal("CapturedAt was dropped by recordFailure (Greptile P1 regression)")
	}
	if gotCapturedAt != captured.UTC().Format(time.RFC3339) {
		t.Errorf("CapturedAt drifted: got %q, want %q", gotCapturedAt, captured.UTC().Format(time.RFC3339))
	}
	// AttemptCount should have bumped.
	gotAttempt, _, _ := unstructured.NestedInt64(got.Object, "status", "attemptCount")
	if gotAttempt != 2 {
		t.Errorf("AttemptCount = %d, want 2 (1 prev + 1 from this failure)", gotAttempt)
	}
}

// TestWriteStatusPreservesUnmanagedKeys is a regression test for
// Greptile P1 on MR !1698. writeStatus must NOT replace the whole
// status subtree — it must merge by key so unmanaged fields like
// `conditions` (which other controllers or future revisions of
// this one may set) survive a status update.
func TestWriteStatusPreservesUnmanagedKeys(t *testing.T) {
	preExistingConditions := []any{
		map[string]any{
			"type":   "Ready",
			"status": "True",
		},
	}
	seed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": "fv-conditions"},
		"spec":       map[string]any{"functionVersionID": "fv-conditions"},
		"status": map[string]any{
			"conditions": preExistingConditions,
			// Plus a key writeStatus never sets — verify it survives too.
			"customOperatorField": "must-survive",
		},
	}}
	dyn := newFakeDynamic(seed)

	if err := writeStatus(context.Background(), dyn, "fv-conditions", statusUpdate{
		CheckpointHash:  "deadbeef",
		LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
	}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-conditions", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Managed key landed.
	gotHash, _, _ := unstructured.NestedString(got.Object, "status", "checkpointHash")
	if gotHash != "deadbeef" {
		t.Errorf("checkpointHash = %q, want deadbeef", gotHash)
	}
	// Unmanaged conditions preserved.
	gotConditions, found, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	if !found || len(gotConditions) != 1 {
		t.Fatal("status.conditions was wiped by writeStatus (Greptile P1 regression)")
	}
	cond := gotConditions[0].(map[string]any)
	if cond["type"] != "Ready" {
		t.Errorf("condition type = %v, want Ready", cond["type"])
	}
	// Arbitrary unmanaged key preserved.
	gotCustom, _, _ := unstructured.NestedString(got.Object, "status", "customOperatorField")
	if gotCustom != "must-survive" {
		t.Errorf("customOperatorField = %q, want must-survive (unmanaged keys must survive a writeStatus call)", gotCustom)
	}
}

// fmtErrorf is a tiny helper to keep the import set lean (avoid
// pulling in fmt just for fmt.Errorf in one test).
func fmtErrorf(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// recordFailure MUST return nil (not the cause) so controller-runtime
// drops the work item instead of requeueing. Returning the error
// triggers the retry-storm pattern that wedged nvsnap-h100-a
// 2026-06-03 (3 back-to-back 80 GB captures from a single L2 timeout).
// Regression test against that behavior.
func TestRecordFailureReturnsNil_NoRequeue(t *testing.T) {
	seed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
		"kind":       "NvSnapFunctionState",
		"metadata":   map[string]any{"name": "fv-noretry"},
		"spec":       map[string]any{"functionVersionID": "fv-noretry"},
		"status":     map[string]any{"attemptCount": int64(0)},
	}}
	dyn := newFakeDynamic(seed)
	r := &Reconciler{DynClient: dyn}

	got := r.recordFailure(context.Background(), "fv-noretry", "completed_failed",
		cfsStatus{}, fmtErrorf("simulated capture failure"), logrus.NewEntry(logrus.New()))
	if got != nil {
		t.Errorf("recordFailure returned %v; want nil so controller-runtime DOES NOT requeue (nvsnap-h100-a 2026-06-03 retry storm)", got)
	}
}

// Each reason swallows independently and bumps the metric with the
// right label so the operator can tell which failure stage fired.
func TestRecordFailureBumpsCounterByReason(t *testing.T) {
	for _, reason := range []string{"create_failed", "poll_failed", "completed_failed"} {
		t.Run(reason, func(t *testing.T) {
			seed := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
				"kind":       "NvSnapFunctionState",
				"metadata":   map[string]any{"name": "fv-" + reason},
				"spec":       map[string]any{"functionVersionID": "fv-" + reason},
				"status":     map[string]any{"attemptCount": int64(0)},
			}}
			dyn := newFakeDynamic(seed)
			r := &Reconciler{DynClient: dyn}
			before := testutil.ToFloat64(checkpointAttemptFailures.WithLabelValues(reason))

			err := r.recordFailure(context.Background(), "fv-"+reason, reason,
				cfsStatus{}, fmtErrorf("simulated"), logrus.NewEntry(logrus.New()))
			if err != nil {
				t.Fatalf("recordFailure returned %v; must be nil", err)
			}
			after := testutil.ToFloat64(checkpointAttemptFailures.WithLabelValues(reason))
			if after-before != 1 {
				t.Errorf("checkpointAttemptFailures{reason=%s} delta = %v, want 1", reason, after-before)
			}
		})
	}
}

// TestIsHealthyDrainsBody was removed alongside the Hook B
// /health poll. The earlier Greptile P2 fix (drain body before close)
// applied to isHealthy(); after the refactor to trust K8s PodReady,
// that function — and the body-drain concern — no longer exists.

// ─── nvca#179: L2 promote-state gate ─────────────────────────────────────

// TestReconcileL2PromoteReady_FlipsWarm — happy path: checkpoint
// reaches Completed, /pvc-state returns "ready" immediately, CFS
// flips to Warm. This is the case that eliminates the need for
// nvsnap-l2-wait (#179 design).
func TestReconcileL2PromoteReady_FlipsWarm(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-179a")

	stub := &nvsnapServerStub{
		pollsBeforeCompleted: 2,
		hash:                 "abcdef0123456789",
		size:                 12345,
		// promoteTerminalState empty defaults to "ready"
		// promotePollsBeforeTerminal=0 → first poll terminal
	}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-179a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = %q, want Warm (L2 ready → flip)", s.LocalCacheState)
	}
	if s.CheckpointHash != "abcdef0123456789" {
		t.Errorf("hash = %q, want abcdef0123456789", s.CheckpointHash)
	}
	if atomic.LoadInt32(&stub.promotePolls) == 0 {
		t.Errorf("reconciler must have polled /pvc-state at least once")
	}
}

// TestReconcileL2PromoteFailed_RecordsFailure — terminal failure
// path: /pvc-state returns "failed". CFS must NOT flip to Warm; the
// reconciler must record a promote_failed failure so future pods
// cold-start instead of getting nvsnap.io/restore-from with a broken
// PVC reference.
func TestReconcileL2PromoteFailed_RecordsFailure(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-179b")

	stub := &nvsnapServerStub{
		pollsBeforeCompleted: 2,
		hash:                 "deadbeef00000000",
		size:                 12345,
		promoteTerminalState: "failed",
	}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	// recordFailure returns nil — reconciler swallows so controller-
	// runtime doesn't requeue (nvca#15 / #167 pattern).
	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must swallow promote-failed; got %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-179b", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState == nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = Warm despite L2 promote=failed; should stay Cold")
	}
	if s.AttemptCount == 0 {
		t.Errorf("AttemptCount = 0; promote_failed should bump it for backoff")
	}
	if s.LastError == "" {
		t.Errorf("LastError empty; promote_failed should populate it")
	}
}

// TestReconcileL2PromoteNoBackend_FlipsWarm — agent has no L2
// backend configured. nvsnap-server's /pvc-state returns 404 (catalog
// row exists but no L2 columns). Reconciler treats this as "no L2,
// proceed via peer cascade" and flips CFS=Warm.
func TestReconcileL2PromoteNoBackend_FlipsWarm(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-179c")

	stub := &nvsnapServerStub{
		pollsBeforeCompleted: 2,
		hash:                 "cafebabe00000000",
		size:                 12345,
		promote404:           true,
	}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-179c", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState != nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = %q, want Warm (no-L2 fallback)", s.LocalCacheState)
	}
}

// TestReconcileL2PromoteTimeout_RecordsFailure — /pvc-state never
// reaches terminal within PromotePollTimeout. The CRIU checkpoint
// itself was a success, but L2 stalled. Reconciler records a
// promote_poll_failed failure (CFS stays Cold; future pods cold-start)
// and returns nil so controller-runtime doesn't requeue.
func TestReconcileL2PromoteTimeout_RecordsFailure(t *testing.T) {
	pod := inferencePod("p1", "ns1", "fv-179d")

	stub := &nvsnapServerStub{
		pollsBeforeCompleted: 2,
		hash:                 "feedface00000000",
		size:                 12345,
		// Never reach terminal — first 10000 polls return "pending"
		promotePollsBeforeTerminal: 10000,
		promoteTerminalState:       "ready",
	}
	nvsnapSrv := httptest.NewServer(stub.handler())
	defer nvsnapSrv.Close()

	dyn := newFakeDynamic()
	r := newTestReconciler(t, pod, nvsnapSrv, dyn)

	if err := r.Reconcile(context.Background(), pod); err != nil {
		t.Fatalf("Reconcile must swallow promote-timeout; got %v", err)
	}

	got, err := dyn.Resource(CFSResource).Get(context.Background(), "fv-179d", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CFS: %v", err)
	}
	s := readStatus(got)
	if s.LocalCacheState == nvsnapv1alpha1.LocalCacheStateWarm {
		t.Errorf("LocalCacheState = Warm despite L2 promote timeout; should stay Cold")
	}
	if s.LastError == "" || !strings.Contains(s.LastError, "L2 promote poll") {
		t.Errorf("LastError = %q, want one mentioning L2 promote poll", s.LastError)
	}
}
