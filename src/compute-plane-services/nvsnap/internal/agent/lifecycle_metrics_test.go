// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// metricValue is the value of the series of c whose labels are want (a
// counter's value, a histogram's number of observations); 0 without one.
func metricValue(t *testing.T, c prometheus.Collector, want map[string]string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			match := true
			for k, v := range want {
				match = match && got[k] == v
			}
			if match {
				return m.GetCounter().GetValue() + float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

// lifecycleAgent is the restorer of a fallback agent of node n0 (GPU
// product gpu) whose events go to the returned recorder.
func lifecycleAgent(t *testing.T, gpu string) (*criuAutoRestorer, *k8sevents.FakeRecorder) {
	t.Helper()
	a, cs := fallbackAgent(t)
	if _, err := cs.CoreV1().Nodes().Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n0",
		Labels: map[string]string{"nvidia.com/gpu.product": gpu}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	rec := k8sevents.NewFakeRecorder(16)
	a.events = rec
	c := a.criuRestorer()
	c.watchStart = time.Now().Add(-time.Hour)
	return c, rec
}

func quietEntry() *logrus.Entry { return logrus.NewEntry(quietLog()) }

// event returns the next event recorded, or "" when there is none.
func event(rec *k8sevents.FakeRecorder) string {
	select {
	case e := <-rec.Events:
		return e
	default:
		return ""
	}
}

// admittedPod is rank ordinal of an instance of size ranks on node n0,
// admitted by the webhook, Ready readyAfter after its creation.
func admittedPod(ordinal, ranks int, readyAfter time.Duration) *corev1.Pod {
	p := ready(enginePod(ordinal))
	p.Spec.NodeName = "n0"
	p.Annotations[webhook.AdmittedConfigAnnotation] = "cache://cfg"
	p.Annotations[modelvolume.CacheGroupSizeAnnotation] = strconv.Itoa(ranks)
	created := time.Now().Add(-time.Minute - readyAfter)
	p.CreationTimestamp = metav1.NewTime(created)
	p.Status.Conditions[0].LastTransitionTime = metav1.NewTime(created.Add(readyAfter))
	return p
}

func TestRanksOf(t *testing.T) {
	if n := recordRanks(map[string]string{"size": "4", "companion.1": "true", "companion.2": "true", "companion.3": "true"}); n != 1 {
		t.Errorf("recordRanks = %d, want 1 (4 members less 3 companions)", n)
	}
	if n := recordRanks(map[string]string{"size": "x"}); n != 0 {
		t.Errorf("recordRanks of a bad size = %d, want 0", n)
	}
	if n := podRanks(admittedPod(0, 8, time.Second)); n != 8 {
		t.Errorf("podRanks = %d, want 8", n)
	}
	if n := podRanks(&corev1.Pod{}); n != 1 {
		t.Errorf("podRanks of a pod of no instance = %d, want 1", n)
	}
	nats := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "nats"}}}}
	if n := gpuRanks([]*corev1.Pod{enginePod(0), enginePod(1), nats}); n != 2 {
		t.Errorf("gpuRanks = %d, want 2 (companions are not ranks)", n)
	}
}

// The record keeps its capture's last restores, newest last.
func TestRecordRestoreHistory(t *testing.T) {
	d := map[string]string{"checkpoint.0": "c0"}
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i := 0; i < groupRestoresMax+2; i++ {
		if !recordRestoreHistory(d, "c0", at.Add(time.Duration(i)*time.Minute), time.Duration(30+i)*time.Second) {
			t.Fatal("a restore of the record's capture not recorded")
		}
	}
	got := strings.Fields(d[groupRestoresKey])
	if len(got) != groupRestoresMax {
		t.Fatalf("%d restores kept, want %d", len(got), groupRestoresMax)
	}
	if got[len(got)-1] != "2026-11-02T10:11:00Z=41.0" || got[0] != "2026-11-02T10:02:00Z=32.0" {
		t.Fatalf("kept %v, want the last %d, newest last", got, groupRestoresMax)
	}
	if recordRestoreHistory(d, "other", at, time.Second) {
		t.Fatal("a restore of another capture recorded")
	}
}

func TestPodStartMethod(t *testing.T) {
	for want, ann := range map[string]map[string]string{
		metrics.MethodCRIU:     {webhook.CRIURestoredAnnotation: "2026-11-02T10:00:00Z"},
		metrics.MethodCachedir: {webhook.FallbackCaptureAnnotation: "cachedir-k-0"},
		metrics.MethodCold:     {},
	} {
		if got := podStartMethod(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}); got != want {
			t.Errorf("method of %v = %s, want %s", ann, got, want)
		}
	}
}

// A GPU pod's start is recorded once, by how it started, with an event.
func TestObservePodReady_Cold(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-ready-cold")
	labels := map[string]string{"method": metrics.MethodCold, "gpu": "GB300-ready-cold", "ranks": "2-4"}
	before := metricValue(t, metrics.PodReadySeconds, labels)
	pod := admittedPod(0, 4, 90*time.Second)
	c.observePodReady(context.Background(), pod)
	c.observePodReady(context.Background(), pod) // its next event
	if got := metricValue(t, metrics.PodReadySeconds, labels) - before; got != 1 {
		t.Fatalf("recorded %v times, want once", got)
	}
	if e := event(rec); !strings.Contains(e, eventStarted) || !strings.Contains(e, "1m30s") || !strings.Contains(e, "cold") {
		t.Fatalf("event %q", e)
	}
	// Deleted and seen again (a new watch): it is a new pod's start only
	// under a new UID.
	c.forgetPod(pod)
	c.observePodReady(context.Background(), pod)
	if got := metricValue(t, metrics.PodReadySeconds, labels) - before; got != 2 {
		t.Fatalf("after the pod was forgotten: %v, want 2", got)
	}
}

// A restored pod's start also records its restore's last phase: restored
// to Ready.
func TestObservePodReady_Restored(t *testing.T) {
	c, _ := lifecycleAgent(t, "GB300-ready-criu")
	pod := admittedPod(0, 1, 40*time.Second)
	pod.Annotations[webhook.CRIURestoredAnnotation] = pod.CreationTimestamp.Add(30 * time.Second).UTC().Format(time.RFC3339)
	phase := map[string]string{"method": metrics.MethodCRIU, "phase": "ready", "gpu": "GB300-ready-criu", "ranks": "1"}
	started := map[string]string{"method": metrics.MethodCRIU, "gpu": "GB300-ready-criu", "ranks": "1"}
	beforePhase, beforeStart := metricValue(t, metrics.RestorePhaseSeconds, phase), metricValue(t, metrics.PodReadySeconds, started)
	c.observePodReady(context.Background(), pod)
	if metricValue(t, metrics.RestorePhaseSeconds, phase)-beforePhase != 1 || metricValue(t, metrics.PodReadySeconds, started)-beforeStart != 1 {
		t.Fatal("a restored pod's start or its restored-to-Ready phase not recorded")
	}
}

// What is not a GPU workload's start the agent watched is not recorded.
func TestObservePodReady_NotCounted(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-ready-skip")
	labels := map[string]string{"gpu": "GB300-ready-skip"}
	before := metricValue(t, metrics.PodReadySeconds, labels)
	notAdmitted := admittedPod(0, 1, time.Second)
	delete(notAdmitted.Annotations, webhook.AdmittedConfigAnnotation)
	companion := admittedPod(1, 1, time.Second)
	companion.Annotations[webhook.CRIUGroupCompanionAnnotation] = "true"
	noGPU := admittedPod(2, 1, time.Second)
	noGPU.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	notReady := admittedPod(3, 1, time.Second)
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	beforeWatch := admittedPod(4, 1, time.Second)
	beforeWatch.Status.Conditions[0].LastTransitionTime = metav1.NewTime(c.watchStart.Add(-time.Minute))
	deleting := admittedPod(5, 1, time.Second)
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	for _, p := range []*corev1.Pod{notAdmitted, companion, noGPU, notReady, beforeWatch, deleting} {
		c.observePodReady(context.Background(), p)
	}
	if metricValue(t, metrics.PodReadySeconds, labels) != before {
		t.Fatal("a pod that is not a watched GPU workload's start was recorded")
	}
	if e := event(rec); e != "" {
		t.Fatalf("event %q", e)
	}
}

func TestObserveGroupCapture(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-capture")
	leader := admittedPod(0, 8, time.Second)
	captured := map[string]string{"method": metrics.MethodCRIU, "outcome": metrics.CaptureCaptured, "gpu": "GB300-capture", "ranks": "5-8"}
	failed := map[string]string{"method": metrics.MethodCRIU, "outcome": metrics.CaptureFailed, "reason": "criu", "gpu": "GB300-capture", "ranks": "5-8"}
	group := map[string]string{"method": metrics.MethodCRIU, "phase": "group", "gpu": "GB300-capture", "ranks": "5-8"}
	c0, f0, g0 := metricValue(t, metrics.CaptureOutcomes, captured), metricValue(t, metrics.CaptureOutcomes, failed), metricValue(t, metrics.CapturePhaseSeconds, group)

	c.observeGroupCapture(context.Background(), "k", leader, 8, 10, nil, 9*time.Minute, quietEntry())
	if e := event(rec); !strings.Contains(e, eventCaptured) || !strings.Contains(e, "8 ranks") {
		t.Fatalf("event %q", e)
	}
	c.observeGroupCapture(context.Background(), "k", leader, 8, 10, errors.New("CRIU dump failed (criu-v2): exit status 1"), time.Minute, quietEntry())
	if e := event(rec); !strings.Contains(e, eventCaptureFailed) || !strings.Contains(e, "exit status 1") {
		t.Fatalf("event %q", e)
	}
	if metricValue(t, metrics.CaptureOutcomes, captured)-c0 != 1 || metricValue(t, metrics.CaptureOutcomes, failed)-f0 != 1 {
		t.Fatal("outcomes not counted")
	}
	if metricValue(t, metrics.CapturePhaseSeconds, group)-g0 != 1 {
		t.Fatal("want the group phase of the capture that succeeded only")
	}
}

func TestObserveRefusal(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-refused")
	labels := map[string]string{"outcome": metrics.CaptureRefused, "reason": "mptcp", "gpu": "GB300-refused", "ranks": "1"}
	before := metricValue(t, metrics.CaptureOutcomes, labels)
	c.observeRefusal(context.Background(), "k", admittedPod(0, 1, time.Second), 1, "preflight: nats: process 1 (nats-server) holds MPTCP sockets", quietEntry())
	if metricValue(t, metrics.CaptureOutcomes, labels)-before != 1 {
		t.Fatal("refusal not counted as mptcp")
	}
	if e := event(rec); !strings.Contains(e, "Warning "+eventCaptureRefused) {
		t.Fatalf("event %q", e)
	}
}

// A verified capture records the time from its start to its restored pods
// serving; one whose pods never served is counted unverified.
func TestObserveVerify(t *testing.T) {
	c, _ := lifecycleAgent(t, "GB300-verify")
	d := map[string]string{"size": "4", "companion.1": "true", "companion.2": "true", "companion.3": "true",
		"startedAt": time.Now().Add(-9 * time.Minute).UTC().Format(time.RFC3339)}
	verified := map[string]string{"outcome": metrics.CaptureVerified, "gpu": "GB300-verify", "ranks": "1"}
	unverified := map[string]string{"outcome": metrics.CaptureUnverified, "gpu": "GB300-verify", "ranks": "1"}
	phase := map[string]string{"phase": "verify", "gpu": "GB300-verify", "ranks": "1"}
	v0, u0, p0 := metricValue(t, metrics.CaptureOutcomes, verified), metricValue(t, metrics.CaptureOutcomes, unverified), metricValue(t, metrics.CapturePhaseSeconds, phase)
	pod := admittedPod(0, 1, time.Second)
	c.observeVerify(context.Background(), "k", pod, d, true, "", quietEntry())
	c.observeVerify(context.Background(), "k", pod, d, false, "the engine exited", quietEntry())
	if metricValue(t, metrics.CaptureOutcomes, verified)-v0 != 1 || metricValue(t, metrics.CaptureOutcomes, unverified)-u0 != 1 {
		t.Fatal("verify outcomes not counted")
	}
	if metricValue(t, metrics.CapturePhaseSeconds, phase)-p0 != 1 {
		t.Fatal("the verified capture's time not recorded")
	}
}

func TestObserveRestore(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-restore")
	pod := admittedPod(0, 2, time.Second)
	for _, tc := range []struct {
		outcome, reason, event string
		err                    error
	}{
		{metrics.RestoreRestored, "", "Normal " + eventRestored, nil},
		{metrics.RestoreFailed, "gpushare", "Warning " + eventRestoreFailed, errors.New("gpushare resume: CUDA_ERROR_UNKNOWN")},
		{metrics.RestoreNotRun, "fetch", "Warning " + eventRestoreFailed, errors.New("the checkpoint could not be brought to this node")},
	} {
		labels := map[string]string{"outcome": tc.outcome, "reason": tc.reason, "gpu": "GB300-restore", "ranks": "2-4"}
		before := metricValue(t, metrics.RestoreOutcomes, labels)
		c.observeRestore("k", pod, "GB300-restore", 2, tc.outcome, tc.err, 35*time.Second, quietEntry())
		if metricValue(t, metrics.RestoreOutcomes, labels)-before != 1 {
			t.Errorf("%s not counted", tc.outcome)
		}
		if e := event(rec); !strings.HasPrefix(e, tc.event) {
			t.Errorf("%s: event %q, want %s", tc.outcome, e, tc.event)
		}
	}
}

// A rank's capture is recorded by phase and its size by part: the GPU
// state, the rootfs diff and the rest (process memory).
func TestObserveRankCapture(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gpushare", "chunks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gpushare", "chunks", "0"), make([]byte, 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rootfsDiffArchive), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	times := &rankTimes{}
	times.add("dump", 20*time.Second)
	times.add("snapshot", 2*time.Second)
	times.add("rank", 30*time.Second)
	gpu := "GB300-rank"
	before := map[string]float64{}
	for _, part := range []string{"gpu", "rootfs", "memory"} {
		before[part] = metricValue(t, metrics.CaptureBytes, map[string]string{"part": part, "gpu": gpu})
	}
	dump0 := metricValue(t, metrics.CapturePhaseSeconds, map[string]string{"phase": "dump", "gpu": gpu, "ranks": "2-4"})
	observeRankCapture(gpu, 2, times, dir, 10000)
	for _, part := range []string{"gpu", "rootfs", "memory"} {
		if metricValue(t, metrics.CaptureBytes, map[string]string{"part": part, "gpu": gpu})-before[part] != 1 {
			t.Errorf("part %s not recorded", part)
		}
	}
	if metricValue(t, metrics.CapturePhaseSeconds, map[string]string{"phase": "dump", "gpu": gpu, "ranks": "2-4"})-dump0 != 1 {
		t.Error("dump phase not recorded")
	}
}

// Phases added from another goroutine (the dump's snapshot) are kept.
func TestRankTimesConcurrent(t *testing.T) {
	times := &rankTimes{}
	done := make(chan struct{})
	go func() {
		times.add("snapshot", time.Second)
		close(done)
	}()
	times.add("dump", time.Second)
	<-done
	n := 0
	times.each(func(string, time.Duration) { n++ })
	if n != 2 {
		t.Fatalf("%d phases, want 2", n)
	}
	var none *rankTimes
	none.add("x", time.Second) // a capture without times
}

// A checkpoint already on the node is no fetch; one fetched from a peer is
// counted with its source; one no source has fails.
func TestEnsureLocal_FetchMetrics(t *testing.T) {
	sumOf := func(source, result string) float64 {
		return metricValue(t, metrics.RestoreFetches, map[string]string{"source": source, "result": result})
	}

	peerURL, id, _ := newPeerAgent(t)
	catalogURL, _ := fakeCatalog(t, []map[string]string{{"node_name": "node-A", "agent_url": peerURL}})
	a, _ := receiverAgent(t, catalogURL)
	ok0 := sumOf("peer", "ok")
	if err := a.EnsureLocal(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if sumOf("peer", "ok")-ok0 != 1 {
		t.Fatal("a fetch from a peer not counted")
	}

	local0 := sumOf("peer", "ok") + sumOf("unknown", "failed")
	if err := a.EnsureLocal(t.Context(), id); err != nil { // now local
		t.Fatal(err)
	}
	if sumOf("peer", "ok")+sumOf("unknown", "failed") != local0 {
		t.Fatal("a checkpoint already on the node counted as a fetch")
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	catalogURL, _ = fakeCatalog(t, []map[string]string{{"node_name": "dead", "agent_url": deadURL}})
	b, _ := receiverAgent(t, catalogURL)
	failed0 := sumOf("unknown", "failed")
	if err := b.EnsureLocal(t.Context(), "ckpt-nowhere"); err == nil {
		t.Fatal("want the fetch to fail")
	}
	if sumOf("unknown", "failed")-failed0 != 1 {
		t.Fatal("a failed fetch not counted")
	}
}
