// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	k8sevents "k8s.io/client-go/tools/record"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// What captures and restores did, kept beyond the agent's life: the
// Prometheus series of internal/metrics/lifecycle.go, and one summary line
// per capture, restore and pod start ("nvsnap ... summary"), which the
// cluster's log shipper keeps with the function's namespace and pod.

// Summary messages, searched for in the log store.
const (
	captureSummary  = "nvsnap capture summary"
	restoreSummary  = "nvsnap restore summary"
	podReadySummary = "nvsnap pod ready summary"
)

// Event reasons on pods.
const (
	eventCaptured       = "NvsnapCaptured"
	eventCaptureFailed  = "NvsnapCaptureFailed"
	eventCaptureRefused = "NvsnapCaptureRefused"
	eventRestored       = "NvsnapRestored"
	eventRestoreFailed  = "NvsnapRestoreFailed"
	eventStarted        = "NvsnapStarted"
)

// podEvent records an event on pod; nothing without a kube client.
func (a *Agent) podEvent(pod *corev1.Pod, warning bool, reason, format string, args ...any) {
	if pod == nil {
		return
	}
	a.event(pod, warning, reason, format, args...)
}

// nodeEvent records an event on this agent's node.
func (a *Agent) nodeEvent(warning bool, reason, format string, args ...any) {
	if a.config.NodeName == "" {
		return
	}
	// A node's events name it by name for its UID, as the kubelet's do.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: a.config.NodeName, UID: types.UID(a.config.NodeName)}}
	a.event(node, warning, reason, format, args...)
}

// event records an event on obj; nothing without a kube client.
func (a *Agent) event(obj k8sruntime.Object, warning bool, reason, format string, args ...any) {
	if a.kubeClient == nil {
		return
	}
	a.eventsOnce.Do(func() {
		if a.events != nil {
			return // a test's
		}
		b := k8sevents.NewBroadcaster()
		b.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: a.kubeClient.CoreV1().Events("")})
		a.events = b.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "nvsnap-agent", Host: a.config.NodeName})
	})
	kind := corev1.EventTypeNormal
	if warning {
		kind = corev1.EventTypeWarning
	}
	a.events.Eventf(obj, kind, reason, format, args...)
}

// nodeGPU returns the GPU product of node, from its labels; "" when it
// cannot be read. Nodes do not change GPUs, so the answer is kept.
func (a *Agent) nodeGPU(ctx context.Context, node string) string {
	if node == "" || a.kubeClient == nil {
		return ""
	}
	if v, ok := a.gpuProducts.Load(node); ok {
		return v.(string)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := a.kubeClient.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	var info CatalogInfo
	populateFromNode(&info, n)
	a.gpuProducts.Store(node, info.GPUType)
	return info.GPUType
}

// podGPU is the GPU product of pod's node.
func (a *Agent) podGPU(ctx context.Context, pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	return a.nodeGPU(ctx, pod.Spec.NodeName)
}

// gpuRanks counts the pods of pods that have a GPU container: an
// instance's ranks, without its companions.
func gpuRanks(pods []*corev1.Pod) int {
	n := 0
	for _, p := range pods {
		if gpuContainer(p) != "" {
			n++
		}
	}
	return n
}

// recordRanks is the number of ranks a group record's capture has: its
// members less its companions.
func recordRanks(d map[string]string) int {
	size, err := strconv.Atoi(d["size"])
	if err != nil {
		return 0
	}
	for k, v := range d {
		if strings.HasPrefix(k, "companion.") && v == "true" {
			size--
		}
	}
	return size
}

// podRanks is the size of the instance pod is a rank of; 1 for a pod of
// no instance.
func podRanks(pod *corev1.Pod) int {
	if _, s, ok := podRank(pod, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation); ok {
		return s
	}
	return 1
}

// instanceRanks is the size of the instance pod is a rank of. A restore's
// placeholder carries its group's size with its companions, so its ranks
// come from the group record; 0 when that cannot be read.
func (c *criuAutoRestorer) instanceRanks(ctx context.Context, pod *corev1.Pod) int {
	key := pod.Annotations[webhook.CRIUGroupAnnotation]
	if key == "" {
		return podRanks(pod)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		return 0
	}
	return recordRanks(cm.Data)
}

// observeGroupCapture records the end of an instance capture that was
// claimed: captured (its restore verifies it) or failed.
func (c *criuAutoRestorer) observeGroupCapture(ctx context.Context, key string, leader *corev1.Pod, ranks, members int, err error, d time.Duration, log *logrus.Entry) {
	gpu := c.a.podGPU(ctx, leader)
	outcome, reason := metrics.CaptureCaptured, ""
	fields := logrus.Fields{"criuGroup": key, "method": metrics.MethodCRIU, "ranks": ranks, "members": members,
		"gpu": gpu, "seconds": int(d.Seconds()), "bundle": c.a.currentBundle()}
	if err != nil {
		outcome, reason = metrics.CaptureFailed, metrics.Reason(err.Error())
		fields["reason"], fields["error"] = reason, truncate(err.Error(), groupReasonMax)
	} else {
		metrics.ObserveCapturePhase(metrics.MethodCRIU, "group", gpu, ranks, d)
	}
	metrics.ObserveCapture(metrics.MethodCRIU, outcome, reason, gpu, ranks)
	fields["outcome"] = outcome
	log.WithFields(fields).Info(captureSummary)
	if err != nil {
		c.a.podEvent(leader, true, eventCaptureFailed, "instance capture of %d ranks failed: %s", ranks, truncate(err.Error(), groupReasonMax))
	} else {
		c.a.podEvent(leader, false, eventCaptured, "instance of %d ranks captured in %s; its restore verifies it", ranks, d.Round(time.Second))
	}
}

// observeRefusal records a configuration the preflight refused to capture.
func (c *criuAutoRestorer) observeRefusal(ctx context.Context, key string, leader *corev1.Pod, ranks int, why string, log *logrus.Entry) {
	gpu := c.a.podGPU(ctx, leader)
	reason := metrics.Reason(why)
	metrics.ObserveCapture(metrics.MethodCRIU, metrics.CaptureRefused, reason, gpu, ranks)
	log.WithFields(logrus.Fields{"criuGroup": key, "method": metrics.MethodCRIU, "outcome": metrics.CaptureRefused,
		"reason": reason, "ranks": ranks, "gpu": gpu, "bundle": c.a.currentBundle(), "error": truncate(why, groupReasonMax)}).Info(captureSummary)
	c.a.podEvent(leader, true, eventCaptureRefused, "the configuration cannot be captured: %s", truncate(why, groupReasonMax))
}

// observeVerify records whether a capture's restored pods served, and the
// time from the capture's start to then.
func (c *criuAutoRestorer) observeVerify(ctx context.Context, key string, pod *corev1.Pod, d map[string]string, served bool, why string, log *logrus.Entry) {
	gpu := c.a.podGPU(ctx, pod)
	ranks := recordRanks(d)
	fields := logrus.Fields{"criuGroup": key, "method": metrics.MethodCRIU, "ranks": ranks, "gpu": gpu}
	outcome := metrics.CaptureUnverified
	if served {
		outcome = metrics.CaptureVerified
		if t, err := time.Parse(time.RFC3339, d["startedAt"]); err == nil {
			took := time.Since(t)
			metrics.ObserveCapturePhase(metrics.MethodCRIU, "verify", gpu, ranks, took)
			fields["seconds"] = int(took.Seconds())
		}
	} else {
		fields["error"] = why
	}
	metrics.ObserveCapture(metrics.MethodCRIU, outcome, "", gpu, ranks)
	fields["outcome"] = outcome
	log.WithFields(fields).Info(captureSummary)
}

// observeGPUShareFailed records a configuration marked to start without
// the gpushare library.
func (c *criuAutoRestorer) observeGPUShareFailed(ctx context.Context, pod *corev1.Pod, log *logrus.Entry) {
	gpu := c.a.podGPU(ctx, pod)
	ranks := podRanks(pod)
	metrics.ObserveCapture(metrics.MethodCRIU, metrics.CaptureGPUShareFailed, "gpushare", gpu, ranks)
	log.WithFields(logrus.Fields{"method": metrics.MethodCRIU, "outcome": metrics.CaptureGPUShareFailed,
		"ranks": ranks, "gpu": gpu, "bundle": c.a.currentBundle()}).Info(captureSummary)
}

// observeCachedirCapture records a rank's cachedir capture.
func (c *criuAutoRestorer) observeCachedirCapture(ctx context.Context, pod *corev1.Pod, bytes int64, err error, d time.Duration, log *logrus.Entry) {
	gpu := c.a.podGPU(ctx, pod)
	ranks := podRanks(pod)
	outcome, reason := metrics.CaptureCaptured, ""
	fields := logrus.Fields{"method": metrics.MethodCachedir, "ranks": ranks, "gpu": gpu, "seconds": int(d.Seconds())}
	if err != nil {
		outcome, reason = metrics.CaptureFailed, metrics.Reason(err.Error())
		fields["reason"], fields["error"] = reason, truncate(err.Error(), groupReasonMax)
	} else {
		metrics.ObserveCapturePhase(metrics.MethodCachedir, "rank", gpu, ranks, d)
		metrics.ObserveCaptureBytes(metrics.MethodCachedir, "cachedir", gpu, bytes)
		fields["bytes"] = bytes
	}
	metrics.ObserveCapture(metrics.MethodCachedir, outcome, reason, gpu, ranks)
	fields["outcome"] = outcome
	log.WithFields(fields).Info(captureSummary)
}

// observeRestore records the end of an instance's restore on its driving
// agent: restored, failed, or not run (tried again). d is its length, 0
// when not known.
func (c *criuAutoRestorer) observeRestore(key string, pod *corev1.Pod, gpu string, ranks int, outcome string, err error, d time.Duration, log *logrus.Entry) {
	reason := ""
	fields := logrus.Fields{"criuGroup": key, "method": metrics.MethodCRIU, "outcome": outcome, "ranks": ranks,
		"gpu": gpu, "seconds": int(d.Seconds()), "bundle": c.a.currentBundle()}
	if err != nil {
		reason = metrics.Reason(err.Error())
		fields["reason"], fields["error"] = reason, truncate(err.Error(), groupReasonMax)
	}
	if outcome == metrics.RestoreRestored && d > 0 {
		metrics.ObserveRestorePhase(metrics.MethodCRIU, "group", gpu, ranks, d)
	}
	metrics.ObserveRestore(metrics.MethodCRIU, outcome, reason, gpu, ranks)
	log.WithFields(fields).Info(restoreSummary)
	switch outcome {
	case metrics.RestoreRestored:
		c.a.podEvent(pod, false, eventRestored, "restored %d ranks from their capture in %s", ranks, d.Round(time.Millisecond))
	case metrics.RestoreFailed:
		c.a.podEvent(pod, true, eventRestoreFailed, "restore failed: %s", truncate(err.Error(), groupReasonMax))
	default:
		c.a.podEvent(pod, true, eventRestoreFailed, "restore did not run; the pods are replaced to try again: %s", truncate(err.Error(), groupReasonMax))
	}
}

// groupRestoresKey keeps a capture's last restores in its group record,
// newest last, as "RFC3339=seconds" separated by spaces: kubectl alone
// shows how its recent restores went.
const (
	groupRestoresKey = "restores"
	groupRestoresMax = 10
)

// recordRestoreHistory adds a restore of checkpoint id at at, which took
// took, to group record data d when id is of its capture.
func recordRestoreHistory(d map[string]string, id string, at time.Time, took time.Duration) bool {
	if !recordOf(d, id) {
		return false
	}
	entries := append(strings.Fields(d[groupRestoresKey]),
		at.UTC().Format(time.RFC3339)+"="+strconv.FormatFloat(took.Seconds(), 'f', 1, 64))
	if len(entries) > groupRestoresMax {
		entries = entries[len(entries)-groupRestoresMax:]
	}
	d[groupRestoresKey] = strings.Join(entries, " ")
	return true
}

// rankTimes are the phases of one rank's capture or restore on its node.
// A capture's snapshot adds to them from CRIU's post-dump action.
type rankTimes struct {
	mu     sync.Mutex
	phases map[string]time.Duration
}

func (t *rankTimes) add(phase string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phases == nil {
		t.phases = map[string]time.Duration{}
	}
	t.phases[phase] += d
}

// each calls f with every phase and its time.
func (t *rankTimes) each(f func(phase string, d time.Duration)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for p, d := range t.phases {
		f(p, d)
	}
}

// observeRankCapture records a rank's capture phases and its size by part.
func observeRankCapture(gpu string, ranks int, t *rankTimes, dir string, total int64) {
	t.each(func(phase string, d time.Duration) {
		metrics.ObserveCapturePhase(metrics.MethodCRIU, phase, gpu, ranks, d)
	})
	gpuBytes := gpuStoreBytes(dir)
	var rootfs int64
	if st, err := statSize(dir, rootfsDiffArchive); err == nil {
		rootfs = st
	}
	metrics.ObserveCaptureBytes(metrics.MethodCRIU, "gpu", gpu, gpuBytes)
	metrics.ObserveCaptureBytes(metrics.MethodCRIU, "rootfs", gpu, rootfs)
	metrics.ObserveCaptureBytes(metrics.MethodCRIU, "memory", gpu, total-gpuBytes-rootfs)
}

// statSize is the size of the file name in dir.
func statSize(dir, name string) (int64, error) {
	st, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// observeRankRestore records a rank's restore phases.
func observeRankRestore(gpu string, ranks int, t *rankTimes) {
	t.each(func(phase string, d time.Duration) {
		metrics.ObserveRestorePhase(metrics.MethodCRIU, phase, gpu, ranks, d)
	})
}

// placeholderRanks is the size of the instance the placeholder ns/name
// restores, from its annotations; 0 when it cannot be read.
func (a *Agent) placeholderRanks(ctx context.Context, ns, name string) int {
	if a.kubeClient == nil || ns == "" || name == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0
	}
	return a.criuRestorer().instanceRanks(ctx, p)
}

// observePodReady records, once, a GPU pod's time from creation to Ready,
// by how it started. Only a pod the webhook admitted counts (a restore's
// placeholder carries its restore, not the admitted configuration), and
// only once this agent watched it become Ready: after an agent restart,
// pods Ready before it are not counted again.
func (c *criuAutoRestorer) observePodReady(ctx context.Context, pod *corev1.Pod) {
	admitted := pod.Annotations[webhook.AdmittedConfigAnnotation] != "" || pod.Annotations[webhook.CRIURestoreAnnotation] != ""
	if pod.DeletionTimestamp != nil || !admitted ||
		pod.Annotations[webhook.CRIUGroupCompanionAnnotation] != "" || gpuContainer(pod) == "" {
		return
	}
	ready := readySince(pod)
	if ready.IsZero() || ready.Before(c.watchStart) {
		return
	}
	if _, done := c.readyObserved.LoadOrStore(pod.UID, struct{}{}); done {
		return
	}
	method := podStartMethod(pod)
	ranks := c.instanceRanks(ctx, pod)
	gpu := c.a.podGPU(ctx, pod)
	took := ready.Sub(pod.CreationTimestamp.Time)
	metrics.ObservePodReady(method, gpu, ranks, took)
	fields := logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "method": method, "ranks": ranks, "gpu": gpu,
		"seconds": int(took.Seconds()), "bundle": c.a.currentBundle()}
	if method == metrics.MethodCRIU {
		if at := restoredAt(pod); !at.IsZero() {
			d := ready.Sub(at.Time)
			metrics.ObserveRestorePhase(metrics.MethodCRIU, "ready", gpu, ranks, d)
			fields["restoredToReadySeconds"] = int(d.Seconds())
		}
	}
	if r := pod.Annotations[webhook.FallbackReasonAnnotation]; r != "" {
		fields["fallbackReason"] = r
	}
	c.a.log.WithFields(fields).Info(podReadySummary)
	c.a.podEvent(pod, false, eventStarted, "Ready %s after creation (%s start)", took.Round(time.Second), method)
}

// podStartMethod is how pod started: restored from a CRIU capture, seeded
// from a cachedir capture, or cold.
func podStartMethod(pod *corev1.Pod) string {
	switch {
	case pod.Annotations[webhook.CRIURestoredAnnotation] != "":
		return metrics.MethodCRIU
	case pod.Annotations[webhook.FallbackCaptureAnnotation] != "":
		return metrics.MethodCachedir
	default:
		return metrics.MethodCold
	}
}
