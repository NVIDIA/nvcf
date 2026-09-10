/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconciler

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// captureProtocolErrors counts protocol-level failures returned by
// nvsnap-server that the reconciler can't act on — typically a bug in
// the agent or nvsnap-server contract (e.g., Completed phase without a
// content hash, see nvca#15 + nvnvsnap#61). Every increment is a
// situation the operator should investigate; wire a Prometheus alert
// rule on rate(...[5m]) > 0.
//
// Cardinality budget: one label `reason`, expected values
// {"empty_hash"}. New reasons must be enumerated below and documented
// — high-cardinality labels (e.g., per-checkpointID) belong in logs
// or traces, not here.
var captureProtocolErrors = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_capture_protocol_error_total",
		Help: "Count of nvsnap-server / agent protocol violations the reconciler swallowed " +
			"to avoid a retry storm. Non-zero means the nvsnap control-plane has a bug " +
			"the operator should investigate. Labeled by reason: empty_hash = Completed " +
			"checkpoint returned with no content hash (see nvnvsnap#61).",
	},
	[]string{"reason"},
)

// checkpointAttemptFailures counts checkpoint attempts that ended in a
// terminal failure and were SWALLOWED by the reconciler (returned nil
// up the controller stack so controller-runtime does NOT requeue).
//
// Why we swallow: a single failed checkpoint attempt should not trigger
// an infinite re-checkpoint loop. The pod is still running; the
// operator wants logs + a counter + the next pod-ready cycle to make
// the call, not a tight retry that re-attempts an 80 GB CRIU dump
// every 30 seconds. See nvsnap-h100-a 2026-06-03: a single L2 promote
// timeout drove 3 back-to-back full captures before manual
// intervention.
//
// Cardinality budget: one label `reason`, fixed set:
//
//	create_failed         — nvsnap-server CreateCheckpoint API returned error
//	poll_failed           — pollCheckpointTerminal returned error (timeout
//	                        or non-transient API error)
//	completed_failed      — checkpoint reached terminal Phase=Failed
//	promote_poll_failed   — L2 promote-state poll timed out or hit a
//	                        non-transient API error (nvca#179). The CRIU
//	                        capture itself succeeded; only the L2 fan-out
//	                        wasn't confirmed within the deadline.
//	promote_failed        — L2 promote terminally failed (Job error,
//	                        snapshot failure, rox PVC bind failure).
//	                        Capture is durable on L1 / peer cascade.
//
// Every increment is a situation the operator should investigate; wire
// a Prometheus alert rule on rate(...[5m]) > 0.
var checkpointAttemptFailures = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_checkpoint_attempt_failure_total",
		Help: "Count of checkpoint attempts that failed terminally and were swallowed " +
			"by the reconciler (no requeue). The pod continues to run cold; the next " +
			"pod-ready event decides whether to re-attempt. Non-zero means an operator " +
			"should investigate the underlying capture failure. Labels: " +
			"reason={create_failed,poll_failed,completed_failed,promote_poll_failed,promote_failed}.",
	},
	[]string{"reason"},
)

// checkpointAttemptsSuppressed counts checkpoint attempts that were
// skipped because the per-function failure backoff (nvca#167) window
// has not yet elapsed. Sustained non-zero rate means the cluster has
// at least one workload NvSnap cannot capture — typically the workload
// is structurally incompatible (no LD_PRELOAD intercept, weird
// signal handling, kernel-version-specific bug) and the operator
// should either opt the function out (spec.optOut=true) or fix the
// agent. The label is intentionally minimal — distinguishing
// individual functions belongs in traces/logs, not here.
var checkpointAttemptsSuppressed = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_checkpoint_attempt_suppressed_total",
		Help: "Count of checkpoint attempts skipped by the per-function failure-backoff " +
			"gate. Sustained non-zero rate indicates at least one workload that NvSnap " +
			"cannot capture; check CFS lastError and consider opting out.",
	},
)

// checkpointAttemptsSkippedWarm counts attempts that were skipped
// because NvSnapFunctionState.status.localCacheState was already Warm
// (nvca#178). This fires when a duplicate pod-ready event reaches
// the reconciler after a successful capture — e.g. CRIU's freeze →
// thaw causes kubelet to flip Ready=true a second time, and the
// workqueue picks up the queued event before the informer-cached pod
// has the annotation-removal patch applied. The CFS check at the
// gate catches this; without it we observed a full duplicate ~3 min
// capture for the same hash on nvsnap-h100-a 2026-06-03.
//
// Non-zero rate is benign (the gate is doing its job). A SUSTAINED
// high rate per function would indicate a flapping pod ready signal
// independent of CRIU, which is worth investigating but doesn't
// damage anything.
var checkpointAttemptsSkippedWarm = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_checkpoint_attempt_skipped_warm_total",
		Help: "Count of checkpoint attempts skipped because CFS LocalCacheState was " +
			"already Warm at the reconciler gate (duplicate pod-ready trigger). " +
			"Benign — the gate prevented a full duplicate capture.",
	},
)

// checkpointAttemptsSkippedInflight counts attempts skipped by the
// capture-once claim (nvca#189): another pod of the same function-
// version already holds the Capturing claim, so this reconcile backed
// off instead of firing a duplicate capture. When N pods of a function
// deploy simultaneously cold, this counter rises to ~N-1 per capture —
// that's the thundering-herd guard doing its job (one capture, N-1
// skips). A sustained high rate with NO completing capture would point
// at a stuck claim (owner crashing before lease expiry); investigate
// alongside the capture-failure counter.
var checkpointAttemptsSkippedInflight = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_checkpoint_attempt_skipped_inflight_total",
		Help: "Count of checkpoint attempts skipped because another pod holds the " +
			"in-flight capture claim for the same function-version (capture-once, " +
			"nvca#189). Expected to rise to ~N-1 per capture when N pods deploy cold " +
			"simultaneously — the guard prevented N-1 duplicate captures.",
	},
)

// cfsSweepRecovered counts NvSnapFunctionStates that the controller's
// periodic recovery sweep flipped to Warm from an existing nvsnap-server
// capture WITHOUT a live source pod (nvca#104 durable-warm). A non-zero
// rate is healthy — the sweep is catching captures whose live reconcile
// died before writeStatus. A sustained HIGH rate suggests the happy-path
// reconcile is frequently interrupted (controller restarts, source pods
// scaled away mid-capture) and is worth investigating. See
// docs/users/nvsnap/DURABLE-WARM-SWEEP.md.
var cfsSweepRecovered = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "nvca_nvsnap_cfs_sweep_recovered_total",
		Help: "Count of NvSnapFunctionStates the periodic recovery sweep flipped to " +
			"Warm from an existing capture without a live source pod (nvca#104). " +
			"Closes the gap where an interrupted Hook B reconcile leaves CFS stuck " +
			"not-Warm.",
	},
)
