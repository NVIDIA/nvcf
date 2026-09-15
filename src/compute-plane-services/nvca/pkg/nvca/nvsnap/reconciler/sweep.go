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

// sweep.go — the pod-independent CFS recovery sweep (nvca#104
// durable-warm). See docs/users/nvsnap/DURABLE-WARM-SWEEP.md.
//
// The happy-path Warm flip and the recoverExistingCapture short-circuit
// both run inside a pod-triggered reconcile. If the reconcile that
// captured a checkpoint dies before writeStatus (controller restart,
// lost goroutine, source pod scaled away mid-poll) AND no new
// checkpoint-on-warm pod ever appears for that function-version — e.g.
// the next pod is a restore attempt, or NVCF scaled the function to
// zero — nothing re-evaluates the CFS and it stays not-Warm forever,
// silently cold-starting every restore.
//
// SweepOnce closes that gap: it reconciles every NvSnapFunctionState
// against nvsnap-server (the source of truth for "does a usable capture
// exist") with no dependence on a live pod, flipping Warm for any CFS
// whose capture has landed.

package reconciler

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvsnapv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvsnap/v1alpha1"
)

// SweepOnce runs one pass of the CFS recovery sweep: list every
// NvSnapFunctionState and, for each that is not Warm, not opted out, and
// carries persisted workload-lookup inputs, ask nvsnap-server whether a
// usable capture exists and flip LocalCacheState=Warm if so. Idempotent
// and best-effort — list/lookup/write errors are logged and the sweep
// continues to the next CFS. Never returns an error (the controller
// just runs it again next tick).
func (r *Reconciler) SweepOnce(ctx context.Context) {
	r.applyDefaults()
	log := r.Log.WithField("component", "nvsnap-cfs-sweep")

	list, err := r.DynClient.Resource(CFSResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Debug("sweep: list NvSnapFunctionState failed; will retry next tick")
		return
	}

	for i := range list.Items {
		cfs := &list.Items[i]
		fvID := cfs.GetName()

		st := readStatus(cfs)
		if st.LocalCacheState == nvsnapv1alpha1.LocalCacheStateWarm {
			continue // already Warm — nothing to recover
		}
		if optedOut(cfs) {
			continue
		}
		imageRef, modelID := readWorkloadLookup(cfs)
		if imageRef == "" {
			continue // never persisted (CFS predates the writer, or Hook B
			// died before persisting) — can't look up without inputs
		}

		hash := r.findUsableCapture(ctx, imageRef, modelID, log)
		if hash == "" {
			continue // no usable capture yet — leave for a future tick
		}

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
			log.WithError(err).WithField("functionVersionID", fvID).
				Warn("sweep: writeStatus Warm failed; will retry next tick")
			continue
		}
		cfsSweepRecovered.Inc()
		log.WithFields(logrus.Fields{
			"functionVersionID": fvID,
			"hash":              hash,
			"prev_state":        st.LocalCacheState,
		}).Info("sweep: recovered a usable capture and flipped CFS=Warm without a live pod (nvca#104 durable-warm)")
	}
}
