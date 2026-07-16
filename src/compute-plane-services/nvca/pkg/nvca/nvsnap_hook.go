/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nvca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// inferenceContainerName is the canonical name of the inference
// container in an NVCF pod. Hook A reads its image/env/args to
// build the content-addressed lookup inputs.
const inferenceContainerName = "inference"

// Annotation keys NVCA stamps on workload pods to drive the NvSnap
// integration. NvSnap's mutating webhook reads RestoreFromAnnotation
// at admission time; Hook B's reconciler (PR-5) watches for pods
// with CheckpointOnWarmAnnotation to know which to checkpoint after
// readiness.
const (
	// NvSnapRestoreFromAnnotation, when present and non-empty, tells
	// NvSnap's mutating webhook to inject the restore mounts for
	// the given content-addressed checkpoint hash. NvSnap's webhook
	// resolves the hash to a manifest ConfigMap, picks a node where
	// the cache lives (or override target node), and injects the
	// rootfs volume + sitecustomize plumbing.
	NvSnapRestoreFromAnnotation = "nvsnap.io/restore-from"

	// NvSnapCheckpointOnWarmAnnotation marks pods that NVCA's
	// post-Ready reconciler (Hook B, PR-5) should checkpoint after
	// the warmup window. NVCA stamps this at pod-create time on
	// every function-version pod whose NvSnapFunctionState is not
	// opted out, so the reconciler doesn't need to re-resolve the
	// FV state at every Ready event.
	NvSnapCheckpointOnWarmAnnotation = "nvsnap.io/checkpoint-on-warm"

	// NvSnapFunctionVersionIDAnnotation is the bridge between
	// ICMSRequest-driven Hook A (which knows the FV id from
	// req.Spec.FunctionDetails) and the Pod-watching reconciler
	// (which doesn't see the originating ICMSRequest). Stamped at
	// the same time as CheckpointOnWarmAnnotation.
	NvSnapFunctionVersionIDAnnotation = "nvsnap.io/function-version-id"
)

// nvsnapFunctionStateGVR is the cluster-scoped CR holding per-cluster
// NvSnap state for one NGC function-version. Defined in
// pkg/apis/nvsnap/v1alpha1.
var nvsnapFunctionStateGVR = nvsnapv1alpha1.SchemeGroupVersion.WithResource("nvsnapfunctionstates")

// stampNvSnapAnnotations is Hook A (restore-on-create). For each pod
// CreatePodArtifactInstances is about to apply, if the NvSnap
// integration is enabled and the function-version has a usable
// checkpoint cached locally, stamp nvsnap.io/restore-from on the pod
// so NvSnap's webhook injects the restore mounts. Always stamps
// nvsnap.io/checkpoint-on-warm when not opted out so Hook B can pick
// up newly-Ready pods.
//
// Fail-open: any error short-circuits to "no stamping" — the pod is
// applied unchanged and cold-starts as before. The NvSnap integration
// is an optimization, never a gate.
//
// Gating (any false short-circuits):
//   - featureflag.NvSnapCheckpointRestore is the global kill switch.
//   - NvSnapFunctionState.spec.optOut is the per-function-version
//     opt-out.
//   - Per-cluster ClusterConfig.NvSnap gating is TODO; will land when
//     the NVCFBackend CR → NVCA agent runtime config wiring is
//     extended (separate PR). Today the global flag is the only
//     non-FV gate.
//
// req.Spec.FunctionDetails.FunctionVersionID is the canonical key
// for NvSnapFunctionState lookup — one CR per NGC function-version.
func (c K8sComputeBackend) stampNvSnapAnnotations(ctx context.Context, pod *corev1.Pod, req *nvcav2beta1.ICMSRequest, log *logrus.Entry) {
	if !featureflag.NvSnapCheckpointRestore.Enabled() {
		return
	}

	// Hook A span covers: CFS lookup, optional content-addressed
	// lookup RPC to nvsnap-server, annotation stamping decision. The
	// span attribute "nvsnap.decision" is set right before return —
	// stamped_restore_from | stamped_checkpoint_on_warm | skipped
	// — so trace consumers can quickly group by what Hook A
	// decided per admission.
	tracer := nvcaotel.NewTracer(nvcaotel.WithName("nvca.nvsnap.hook_a"))
	ctx, span := tracer.Start(ctx, "nvsnap.hook_a.stamp",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			otelattr.String("k8s.pod.name", pod.Name),
			otelattr.String("k8s.pod.namespace", pod.Namespace),
			// pod.UID is empty at admission time (assigned by apiserver
			// after the webhook returns), so we skip it here.
		),
	)
	defer span.End()

	fvID := req.Spec.FunctionDetails.FunctionVersionID
	if fvID == "" {
		span.SetAttributes(otelattr.String("nvsnap.decision", "skipped_no_fvid"))
		return // no function-version, nothing to look up
	}
	span.SetAttributes(otelattr.String("nvsnap.function_version_id", fvID))

	cfsObj, err := c.dynClient.Resource(nvsnapFunctionStateGVR).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).WithField("functionVersionID", fvID).
			Debug("nvsnap: NvSnapFunctionState lookup failed; pod will cold-start")
		return
	}
	if apierrors.IsNotFound(err) {
		// First-touch for this function-version. Without this branch
		// we'd have a chicken-and-egg: Hook A skips stamping
		// checkpoint-on-warm because no CFS exists, Hook B never
		// sees a pod with the annotation so no checkpoint fires,
		// CFS never gets created. Materialize an empty CFS at
		// state=Cold so the next steps stamp the annotation and Hook B
		// can pick it up after PodReady.
		cfsObj, err = createInitialCFS(ctx, c.dynClient, fvID)
		if err != nil {
			log.WithError(err).WithField("functionVersionID", fvID).
				Debug("nvsnap: failed to create initial NvSnapFunctionState; pod will cold-start")
			return
		}
		log.WithField("functionVersionID", fvID).
			Info("nvsnap: created initial NvSnapFunctionState at Cold")
	}

	if optOut, _, _ := unstructured.NestedBool(cfsObj.Object, "spec", "optOut"); optOut {
		log.WithField("functionVersionID", fvID).Debug("nvsnap: function-version opted out; pod will cold-start")
		span.SetAttributes(otelattr.String("nvsnap.decision", "skipped_optout"))
		return
	}

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	// FunctionVersionID is needed in both branches below — Hook A
	// (the nvsnap webhook reads it back when resolving the restore
	// manifest) and Hook B (the reconciler watches Pods, not
	// ICMSRequests, so it reads FV id off the pod).
	pod.Annotations[NvSnapFunctionVersionIDAnnotation] = fvID

	hash, _, _ := unstructured.NestedString(cfsObj.Object, "status", "checkpointHash")
	state, _, _ := unstructured.NestedString(cfsObj.Object, "status", "localCacheState")

	if hash != "" && state == string(nvsnapv1alpha1.LocalCacheStateWarm) {
		// Fast path: this function-version already has its own
		// checkpoint cached locally. Stamp restore-from; do NOT
		// stamp checkpoint-on-warm, otherwise the restore pod would
		// also be queued by Hook B and N restored replicas would each
		// POST a redundant CreateCheckpoint, overwriting each other's
		// hash in CFS. (Greptile P2 on MR !1698.) Refresh-driven
		// re-checkpoint is a separate flow (CFS-level state machine,
		// not per-pod), so unconditional re-stamping here is wrong.
		//
		// Self-heal (nvca#190): the artifact can be deleted out from
		// under us — UI delete, retention GC, cross-cluster eviction —
		// without anything resetting this CFS (the nvsnap-side delete
		// cascade owns nvsnap tiers, not NVCA's CFS). A stale Warm then
		// makes us stamp restore-from for a hash whose bytes are gone:
		// the pod silently cold-starts AND never re-captures (stuck
		// Warm forever). So verify the artifact still exists on
		// nvsnap-server before honoring Warm. Fail-OPEN: a transient
		// nvsnap-server error must not break otherwise-valid restores.
		stale := false
		if c.nvsnapClient != nil {
			vctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			exists, checkErr := nvsnapArtifactExists(vctx, c.nvsnapClient, hash)
			cancel()
			if checkErr != nil {
				log.WithError(checkErr).WithFields(logrus.Fields{
					"functionVersionID": fvID, "hash": hash,
				}).Debug("nvsnap: artifact existence check failed; failing open (honoring Warm)")
			} else if !exists {
				stale = true
			}
		}
		if stale {
			// Definitive: nvsnap-server has no record of this hash. Reset
			// CFS to Cold and fall through to the cold/capture path so
			// Hook B re-captures. Do NOT stamp restore-from.
			if err := resetCFSToCold(ctx, c.dynClient, fvID); err != nil {
				log.WithError(err).WithField("functionVersionID", fvID).
					Warn("nvsnap: failed to reset stale-Warm CFS to Cold; pod will still cold-start")
			}
			span.SetAttributes(
				otelattr.String("nvsnap.decision", "warm_artifact_missing_recapture"),
				otelattr.String("nvsnap.hash", hash),
			)
			log.WithFields(logrus.Fields{
				"functionVersionID": fvID,
				"hash":              hash,
			}).Warn("nvsnap: CFS=Warm but nvsnap-server has no artifact for hash; reset to Cold, will re-capture")
			// fall through to the cold/capture path below (no return).
		} else {
			pod.Annotations[NvSnapRestoreFromAnnotation] = hash
			span.SetAttributes(
				otelattr.String("nvsnap.decision", "stamped_restore_from_fvid"),
				otelattr.String("nvsnap.hash", hash),
			)
			log.WithFields(logrus.Fields{
				"functionVersionID": fvID,
				"hash":              hash,
			}).Info("nvsnap: stamped nvsnap.io/restore-from (fvID-keyed)")
			return
		}
	}

	// Content-addressed dedup (nvnvsnap#59). The fvID-keyed lookup
	// missed — either this is a true cold start, or this fvID is new
	// but another fvID with the same canonical content has already
	// captured. Ask nvsnap-server "do you have a checkpoint for this
	// (image, model, flags, driver)?" — if yes, restore from it
	// instead of cold-starting + re-capturing.
	if match := c.lookupContentAddressedMatch(ctx, pod, fvID, log); match != nil {
		pod.Annotations[NvSnapRestoreFromAnnotation] = match.Hash
		span.SetAttributes(
			otelattr.String("nvsnap.decision", "stamped_restore_from_content_addressed"),
			otelattr.String("nvsnap.hash", match.Hash),
			otelattr.String("nvsnap.source_checkpoint_id", match.CheckpointID),
		)
		log.WithFields(logrus.Fields{
			"functionVersionID":  fvID,
			"hash":               match.Hash,
			"sourceCheckpointID": match.CheckpointID,
			"sourceNode":         match.CapturedOnNode,
		}).Info("nvsnap: stamped nvsnap.io/restore-from (content-addressed dedup across fvIDs)")
		// Update our CFS to Warm with the dedup'd hash so subsequent
		// pods for THIS fvID hit the fvID-keyed fast path above
		// (avoids one lookup RPC per admission once we know).
		if err := markCFSWarmFromLookup(ctx, c.dynClient, fvID, match.Hash); err != nil {
			// Non-fatal — the annotation is already stamped on this
			// pod; the next admission will just re-do the lookup.
			log.WithError(err).Debug("nvsnap: failed to persist dedup'd hash into CFS; will re-lookup next admission")
		}
		return
	}

	// True cold start. Hook B trigger — cache is Cold / Fetching /
	// Failed / absent and no content-addressed match exists either.
	pod.Annotations[NvSnapCheckpointOnWarmAnnotation] = "true"
	span.SetAttributes(otelattr.String("nvsnap.decision", "stamped_checkpoint_on_warm"))
}

// lookupContentAddressedMatch queries nvsnap-server for a checkpoint
// whose canonical content matches this pod's spec. Returns the
// freshest match if any, nil otherwise (or on any error — fail-open,
// the pod just cold-starts as before).
//
// Inputs derived from the pod:
//   - imageRef: the "inference" container's image string (NVCF
//     convention; falls back to pod.Spec.Containers[0] if absent)
//   - modelID: pulled from container env (NIM_MODEL_NAME,
//     HF_MODEL_ID, etc.) or --model args (vLLM/SGLang style)
//   - engineFlags: container.Args minus --model* (server canonicalizes)
//   - driverMajor: best-effort from node label; left 0 (= match any)
//     when we can't resolve it cheaply, so we don't gate the dedup
//     on perfect node info
func (c K8sComputeBackend) lookupContentAddressedMatch(ctx context.Context, pod *corev1.Pod, fvID string, log *logrus.Entry) *nvsnap.LookupMatch {
	if c.nvsnapClient == nil {
		return nil
	}
	target := inferenceContainer(pod)
	if target == nil || target.Image == "" {
		// No inference container, no canonical inputs — can't dedup.
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, err := c.nvsnapClient.LookupCheckpoints(lookupCtx, nvsnap.LookupRequest{
		ImageRef:    target.Image,
		ModelID:     extractModelID(target),
		EngineFlags: canonicalizeArgs(target.Args),
		// DriverMajor left 0 → match any driver. Production wiring
		// will populate this from a node-label cache once the node
		// the pod will land on is known; admission-time we don't
		// have that, and partial-match-better-than-no-match.
	})
	if err != nil {
		log.WithError(err).WithField("functionVersionID", fvID).
			Debug("nvsnap: content-addressed lookup failed; pod will cold-start")
		return nil
	}
	if len(resp.Matches) == 0 {
		return nil
	}
	return &resp.Matches[0]
}

// createInitialCFS materializes a NvSnapFunctionState at state=Cold for
// the given function-version. Used by Hook A's first-touch path:
// before this, Hook A skipped stamping checkpoint-on-warm when no CFS
// existed, which left the function in a chicken-and-egg state with
// Hook B (no annotation -> no Hook B fire -> no checkpoint -> no CFS
// status update). Mirrors the controller-side getOrCreateCFS in
// pkg/nvca/nvsnap/reconciler/state.go (deliberately duplicated rather
// than imported, to avoid an import cycle: pkg/nvca depends on
// pkg/nvca/nvsnap/* via nvsnap_controller_start.go, and reconciler
// importing pkg/nvca would close the cycle).
//
// AlreadyExists on race: re-read the existing object — another
// caller materialized it between our Get and Create, the state we
// want is already there.
func createInitialCFS(ctx context.Context, dc dynamic.Interface, fvID string) (*unstructured.Unstructured, error) {
	fresh := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": nvsnapv1alpha1.SchemeGroupVersion.String(),
			"kind":       "NvSnapFunctionState",
			"metadata":   map[string]any{"name": fvID},
			"spec": map[string]any{
				"functionVersionID": fvID,
			},
			"status": map[string]any{
				"localCacheState": string(nvsnapv1alpha1.LocalCacheStateCold),
			},
		},
	}
	created, err := dc.Resource(nvsnapFunctionStateGVR).Create(ctx, fresh, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if apierrors.IsAlreadyExists(err) {
		return dc.Resource(nvsnapFunctionStateGVR).Get(ctx, fvID, metav1.GetOptions{})
	}
	return nil, fmt.Errorf("create NvSnapFunctionState %s: %w", fvID, err)
}

// inferenceContainer returns the canonical inference container in
// pod.Spec.Containers — defaults to the one named "inference" (NVCF
// convention), falls back to the first GPU-requesting container,
// then the first container. Returns nil only if Containers is empty.
func inferenceContainer(pod *corev1.Pod) *corev1.Container {
	if pod == nil {
		return nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == inferenceContainerName {
			return &pod.Spec.Containers[i]
		}
	}
	for i := range pod.Spec.Containers {
		if _, ok := pod.Spec.Containers[i].Resources.Limits["nvidia.com/gpu"]; ok {
			return &pod.Spec.Containers[i]
		}
	}
	if len(pod.Spec.Containers) > 0 {
		return &pod.Spec.Containers[0]
	}
	return nil
}

// extractModelID inspects the container's env + args for the
// well-known model-identifier keys (NIM, vLLM, SGLang, TRT-LLM, HF).
// Mirrors the agent-side helper in nvsnap's internal/agent/catalog.go
// so both sides extract the same value from the same pod spec.
func extractModelID(c *corev1.Container) string {
	envKeys := []string{
		"NIM_MODEL_NAME",
		"MODEL_NAME",
		"MODEL_REPO",
		"HF_MODEL_ID",
		"HUGGINGFACE_MODEL",
	}
	for _, e := range c.Env {
		for _, key := range envKeys {
			if e.Name == key && e.Value != "" {
				return e.Value
			}
		}
	}
	for i, arg := range c.Args {
		if arg == "--model" || arg == "--model-path" {
			if i+1 < len(c.Args) {
				return c.Args[i+1]
			}
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
		if strings.HasPrefix(arg, "--model-path=") {
			return strings.TrimPrefix(arg, "--model-path=")
		}
	}
	return ""
}

// canonicalizeArgs strips --model* tokens (recorded separately as
// modelID) and sorts the remainder. Same logic the nvsnap agent and
// nvsnap-server use; the server canonicalizes again on its side, so
// any drift between the two implementations self-corrects to "no
// match". Mirroring keeps the round-trip free of false negatives.
func canonicalizeArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if a == "--model" || a == "--model-path" {
			skip = true
			continue
		}
		if strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "--model-path=") {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// markCFSWarmFromLookup persists a content-addressed dedup hit into
// NvSnapFunctionState.status. Result: subsequent pods for this fvID
// take the fvID-keyed fast path in stampNvSnapAnnotations and skip the
// lookup RPC. capturedHere stays false — we didn't capture, we
// borrowed someone else's checkpoint.
//
// Mirrors writeStatus semantics from pkg/nvca/nvsnap/reconciler/state.go
// but avoids importing reconciler to dodge the import cycle (same
// rationale as createInitialCFS).
// nvsnapArtifactExists reports whether nvsnap-server still has a catalog
// record for hash. Returns (false,nil) on a definitive 404, (true,nil)
// when present, and (false,err) on any other error so callers can
// fail-open (a transient nvsnap-server blip must not invalidate Warm).
func nvsnapArtifactExists(ctx context.Context, client *nvsnap.Client, hash string) (bool, error) {
	if _, err := client.GetCheckpoint(ctx, hash); err != nil {
		var apiErr *nvsnap.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// resetCFSToCold flips a stale Warm NvSnapFunctionState back to Cold and
// clears the dangling checkpoint hash, so the next admission stamps
// checkpoint-on-warm and Hook B re-captures. Mirrors markCFSWarmFromLookup's
// UpdateStatus/Update fallback for CRDs without a /status subresource.
func resetCFSToCold(ctx context.Context, dc dynamic.Interface, fvID string) error {
	cur, err := dc.Resource(nvsnapFunctionStateGVR).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["localCacheState"] = string(nvsnapv1alpha1.LocalCacheStateCold)
	status["checkpointHash"] = ""
	status["capturedHere"] = false
	status["lastError"] = ""
	if err := unstructured.SetNestedField(cur.Object, status, "status"); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if _, err := dc.Resource(nvsnapFunctionStateGVR).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
			if _, err := dc.Resource(nvsnapFunctionStateGVR).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update NvSnapFunctionState %s: %w", fvID, err)
			}
			return nil
		}
		return fmt.Errorf("update status %s: %w", fvID, err)
	}
	return nil
}

func markCFSWarmFromLookup(ctx context.Context, dc dynamic.Interface, fvID, hash string) error {
	cur, err := dc.Resource(nvsnapFunctionStateGVR).Get(ctx, fvID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get NvSnapFunctionState %s: %w", fvID, err)
	}
	status, _, _ := unstructured.NestedMap(cur.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["checkpointHash"] = hash
	status["localCacheState"] = string(nvsnapv1alpha1.LocalCacheStateWarm)
	status["capturedHere"] = false
	status["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
	status["lastError"] = ""
	if err := unstructured.SetNestedField(cur.Object, status, "status"); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if _, err := dc.Resource(nvsnapFunctionStateGVR).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
			// CRD without /status subresource — fall back to full Update.
			// Same known TOCTOU on spec as state.go writeStatus; same fix
			// (registering subresource:status) resolves both.
			if _, err := dc.Resource(nvsnapFunctionStateGVR).Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update NvSnapFunctionState %s: %w", fvID, err)
			}
			return nil
		}
		return fmt.Errorf("update status %s: %w", fvID, err)
	}
	return nil
}
