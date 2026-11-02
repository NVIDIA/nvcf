// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Lifecycle metrics: what captures, restores and admissions did and how
// long they took, kept by the cluster's Prometheus so the history outlives
// the agents. Every label takes a bounded set of values: no namespace, pod,
// function or checkpoint (those are in the summary log lines).

// Capture and restore methods.
const (
	MethodCRIU     = "criu"
	MethodCachedir = "cachedir"
	MethodCold     = "cold"
)

// Capture outcomes.
const (
	CaptureCaptured       = "captured"        // dumped; its restore verifies it
	CaptureVerified       = "verified"        // its restored pods served
	CaptureUnverified     = "unverified"      // its restored pods never served
	CaptureFailed         = "failed"          // the capture itself failed
	CaptureRefused        = "refused"         // the preflight refused the configuration
	CaptureGPUShareFailed = "gpushare-failed" // the pod never started under the library
)

// Restore outcomes.
const (
	RestoreRestored = "restored"
	RestoreFailed   = "failed"  // ran and failed
	RestoreNotRun   = "not-run" // could not run (fetch, placeholders); tried again
	RestoreRefused  = "refused" // the pod may not restore what it names
)

// Admission decisions.
const (
	AdmitRestore  = "restore"
	AdmitFallback = "fallback"
	AdmitCold     = "cold"
)

var (
	secondsBuckets = []float64{1, 2, 5, 10, 20, 30, 45, 60, 90, 120, 180, 240, 300, 450, 600, 900, 1200, 1800, 2700, 3600}
	bytesBuckets   = []float64{1e6, 1e7, 1e8, 1e9, 5e9, 1e10, 2.5e10, 5e10, 1e11, 2.5e11, 5e11, 1e12, 2e12}

	// CaptureOutcomes counts captures by method and outcome; reason is the
	// class of a failure or refusal ("" otherwise).
	CaptureOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "capture_outcomes_total",
		Help:      "Captures by method, outcome and failure reason, GPU product and instance size.",
	}, []string{"method", "outcome", "reason", "gpu", "ranks"})

	// CapturePhaseSeconds is the time a capture spent in a phase: per rank
	// "dump" (freeze, CRIU, GPU state, snapshot), "snapshot" (replay mounts
	// and rootfs diff, the tree stopped) and "rank" (all of it); per instance "group" (the driver's dump of every
	// member) and "verify" (capture start to its restored pods serving).
	CapturePhaseSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "capture_phase_seconds",
		Help:      "Time a capture spent in each phase.",
		Buckets:   secondsBuckets,
	}, []string{"method", "phase", "gpu", "ranks"})

	// CaptureBytes is the size of a rank's capture by part: "gpu" (GPU
	// state), "memory" (process images), "rootfs" (rootfs diff), "cachedir".
	CaptureBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "capture_bytes",
		Help:      "Size of a rank's capture by part.",
		Buckets:   bytesBuckets,
	}, []string{"method", "part", "gpu"})

	// RestoreOutcomes counts restores by method and outcome.
	RestoreOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "restore_outcomes_total",
		Help:      "Restores by method, outcome and failure reason, GPU product and instance size.",
	}, []string{"method", "outcome", "reason", "gpu", "ranks"})

	// RestorePhaseSeconds is the time a restore spent in a phase: per rank
	// "criu", "gpu" (GPU state reload and resume) and "rank" (the node's
	// whole restore); per instance
	// "group" (placeholders running to every rank restored) and "ready"
	// (restored to the pod Ready).
	RestorePhaseSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "restore_phase_seconds",
		Help:      "Time a restore spent in each phase.",
		Buckets:   secondsBuckets,
	}, []string{"method", "phase", "gpu", "ranks"})

	// RestoreFetches counts checkpoint fetches to a node by where the bytes
	// came from ("rails", "peer", "fsstore", "volume"). A checkpoint already
	// on the node is no fetch.
	RestoreFetches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "restore_fetches_total",
		Help:      "Checkpoint fetches to a node by source and result.",
	}, []string{"source", "result"})

	// RestoreFetchSeconds is the time a successful fetch took, by source.
	RestoreFetchSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "restore_fetch_seconds",
		Help:      "Time a checkpoint fetch to a node took, by source.",
		Buckets:   secondsBuckets,
	}, []string{"source"})

	// PodReadySeconds is a GPU pod's time from creation to Ready, by how
	// it started: restored (criu), seeded from a cachedir capture
	// (cachedir), or cold. Cold pods are the baseline the others save on.
	PodReadySeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "pod_ready_seconds",
		Help:      "GPU pod time from creation to Ready, by how it started.",
		Buckets:   secondsBuckets,
	}, []string{"method", "gpu", "ranks"})

	// Admissions counts the webhook's decision for a GPU pod of a captured
	// configuration: restore, fall back to the cachedir, or start cold, and
	// why.
	Admissions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "admissions_total",
		Help:      "Webhook decisions for GPU pods of captured configurations, and why.",
	}, []string{"decision", "reason"})

	// BuildInfo is 1 for the agent bundle this agent serves; join on it to
	// compare releases.
	BuildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "The agent bundle version this agent serves.",
	}, []string{"version"})
)

// lifecycleCollectors are registered with the agent's.
var lifecycleCollectors = []prometheus.Collector{
	CaptureOutcomes, CapturePhaseSeconds, CaptureBytes,
	RestoreOutcomes, RestorePhaseSeconds, RestoreFetches, RestoreFetchSeconds,
	PodReadySeconds, Admissions, BuildInfo,
}

// Ranks buckets an instance size: the label stays bounded for instances of
// tens of ranks.
func Ranks(n int) string {
	switch {
	case n <= 0:
		return "unknown"
	case n == 1:
		return "1"
	case n <= 4:
		return "2-4"
	case n <= 8:
		return "5-8"
	case n <= 16:
		return "9-16"
	default:
		return "17+"
	}
}

// labelMax bounds a free-form label value (a GPU product name).
const labelMax = 64

// GPU is the label value of a GPU product name.
func GPU(product string) string {
	product = strings.TrimSpace(product)
	if product == "" {
		return "unknown"
	}
	if len(product) > labelMax {
		product = product[:labelMax]
	}
	return product
}

// Reason classes a failure's text into a bounded label value.
func Reason(text string) string {
	t := strings.ToLower(text)
	switch {
	case t == "":
		return ""
	case strings.Contains(t, "mptcp"):
		return "mptcp"
	case strings.Contains(t, "rdma") || strings.Contains(t, "infiniband"):
		return "rdma"
	case strings.Contains(t, "gpushare"):
		return "gpushare"
	case strings.Contains(t, "ensure-local") || strings.Contains(t, "could not be brought"):
		return "fetch"
	case strings.Contains(t, "deadline exceeded") || strings.Contains(t, "timed out") || strings.Contains(t, "timeout"):
		return "timeout"
	case strings.Contains(t, "criu"):
		return "criu"
	default:
		return "other"
	}
}

// ObserveCapture counts a capture's outcome.
func ObserveCapture(method, outcome, reason, gpu string, ranks int) {
	CaptureOutcomes.WithLabelValues(method, outcome, reason, GPU(gpu), Ranks(ranks)).Inc()
}

// ObserveCapturePhase records the time a capture spent in phase.
func ObserveCapturePhase(method, phase, gpu string, ranks int, d time.Duration) {
	if d < 0 {
		return
	}
	CapturePhaseSeconds.WithLabelValues(method, phase, GPU(gpu), Ranks(ranks)).Observe(d.Seconds())
}

// ObserveCaptureBytes records the size of a part of a rank's capture; an
// empty part is not recorded.
func ObserveCaptureBytes(method, part, gpu string, n int64) {
	if n <= 0 {
		return
	}
	CaptureBytes.WithLabelValues(method, part, GPU(gpu)).Observe(float64(n))
}

// ObserveRestore counts a restore's outcome.
func ObserveRestore(method, outcome, reason, gpu string, ranks int) {
	RestoreOutcomes.WithLabelValues(method, outcome, reason, GPU(gpu), Ranks(ranks)).Inc()
}

// ObserveRestorePhase records the time a restore spent in phase.
func ObserveRestorePhase(method, phase, gpu string, ranks int, d time.Duration) {
	if d < 0 {
		return
	}
	RestorePhaseSeconds.WithLabelValues(method, phase, GPU(gpu), Ranks(ranks)).Observe(d.Seconds())
}

// ObserveFetch records a checkpoint fetch from source that took d.
func ObserveFetch(source string, ok bool, d time.Duration) {
	if source == "" {
		source = "unknown"
	}
	result := "ok"
	if !ok {
		result = "failed"
	}
	RestoreFetches.WithLabelValues(source, result).Inc()
	if ok {
		RestoreFetchSeconds.WithLabelValues(source).Observe(d.Seconds())
	}
}

// ObservePodReady records a GPU pod's time from creation to Ready.
func ObservePodReady(method, gpu string, ranks int, d time.Duration) {
	if d < 0 {
		return
	}
	PodReadySeconds.WithLabelValues(method, GPU(gpu), Ranks(ranks)).Observe(d.Seconds())
}

// CountAdmission counts a webhook decision.
func CountAdmission(decision, reason string) {
	Admissions.WithLabelValues(decision, reason).Inc()
}

// SetBuildInfo marks version as the bundle this agent serves.
func SetBuildInfo(version string) {
	if version == "" {
		version = "unknown"
	}
	BuildInfo.Reset()
	BuildInfo.WithLabelValues(version).Set(1)
}
