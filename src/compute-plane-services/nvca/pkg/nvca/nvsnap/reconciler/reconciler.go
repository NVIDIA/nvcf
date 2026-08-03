/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package reconciler implements Hook B of the NVCA × NvSnap integration:
// the post-Ready checkpoint loop. Pods stamped by Hook A (in
// pkg/nvca/nvsnap_hook.go) with nvsnap.io/checkpoint-on-warm: "true" are
// fed to this reconciler. It polls the inference container's health
// endpoint until it returns 200, waits the warmup buffer, POSTs a
// checkpoint request to nvsnap-server, polls until terminal, and
// updates NvSnapFunctionState.status.
//
// This package is the state machine only. The wiring (Pod informer +
// workqueue + agent startup) lands in a follow-up — keeps this PR
// scoped to the logic that's worth testing.
//
// Design: docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md §"Hook B".
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// Annotation keys driven by this reconciler. Must match the constants
// in pkg/nvca/nvsnap_hook.go — duplicated here to avoid an import cycle
// (nvsnap_hook.go lives in package nvca, which would otherwise import
// this package).
const (
	CheckpointOnWarmAnnotation = "nvsnap.io/checkpoint-on-warm"
	RestoreFromAnnotation      = "nvsnap.io/restore-from"
)

// Default timing knobs. Mirror nvcfv1.DefaultNvSnapWarmup{Timeout,Buffer}Seconds —
// duplicated to avoid an import dep on the operator CRD package
// (which would require all transitive deps to be in this scope).
// Tests should override via Reconciler fields; production wires from
// the agent's runtime config (PR-6).
const (
	defaultWarmupBuffer = 10 * time.Second
)

// FunctionVersionIDAnnotation carries the NGC function-version UUID
// on the pod. NVCA stamps this via labels-for-request elsewhere
// (nvsnap.io/source-function-version-id). The reconciler reads it off
// the pod rather than re-resolving from the ICMSRequest.
//
// NOTE: Hook A in PR-4 looks up FunctionVersionID from the
// ICMSRequest passed to CreatePodArtifactInstances. The reconciler
// doesn't have that request handy at watch time; an extra annotation
// on the pod is the bridge. PR-4 should stamp this annotation when it
// stamps CheckpointOnWarm — listed as a TODO in the integration doc.
const FunctionVersionIDAnnotation = "nvsnap.io/function-version-id"

// Reconciler runs the post-Ready checkpoint state machine for one
// pod at a time. Construct via New + Reconcile.
//
// Concurrency: safe for many concurrent Reconcile calls on different
// pods (each call is self-contained).
type Reconciler struct {
	// KubeClient mutates the pod (annotation removal at completion).
	KubeClient kubernetes.Interface

	// DynClient reads/writes NvSnapFunctionState.
	DynClient dynamic.Interface

	// NvSnapClient is the nvsnap-server HTTP client (PR-1).
	NvSnapClient *nvsnap.Client

	// InferenceContainerName is the container the nvsnap-server should
	// snapshot. Defaults to "inference" (NVCA convention).
	InferenceContainerName string

	// WarmupBuffer is the dwell between PodReady=true and POST
	// checkpoint. Default 10s. Lets post-Ready setup (CUDA graph
	// capture, JIT warmup) land before the snapshot. NVCA's utils
	// sidecar already runs the /health probe that gates PodReady,
	// so the reconciler trusts that signal and only adds this dwell.
	WarmupBuffer time.Duration

	// CheckpointPollInterval is how often the reconciler polls the
	// nvsnap-server checkpoint by id. Default 5s.
	CheckpointPollInterval time.Duration

	// CheckpointTimeout caps how long the reconciler waits for the
	// checkpoint to reach terminal status. Default 30 min — large
	// rootfs captures (NIM Qwen3-32B / vLLM 70B) can run minutes,
	// plus uploads to nvsnap-blobstore.
	CheckpointTimeout time.Duration

	// PromotePollInterval is how often the reconciler polls
	// nvsnap-server's pvc-state endpoint after the checkpoint reaches
	// terminal Completed (nvca#179). Default 5 s — same cadence as
	// CheckpointPollInterval; the L2 snap+clone is comparable in
	// duration to the CRIU dump itself for any non-trivial capture.
	PromotePollInterval time.Duration

	// PromotePollTimeout caps how long the reconciler waits for the
	// agent's L2 snap+clone to reach a terminal state. Default 15
	// min — Hyperdisk-ML snap+clone takes ~6 min for a 90 GB rwx
	// PVC on nvsnap-h100-a 2026-06-03; 15 min gives 2.5× headroom for
	// the largest captures and accommodates slower default snapshot
	// classes. Beyond this, the reconciler logs and records a
	// promote-poll failure on CFS — the underlying capture is still
	// durable on L1 / peer cascade, only the L2 fan-out is missed.
	PromotePollTimeout time.Duration

	// CaptureLeaseTTL bounds the capture-once Capturing claim
	// (nvca#189). When a reconcile wins the Cold→Capturing CAS it
	// stamps captureLeaseExpiry = now + CaptureLeaseTTL; other pods of
	// the same function-version observe the live claim and back off. To
	// guarantee a legitimately-running capture is never stolen
	// mid-flight, this must exceed the maximum time one reconcile can
	// hold the claim: WarmupBuffer + CheckpointTimeout +
	// PromotePollTimeout, plus margin. Zero = derive that sum in
	// applyDefaults. Only a crashed/hung capturer (one that exceeds the
	// whole budget) has its claim expire and become stealable.
	CaptureLeaseTTL time.Duration

	Log logrus.FieldLogger
}

// applyDefaults fills zero fields with sane defaults. Called at the
// top of Reconcile so callers can use a partially-populated
// Reconciler without thinking about every knob.
func (r *Reconciler) applyDefaults() {
	if r.InferenceContainerName == "" {
		r.InferenceContainerName = "inference"
	}
	if r.WarmupBuffer == 0 {
		r.WarmupBuffer = defaultWarmupBuffer
	}
	if r.CheckpointPollInterval == 0 {
		r.CheckpointPollInterval = 5 * time.Second
	}
	if r.CheckpointTimeout == 0 {
		r.CheckpointTimeout = 30 * time.Minute
	}
	if r.PromotePollInterval == 0 {
		r.PromotePollInterval = 5 * time.Second
	}
	if r.PromotePollTimeout == 0 {
		r.PromotePollTimeout = 15 * time.Minute
	}
	if r.CaptureLeaseTTL == 0 {
		// Derive a lease that provably covers the longest a single
		// reconcile can legitimately hold the Capturing claim — the
		// full capture budget plus a 5-minute margin — so a valid
		// in-flight capture is never stolen by a peer pod (nvca#189).
		r.CaptureLeaseTTL = r.WarmupBuffer + r.CheckpointTimeout + r.PromotePollTimeout + 5*time.Minute
	}
	if r.Log == nil {
		r.Log = logrus.NewEntry(logrus.New())
	}
}

// DefaultColdStartPioneerLeaseTTL is the lease TTL the MiniService-creation
// gate (package nvca) stamps when it elects a cold-start pioneer
// (serialized-herd). It reuses the same concept as CaptureLeaseTTL: a
// deferred replica must not steal the pioneer slot while the pioneer is
// still legitimately cold-starting + capturing, so the lease must cover
// the full cold-start-to-Warm budget — warmup buffer + checkpoint
// timeout + L2 promote timeout — plus margin. The gate has no Reconciler
// instance, so this exposes the same default sum applyDefaults derives,
// computed from the package defaults.
func DefaultColdStartPioneerLeaseTTL() time.Duration {
	return defaultWarmupBuffer + 30*time.Minute + 15*time.Minute + 5*time.Minute
}

// Reconcile drives the checkpoint state machine for one pod, end to
// end. Blocks until the checkpoint reaches a terminal state, the
// context is canceled, or a timeout fires.
//
// The pod must:
//   - have annotation CheckpointOnWarmAnnotation = "true"
//   - have annotation FunctionVersionIDAnnotation set to the NGC FV id
//   - be PodReady (caller responsibility — wire this from a Ready event)
//
// On terminal success: NvSnapFunctionState.status reflects the new
// hash + Warm cache state + CapturedHere=true; pod's
// checkpoint-on-warm annotation is removed (idempotency).
//
// On terminal failure: NvSnapFunctionState.status records the error
// and increments AttemptCount; pod's annotation remains so a future
// reconciler can retry.
func (r *Reconciler) Reconcile(ctx context.Context, pod *corev1.Pod) error {
	r.applyDefaults()
	if pod == nil {
		return errors.New("reconciler: pod is nil")
	}

	// Span covers the full Hook B reconcile — warmup buffer, POST
	// nvsnap-server, poll until terminal, write CFS status. Pod uid
	// is the link key so nvsnap-side spans (CRIU dump, cascade fetch)
	// can be correlated to the NVCA admission/reconcile that
	// triggered them. See docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md
	// §"Observability" for the span hierarchy.
	tracer := nvcaotel.NewTracer(nvcaotel.WithName("nvca.nvsnap.reconciler"))
	ctx, span := tracer.Start(ctx, "nvsnap.hook_b.reconcile",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			otelattr.String("k8s.pod.name", pod.Name),
			otelattr.String("k8s.pod.namespace", pod.Namespace),
			otelattr.String("k8s.pod.uid", string(pod.UID)),
		),
	)
	defer span.End()

	log := r.Log.WithFields(logrus.Fields{
		"pod":       pod.Namespace + "/" + pod.Name,
		"component": "nvsnap-reconciler",
	})

	if pod.Annotations[CheckpointOnWarmAnnotation] != "true" {
		log.Debug("skip: pod is not marked for checkpoint-on-warm")
		return nil
	}
	fvID := pod.Annotations[FunctionVersionIDAnnotation]
	if fvID == "" {
		log.Warn("skip: pod has checkpoint-on-warm but no function-version annotation")
		return nil
	}
	log = log.WithField("functionVersionID", fvID)

	cfs, err := getOrCreateCFS(ctx, r.DynClient, fvID)
	if err != nil {
		return fmt.Errorf("reconciler: NvSnapFunctionState bootstrap failed: %w", err)
	}
	if optedOut(cfs) {
		log.Info("skip: function-version is opted out")
		return r.removeCheckpointOnWarm(ctx, pod, log) // stop watching this pod
	}

	// Persist this pod's workload-lookup inputs onto CFS.spec so the
	// controller's recovery sweep can flip Warm from an existing capture
	// even if THIS reconcile dies before writeStatus (nvca#104
	// durable-warm). Best-effort — a failure here only forgoes the sweep
	// safety net for this function-version; the live reconcile below is
	// unaffected. Done before the long poll so an interruption mid-poll
	// still leaves the inputs persisted.
	if c := findInferenceContainer(pod, r.InferenceContainerName); c != nil && c.Image != "" {
		if err := writeWorkloadLookup(ctx, r.DynClient, fvID, c.Image, extractModelID(c)); err != nil {
			log.WithError(err).Debug("could not persist spec.workloadLookup; recovery sweep won't cover this fvID until next reconcile")
		}
	}

	prev := readStatus(cfs)

	// 0a. Already-Warm short-circuit (nvca#178). The annotation alone
	// is not enough to decide "should I capture?" — CFS state is the
	// source of truth. Without this gate we observed a duplicate
	// capture on nvsnap-h100-a 2026-06-03:
	//
	//   T+0:00  capture #1 starts (pod transitions Ready=true)
	//   T+2:36  capture #1 finishes; writeStatus(Warm) + removeAnnotation
	//   T+2:36  CRIU's freeze→thaw caused kubelet to flip Ready=true
	//           AGAIN as the inference container resumed; that second
	//           Ready event was already queued in the workqueue
	//   T+2:36  capture #2 dispatched — annotation just removed but the
	//           informer-cached pod object that this Reconcile got still
	//           had it. Full duplicate ~3 min capture for the same hash.
	//
	// The failure backoff (nvca#167) doesn't help because the first
	// attempt succeeded (AttemptCount=0). The annotation guard at the
	// top of Reconcile() reads from the informer-cached pod object and
	// can lag. The CFS read above goes straight to the API server via
	// the dynamic client and is strongly consistent — that's the
	// trustworthy gate.
	//
	// removeCheckpointOnWarm is idempotent (no-op when annotation
	// already gone), so calling it here drains a stale annotation if
	// one is still around and stops the workqueue from re-firing.
	if prev.LocalCacheState == nvsnapv1alpha1.LocalCacheStateWarm {
		checkpointAttemptsSkippedWarm.Inc()
		capturedAt := "(unknown)"
		if prev.CapturedAt != nil {
			capturedAt = prev.CapturedAt.UTC().Format(time.RFC3339)
		}
		log.WithFields(logrus.Fields{
			"hash":        prev.CheckpointHash,
			"captured_at": capturedAt,
			"reason":      "cfs_already_warm",
		}).Info("skip: CFS LocalCacheState=Warm — duplicate trigger ignored (likely informer lag after a successful capture)")
		return r.removeCheckpointOnWarm(ctx, pod, log)
	}

	// 0b. Recovery short-circuit (nvca#104). CFS is not Warm, but a
	// usable capture may already exist — the previous reconcile could
	// have completed the dump+promote and then been interrupted before
	// writeStatus (controller restart, leader-election change, ctx
	// cancel, source-pod redeploy). Rather than re-capture, ask
	// nvsnap-server (the same content-addressed lookup Hook A uses) and,
	// on a usable match, flip CFS=Warm directly. Idempotent; also closes
	// the duplicate-capture race past the already-Warm gate (nvca#178/#189).
	// Runs before the failure-backoff gate so a real, usable capture
	// recovers even if a prior attempt was recorded as failed.
	if hash := r.recoverExistingCapture(ctx, pod, log); hash != "" {
		log.WithField("hash", hash).
			Info("recovery: usable capture already exists for this workload — flipping CFS=Warm without re-capture (nvca#104)")
		now := time.Now()
		if err := writeStatus(ctx, r.DynClient, fvID, statusUpdate{
			CheckpointHash:  hash,
			CapturedHere:    true,
			CapturedAt:      now,
			LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
			AttemptCount:    0,
			LastError:       "",
			LastAttemptAt:   now,
		}); err != nil {
			return fmt.Errorf("recovery: writeStatus Warm: %w", err)
		}
		return r.removeCheckpointOnWarm(ctx, pod, log)
	}

	// 0c. Per-function failure backoff (nvca#167). If the previous
	// attempt failed and we're still within the suppression window,
	// don't fire another capture. Without this, every pod-ready
	// event reconsiders — an incompatible workload gets hammered
	// indefinitely. AttemptCount is reset to 0 on success so a
	// healthy function never hits this path.
	if suppress, until := shouldSuppressAttempt(prev, time.Now()); suppress {
		checkpointAttemptsSuppressed.Inc()
		log.WithFields(logrus.Fields{
			"attempt_count":      prev.AttemptCount,
			"last_error":         prev.LastError,
			"last_attempt_at":    prev.LastAttemptAt.Time.UTC().Format(time.RFC3339),
			"next_attempt_after": until.UTC().Format(time.RFC3339),
			"backoff_window_sec": int64(until.Sub(prev.LastAttemptAt.Time).Seconds()),
		}).Warn("checkpoint attempt suppressed by failure backoff; function continues cold until window elapses")
		return nil
	}

	// 1. Workload-ready signal: defer to K8s PodReady.
	//
	// The pod's own readinessProbe (utils sidecar's httpGet against
	// the inference container's /health endpoint, per NVCA's
	// convention) already fires only when the engine is loaded and
	// serving. PodReady=true means K8s aggregated that signal and
	// considers the pod ready. Re-polling /health from here would
	// duplicate the same RTT, on the same port, against the same
	// endpoint — and would fail when the inference container has
	// no containerPort declared (which is the default for NVCF
	// function pods: NVCA stamps INFERENCE_PORT on the utils
	// sidecar's env, not on the inference container's ports[]).
	//
	// The controller already gates the workqueue on IsPodReady, so
	// this defensive check just protects against direct Reconcile()
	// calls (tests, future entrypoints) that bypass the controller.
	if !podReady(pod) {
		log.Debug("skip: pod not Ready; controller will re-enqueue on next event")
		return nil
	}

	// 1b. Capture-once claim (nvca#189 thundering-herd guard). With N
	// pods of the same function-version admitted cold, each reaches
	// here. Atomically transition CFS Cold→Capturing via Kubernetes
	// optimistic concurrency; exactly one reconcile wins and runs the
	// single capture. Losers observe the live claim and return without
	// firing a duplicate (multi-GB) CRIU dump. A crashed/hung owner's
	// claim expires after CaptureLeaseTTL and becomes stealable so the
	// function isn't pinned Capturing forever. Placed after the
	// podReady gate (only Ready pods capture) and before the warmup
	// buffer so losers skip immediately rather than each dwelling.
	claimNow := time.Now()
	owner := pod.Namespace + "/" + pod.Name
	claimed, err := tryClaimCapture(ctx, r.DynClient, fvID, owner, claimNow.Add(r.CaptureLeaseTTL), claimNow)
	if err != nil {
		return fmt.Errorf("capture-once claim for %s: %w", fvID, err)
	}
	if !claimed {
		checkpointAttemptsSkippedInflight.Inc()
		log.WithField("reason", "capture_in_flight").
			Info("skip: another pod holds the in-flight capture claim for this function-version (capture-once, nvca#189)")
		return nil
	}

	// 2. Dwell before snapshot so post-Ready setup completes.
	if r.WarmupBuffer > 0 {
		log.WithField("buffer", r.WarmupBuffer).Info("phase: warmup buffer")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.WarmupBuffer):
		}
	}

	// 3. POST checkpoint to nvsnap-server.
	log.Info("phase: POST nvsnap-server /api/v1/checkpoints")
	ckpt, err := r.NvSnapClient.CreateCheckpoint(ctx, nvsnap.CheckpointRequest{
		Namespace:     pod.Namespace,
		PodName:       pod.Name,
		ContainerName: r.InferenceContainerName,
		LeaveRunning:  true,
	})
	if err != nil {
		return r.recordFailure(ctx, fvID, "create_failed", prev, fmt.Errorf("nvsnap CreateCheckpoint: %w", err), log)
	}
	log = log.WithField("checkpointID", ckpt.ID)

	// 4. Poll GetCheckpoint until terminal (or checkpoint-timeout).
	pollCtx, pollCancel := context.WithTimeout(ctx, r.CheckpointTimeout)
	defer pollCancel()
	final, err := r.pollCheckpointTerminal(pollCtx, ckpt.ID, log)
	if err != nil {
		return r.recordFailure(ctx, fvID, "poll_failed", prev, err, log)
	}
	if final.Phase == nvsnap.PhaseFailed {
		return r.recordFailure(ctx, fvID, "completed_failed", prev,
			fmt.Errorf("nvsnap-server reported checkpoint %s Phase=Failed: %s", ckpt.ID, final.Error), log)
	}

	// Defense in depth (nvca#14 + nvca#15). nvsnap-server should always
	// return a non-empty content hash on a Completed checkpoint; an
	// empty hash means the agent or nvsnap-server has a hash-propagation
	// bug (see nvnvsnap#61). Behavior here used to be "return an error
	// and let controller-runtime requeue" — but that caused a retry
	// storm on nvsnap-h100-a (2026-05-31): 3 back-to-back 80 GB captures
	// looped before manual intervention, because every requeue POSTs a
	// fresh GPUCheckpoint job to nvsnap-server.
	//
	// Empty hash on Completed is a software bug, not a transient.
	// Treat it as if the capture never happened: log loudly, bump an
	// alertable counter so the operator notices, and return nil. The
	// CFS stays in its previous state (no Warm transition, no Failed
	// state-change either); the next pod-ready event will retry once.
	// The defense in depth — refusing to stamp restore-from="" — stays
	// in place; we just stop the storm.
	if final.Hash == "" {
		captureProtocolErrors.WithLabelValues("empty_hash").Inc()
		log.WithFields(logrus.Fields{
			"checkpointID": ckpt.ID,
			"phase":        final.Phase,
			"size":         final.Size,
			"duration":     final.Duration,
			"reason":       "empty_hash",
		}).Error("nvsnap-server returned Completed with empty hash — likely agent or nvsnap-server bug (see nvnvsnap#61). " +
			"Skipping CFS update; pod stays cold. Next pod-ready event will retry.")
		return nil
	}

	// 5. L2 promote-state gate (nvca#179). The CRIU checkpoint reached
	// terminal Completed, but the agent's L2 snap+clone runs ASYNC —
	// rox-<hash> may not be Bound yet (or may have failed). If we
	// flip CFS=Warm now, future function pods get nvsnap.io/restore-from
	// and admit, but the rox PVC reference points at a not-yet-ready
	// (or never-going-to-be-ready) PVC. Restore stalls or fails.
	//
	// Solution: poll nvsnap-server's /pvc-state until the agent reaches
	// a terminal L2 state. Three outcomes:
	//   - state=ready: L2 done, fall through to write CFS=Warm
	//   - state=failed: L2 promote terminally failed. Record the
	//     failure on CFS but DON'T flip Warm — future pods will
	//     cold-start (no nvsnap.io/restore-from stamped).
	//   - state="" (or 404): agent has no L2 backend configured.
	//     Fall through to write CFS=Warm — restore uses L1/peer
	//     cascade, no L2 to wait on.
	//
	// Time budget: capped by PromotePollTimeout (default 15 min,
	// covers the largest captures we've seen — Qwen3-32B ~6 min
	// snap+clone on Hyperdisk-ML). A timeout records the partial
	// state on CFS as "L2 stalled" so the operator can see it; it
	// does NOT crash the reconcile — checkpoint capture was a
	// success, we just don't get the L2 fan-out benefit this round.
	promoteCtx, promoteCancel := context.WithTimeout(ctx, r.PromotePollTimeout)
	defer promoteCancel()
	promote, err := r.pollPVCPromoteTerminal(promoteCtx, final.Hash, log)
	if err != nil {
		// Transient/timeout — record but proceed cold. The capture
		// itself succeeded; the L2 fan-out simply won't be available
		// for the first restore wave. Future captures will retry.
		log.WithError(err).WithField("hash", final.Hash).
			Warn("L2 promote-state poll did not reach terminal — falling back to cold start (capture itself succeeded)")
		return r.recordFailure(ctx, fvID, "promote_poll_failed", prev,
			fmt.Errorf("L2 promote poll: %w", err), log)
	}
	switch promote.State {
	case "failed":
		// L2 promote terminally failed (Job error, snapshot failure,
		// rox PVC bind failure). Capture is durable on L1 / peer
		// cascade, but no L2 fan-out for this hash. Function pods
		// will admit without restore-from and cold-start; next
		// capture attempt may succeed L2 (e.g. transient SC issue).
		log.WithFields(logrus.Fields{
			"hash":     final.Hash,
			"pvc_name": promote.PVCName,
		}).Warn("L2 promote terminally failed — pods will cold-start until next successful capture")
		return r.recordFailure(ctx, fvID, "promote_failed", prev,
			fmt.Errorf("L2 promote state=failed for hash %s", final.Hash), log)
	case "ready":
		log.WithFields(logrus.Fields{
			"hash":     final.Hash,
			"pvc_name": promote.PVCName,
		}).Info("L2 promote ready — rox PVC bound, safe to flip CFS=Warm")
	default:
		// Empty state or terminal-but-no-L2-configured. The capture
		// is on L1 / peer cascade only; restore will work via the
		// existing fallback path. Proceed to flip Warm.
		log.WithField("hash", final.Hash).
			Info("L2 not in use for this capture (no agent backend configured) — flipping CFS=Warm via peer-cascade fallback")
	}

	// 6. Success — update CFS, drop the annotation.
	now := time.Now()
	if err := writeStatus(ctx, r.DynClient, fvID, statusUpdate{
		CheckpointHash:  final.Hash,
		CapturedHere:    true,
		CapturedAt:      now,
		LocalCacheState: nvsnapv1alpha1.LocalCacheStateWarm,
		AttemptCount:    0,
		LastError:       "",
		LastAttemptAt:   now,
	}); err != nil {
		log.WithError(err).Error("status update failed after successful checkpoint; will retry on next reconcile")
		return err
	}
	log.WithFields(logrus.Fields{
		"hash":     final.Hash,
		"size":     final.Size,
		"duration": final.Duration,
	}).Info("checkpoint committed")

	return r.removeCheckpointOnWarm(ctx, pod, log)
}

// pollCheckpointTerminal polls nvsnap-server for the checkpoint's
// terminal phase. Returns the final Checkpoint when reached, or an
// error if the context expires, the API returns a non-transient
// failure (404 = checkpoint no longer exists; 4xx other than 429 =
// permanent client error), or transient transport errors persist
// past the context deadline.
//
// Greptile P2 (nvca!1748): the old behavior was to silently retry
// every error — including a permanent 404 — until CheckpointTimeout
// (default 30 min) expired. A nvsnap-server that lost the row, a
// retention sweep that deleted the in-flight checkpoint, or a
// bad-id bug would all spin instead of failing fast.
func (r *Reconciler) pollCheckpointTerminal(ctx context.Context, id string, log logrus.FieldLogger) (*nvsnap.Checkpoint, error) {
	ticker := time.NewTicker(r.CheckpointPollInterval)
	defer ticker.Stop()
	for {
		c, err := r.NvSnapClient.GetCheckpoint(ctx, id)
		if err != nil {
			if isNonTransientAPIError(err) {
				return nil, fmt.Errorf("poll checkpoint %s: non-transient error: %w", id, err)
			}
			// Transient (transport error, 5xx, 429). Log + keep polling
			// until the context deadline.
			log.WithError(err).Debug("GetCheckpoint failed; will retry until deadline")
		} else if c.IsTerminal() {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("poll checkpoint %s: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// pollPVCPromoteTerminal polls nvsnap-server's /pvc-state until the
// agent's async L2 snap+clone reaches a terminal state (ready,
// failed, or "" / 404 = no L2 backend). Returns the final state on
// terminal, or an error on context expiry / non-transient API
// failure.
//
// Symmetric to pollCheckpointTerminal — same retry policy, same
// fail-fast on permanent 4xx, same transient-error swallow on
// transport hiccups. The only difference: 404 on /pvc-state is a
// terminal "no L2" signal (caller proceeds with cold-start /
// peer-cascade restore), NOT a fatal error.
func (r *Reconciler) pollPVCPromoteTerminal(ctx context.Context, hash string, log logrus.FieldLogger) (*nvsnap.PVCPromoteState, error) {
	ticker := time.NewTicker(r.PromotePollInterval)
	defer ticker.Stop()
	for {
		s, err := r.NvSnapClient.GetPVCPromoteState(ctx, hash)
		if err != nil {
			var apiErr *nvsnap.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				// 404 → catalog has no row for this hash. Agent has no
				// L2 backend configured for this capture. Return a
				// synthetic "no L2" state so the caller falls through
				// to the cold/peer-cascade fallback without erroring.
				log.WithField("hash", hash).
					Debug("pvc-state 404 — agent has no L2 backend; proceed without L2")
				return &nvsnap.PVCPromoteState{Hash: hash, State: ""}, nil
			}
			if isNonTransientAPIError(err) {
				return nil, fmt.Errorf("poll pvc-state %s: non-transient error: %w", hash, err)
			}
			log.WithError(err).Debug("GetPVCPromoteState failed; will retry until deadline")
		} else if s.IsTerminal() {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("poll pvc-state %s: %w", hash, ctx.Err())
		case <-ticker.C:
		}
	}
}

// isNonTransientAPIError returns true when err is an *APIError whose
// status code indicates the request will never succeed on retry
// (4xx, except 429 which IS transient). Transport errors (network
// failures) are treated as transient by definition.
func isNonTransientAPIError(err error) bool {
	var apiErr *nvsnap.APIError
	if !errors.As(err, &apiErr) {
		return false // transport error → transient
	}
	if apiErr.StatusCode == http.StatusTooManyRequests {
		return false
	}
	return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// recordFailure writes the error into NvSnapFunctionState.status, logs
// the full failure context at Error level, bumps an alertable counter,
// and returns nil so controller-runtime does NOT requeue. The next
// pod-ready event will decide whether to re-attempt — we explicitly
// do NOT trigger a tight retry loop here.
//
// Why no requeue (instead of returning the error like a normal
// reconciler would): a checkpoint attempt is heavy (an 80 GB CRIU
// dump can take 2-3 minutes; an L2 promote can pile on another
// 5-8 minutes of GCP storage operations). controller-runtime's
// rate-limited requeue would re-fire every ~30 seconds — fast enough
// to wedge the source pod's GPU under repeat capture pressure but
// slow enough that the operator wouldn't notice for hours. Empirically
// reproduced on nvsnap-h100-a 2026-06-03: a single L2 timeout drove
// 3 back-to-back full captures of the same hash before manual
// intervention. The defense-in-depth pattern (nvca#15) for the
// empty-hash case is the same: log loudly, count, return nil.
//
// AttemptCount on CFS is still incremented so operators can see how
// many attempts a given function has burned across pod-ready events.
//
// `reason` is a small enum that lands in the metric label — keep the
// set bounded (see metrics.go).
func (r *Reconciler) recordFailure(ctx context.Context, fvID, reason string, prev cfsStatus, cause error, log logrus.FieldLogger) error {
	now := time.Now()
	upd := statusUpdate{
		CheckpointHash:  prev.CheckpointHash,
		CapturedHere:    prev.CapturedHere,
		LocalCacheState: prev.LocalCacheState,
		AttemptCount:    prev.AttemptCount + 1,
		LastError:       cause.Error(),
		LastAttemptAt:   now,
	}
	// Forward CapturedAt from prior status. Without this, every
	// failed re-checkpoint wipes the timestamp set by an earlier
	// successful capture, hiding it from operators debugging a CFS
	// that was once Warm.
	if prev.CapturedAt != nil {
		upd.CapturedAt = prev.CapturedAt.Time
	}
	if err := writeStatus(ctx, r.DynClient, fvID, upd); err != nil {
		log.WithError(err).Error("failed to write failure status; original error preserved in log")
	}
	checkpointAttemptFailures.WithLabelValues(reason).Inc()
	// Loud, structured log so the operator has everything in one line:
	// reason, full error chain, attempt count, prior CFS state. Error
	// level (not Warn) because every entry is an attempt that won't be
	// retried — the operator must look.
	log.WithError(cause).WithFields(logrus.Fields{
		"reason":           reason,
		"attempt_count":    upd.AttemptCount,
		"prev_cache_state": prev.LocalCacheState,
		"prev_hash":        prev.CheckpointHash,
		"function_version": fvID,
		"swallowed_retry":  true,
	}).Error("checkpoint attempt failed; NOT retrying — next pod-ready event will reconsider")
	// Return nil: no requeue. The pod stays running cold; controller-
	// runtime drops the work item. Defense-in-depth against the
	// retry storm pattern (nvca#15).
	return nil
}

// removeCheckpointOnWarm patches the pod to drop the annotation, so
// a re-reconcile (e.g. controller restart) doesn't checkpoint again.
// Failure to patch is logged but not fatal — the next reconciler can
// observe the warm cache state and skip via Hook A's gating instead.
func (r *Reconciler) removeCheckpointOnWarm(ctx context.Context, pod *corev1.Pod, log logrus.FieldLogger) error {
	if _, ok := pod.Annotations[CheckpointOnWarmAnnotation]; !ok {
		return nil
	}
	// Strategic-merge-patch with null deletes the annotation key.
	patch := []byte(`{"metadata":{"annotations":{"nvsnap.io/checkpoint-on-warm":null}}}`)
	_, err := r.KubeClient.CoreV1().Pods(pod.Namespace).Patch(
		ctx, pod.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Pod is gone — nothing to do.
			return nil
		}
		log.WithError(err).Warn("failed to remove nvsnap.io/checkpoint-on-warm annotation")
		return err
	}
	return nil
}
