/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// recover.go — the nvca#104 recovery short-circuit for Hook B.
//
// The happy-path Warm flip is reachable only by ONE uninterrupted
// reconcile: warmup → POST → pollCheckpointTerminal → pollPVCPromoteTerminal
// → writeStatus(Warm). pollPVCPromoteTerminal can block for minutes
// (Hyperdisk-ML snap+clone). If that reconcile is interrupted AFTER the
// capture succeeds but BEFORE writeStatus — controller restart, leader
// election change, ctx cancel, the source pod being redeployed, or an
// earlier transient error that returned — CFS stays not-Warm. The only
// existing short-circuit is "already Warm", so the next reconcile would
// re-capture from scratch (and, in the worst case where nothing
// re-triggers it, CFS never goes Warm → silent cold start).
//
// recoverExistingCapture closes that gap: before POSTing a fresh
// capture, ask nvsnap-server (the same content-addressed lookup Hook A
// uses for restore) whether a usable capture already exists for this
// pod's workload identity. If so, the caller flips CFS=Warm directly —
// idempotent, no re-dump. This also closes the duplicate-capture race
// (nvca#178/#189): a capture that already exists is never re-taken.

package reconciler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
)

// recoverExistingCapture returns the content hash of a usable existing
// capture for this pod's workload identity, or "" if none exists or
// none is usable yet (so the caller proceeds with the normal capture
// flow). Best-effort: any lookup error returns "" — recovery never
// blocks a fresh capture.
func (r *Reconciler) recoverExistingCapture(ctx context.Context, pod *corev1.Pod, log logrus.FieldLogger) string {
	c := findInferenceContainer(pod, r.InferenceContainerName)
	if c == nil || c.Image == "" {
		return ""
	}
	return r.findUsableCapture(ctx, c.Image, extractModelID(c), log)
}

// findUsableCapture asks nvsnap-server whether a usable capture exists for
// the given workload identity (image + model) and returns its content
// hash, or "" if none exists or none is usable yet. Best-effort: any
// lookup error returns "". Shared by the pod-triggered recovery
// short-circuit (recoverExistingCapture) and the controller's
// pod-independent CFS sweep (SweepOnce) so both use one definition of
// "usable".
func (r *Reconciler) findUsableCapture(ctx context.Context, imageRef, modelID string, log logrus.FieldLogger) string {
	if imageRef == "" {
		return ""
	}
	resp, err := r.NvSnapClient.LookupCheckpoints(ctx, nvsnap.LookupRequest{
		ImageRef: imageRef,
		ModelID:  modelID,
	})
	if err != nil {
		log.WithError(err).Debug("recovery: lookup failed; proceeding with normal capture flow")
		return ""
	}
	if resp == nil || len(resp.Matches) == 0 {
		return ""
	}
	// Matches are freshest-first. Take the newest whose L2 promote is
	// terminal-usable: "ready" (rox bound) or no-L2 (404 / "" — the
	// capture lives on L1 / peer cascade and Hook A restores via the
	// fallback path). A "failed" or still-in-progress promote is NOT
	// recoverable here — leave it to the normal flow.
	for _, m := range resp.Matches {
		if m.Hash == "" {
			continue
		}
		ps, err := r.NvSnapClient.GetPVCPromoteState(ctx, m.Hash)
		if err != nil {
			var apiErr *nvsnap.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				return m.Hash // no L2 row → usable via L1/peer cascade
			}
			log.WithError(err).WithField("hash", m.Hash).
				Debug("recovery: pvc-state check failed; skipping this match")
			continue
		}
		switch ps.State {
		case "ready", "":
			return m.Hash
		default: // "failed" | "pending" | "writing" | "snapshotting"
			continue
		}
	}
	return ""
}

// findInferenceContainer returns the container named `name`, falling
// back to the first GPU-requesting container, then the first. Mirrors
// pkg/nvca/nvsnap_hook.go::inferenceContainer — duplicated here to avoid
// an import cycle with package nvca.
func findInferenceContainer(pod *corev1.Pod, name string) *corev1.Container {
	if pod == nil {
		return nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
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

// extractModelID inspects the container's env + args for the well-known
// model-identifier keys. Mirrors pkg/nvca/nvsnap_hook.go::extractModelID
// (same import-cycle reason).
func extractModelID(c *corev1.Container) string {
	for _, e := range c.Env {
		switch e.Name {
		case "NIM_MODEL_NAME", "MODEL_NAME", "MODEL_REPO", "HF_MODEL_ID", "HUGGINGFACE_MODEL":
			if e.Value != "" {
				return e.Value
			}
		}
	}
	for i, arg := range c.Args {
		if (arg == "--model" || arg == "--model-path") && i+1 < len(c.Args) {
			return c.Args[i+1]
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
