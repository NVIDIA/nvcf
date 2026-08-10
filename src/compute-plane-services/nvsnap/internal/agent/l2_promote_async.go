/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Async L2 promote (nvsnap#166).
//
// Before: the capture handler called a.l2Backend.Put synchronously,
// holding open the HTTP response to nvsnap-server for the duration of
// writer Job + VolumeSnapshot wait + rox PVC create. For e5-mistral
// (88 GB / ~7 min snap+clone) this raced nvsnap-server's 10-minute
// httpClient.Timeout and lost: the agent's actual capture succeeded,
// but nvsnap-server saw a context-deadline-exceeded and marked the
// GPUCheckpoint CRD Failed, triggering NVCA to consider a fresh
// capture on the next pod-ready event. Pod cycle → capture again →
// timeout again → loop.
//
// The fix is structural: capture durability (CRIU dump + catalog row +
// peer-add) is the contract that earns Phase=Completed. L2 promote is
// an *optimisation* layer on top — restore-side falls back to the
// peer cascade if L2 isn't ready yet. So we make L2 promote async:
// the agent returns 200 the moment durability is achieved, and the
// promote runs in a background goroutine with its own context. State
// is observable via the catalog row's pvc_promote_state column
// (written by the Backend's CatalogStateWriter), which nvsnap-init
// already polls before mounting the rox PVC.
//
// Panic safety: a panic in the goroutine MUST NOT crash the agent
// DaemonSet (which would take every other capture on this node with
// it). We recover, log, and let the state machine show "failed".

package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// l2PromoteTimeout caps the background promote at 35 minutes — long
// enough for the largest checkpoints (deepseek-v4-flash tier on
// Hyperdisk-ML snap+clone) without leaking goroutines if the writer
// Job hangs.
const l2PromoteTimeout = 35 * time.Minute

// l2PromoteInput is the immutable subset of capture state the
// background goroutine needs. Passing a single struct (instead of
// captured closure variables) makes the data flow explicit at the
// call site and trivial to mock in tests.
type l2PromoteInput struct {
	Hash        string
	HostDumpDir string            // source dir on the host (translated from checkpointDir)
	PodMeta     map[string]string // SourcePodMeta for Manifest
	CapturedAt  time.Time
}

// runL2PromoteAsync runs the per-capture-PVC promote in a background
// goroutine. The caller's HTTP request has already returned by the
// time the goroutine runs; do not capture request-scoped context or
// logger fields that may have been recycled.
//
// Safe to call with backend==nil — it short-circuits silently. That's
// the legitimate "no L2 backend configured" path (filestore-only
// clusters, single-node dev, etc).
func runL2PromoteAsync(backend checkpointstore.Backend, log *logrus.Entry, in l2PromoteInput) {
	if backend == nil {
		return
	}
	if in.Hash == "" || in.HostDumpDir == "" {
		log.WithFields(logrus.Fields{
			"hash":        in.Hash,
			"hostDumpDir": in.HostDumpDir,
		}).Warn("L2 promote skipped: empty hash or hostDumpDir")
		return
	}

	go func() {
		// Recover unconditionally — a panic in promote (e.g. nil
		// pointer in the Backend implementation, k8s client closed)
		// must not bring down the agent. The catalog state writer
		// will leave the row in whatever state preceded the panic;
		// restore-side falls back to peer cascade.
		defer func() {
			if r := recover(); r != nil {
				log.WithFields(logrus.Fields{
					"hash":  checkpointstore.ShortHash(in.Hash),
					"panic": r,
					"stack": string(debug.Stack()),
				}).Error("L2 promote goroutine PANIC — capture is durable, L2 marked failed via state machine")
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), l2PromoteTimeout)
		defer cancel()

		// New root span: the originating HTTP request has already
		// returned, so this is its own trace (the promote runs detached).
		ctx, span := tracing.Tracer().Start(ctx, "l2.promote")
		span.SetAttributes(attribute.String("nvsnap.hash", checkpointstore.ShortHash(in.Hash)))
		defer span.End()

		src := checkpointstore.CaptureSource{
			Kind:       checkpointstore.SourceKindCRIU,
			SrcPath:    in.HostDumpDir,
			DstSubpath: "",
		}
		manifest := checkpointstore.Manifest{
			Hash:          in.Hash,
			CapturedAt:    in.CapturedAt,
			SourcePodMeta: in.PodMeta,
		}

		start := time.Now()
		log.WithField("hash", checkpointstore.ShortHash(in.Hash)).
			Info("L2 promote starting (async)")

		if _, err := backend.Put(ctx, in.Hash, []checkpointstore.CaptureSource{src}, manifest); err != nil {
			span.RecordError(err)
			// Best-effort: state writer already wrote "failed" before
			// returning the error. Restore-side resolver checks
			// pvc_promote_state and falls back to peer cascade.
			log.WithError(err).WithFields(logrus.Fields{
				"hash":     checkpointstore.ShortHash(in.Hash),
				"duration": fmt.Sprintf("%.1fs", time.Since(start).Seconds()),
			}).Warn("L2 promote failed — capture is durable; restore falls back to peer cascade")
			return
		}

		log.WithFields(logrus.Fields{
			"hash":     checkpointstore.ShortHash(in.Hash),
			"rox":      "rox-" + checkpointstore.ShortHash(in.Hash),
			"duration": fmt.Sprintf("%.1fs", time.Since(start).Seconds()),
		}).Info("L2 promote complete — multi-node restore can mount rox PVC")
	}()
}
