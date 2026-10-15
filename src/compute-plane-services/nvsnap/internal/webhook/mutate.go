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

// Package webhook implements the customer-facing surface for transparent
// rootfs-only restore: a mutating admission webhook that injects cache
// volumes + mounts into pods marked with the nvsnap.io/restore-from
// annotation. See docs/MULTI-GPU-ROOTFS-FANOUT-DESIGN.md §3.5.
//
// This file is the pure-logic Mutator (pod in → JSON Patch ops out). The
// HTTPS / cert / AdmissionReview transport layer lives separately so the
// mutation rules are unit-testable without a running server.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// RestoreFromAnnotation is the customer-facing opt-in. Value forms:
//
//	"auto"           webhook computes the hash from the pod spec
//	"<hash>"         pin to a specific full sha256 hex (rollback / debug)
//	"<short-hash>"   12-hex-char prefix; resolved by the backend
const RestoreFromAnnotation = "nvsnap.io/restore-from"

// TargetNodeAnnotation overrides the auto-pin to manifest.CapturedOnNodes.
// When set, the webhook injects nodeAffinity to this single node instead.
// Used for cross-node restore demos (showcases tier-2/tier-3 cascade fetch)
// and any case where the operator wants control over scheduling rather than
// letting nvsnap pin to the capture-source node.
//
// If the named node does not actually have the capture cached, the agent's
// EnsureCaptureLocal cascade kicks in: tier-2 peer fetch from a node in
// CapturedOnNodes, falling back to tier-3 blobstore.
const TargetNodeAnnotation = "nvsnap.io/target-node"

// CaptureLabel is the opt-in marker for capture-side injection. cachedir
// capture (the /opt/nvsnap emptyDir + cache-env redirect) is only injected
// into pods carrying nvsnap.io/capture: "true" — the SAME label the rootfs
// capture watcher gates on (rootfsonly.DefaultCaptureLabel). Without it the
// webhook would inject into every cold pod in scope, including unrelated
// system/infra pods (e.g. helm-chart NVCF miniservice pods), which both
// pollutes those pods and can wedge controllers that re-apply the pod spec
// (volume-order change → forbidden pod update). Opt-in keeps nvsnap generic:
// no orchestrator-specific knowledge — whoever deploys (a human, Helm, or
// NVCA, which knows container-function vs helm/miniservice) sets the label
// only on workloads it wants captured. Default (no label) = no injection.
const CaptureLabel = "nvsnap.io/capture"

// PatchOp is one entry in a JSON Patch (RFC 6902) document.
//
// K8s admission webhooks return base64(JSON([]PatchOp)) in
// AdmissionResponse.Patch + PatchType=JSONPatch. Encoding lives in the
// HTTP layer.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// mergeableArrayRe matches the per-container appendable arrays whose
// bootstrap (`add <array> []`) the auto-inject and restore builders each
// compute independently from the original pod.
var mergeableArrayRe = regexp.MustCompile(`^/spec/containers/\d+/(volumeMounts|env)$`)

// isMergeableArray reports whether path addresses one of the pod arrays
// that multiple builders may bootstrap (so the plan must be normalized).
func isMergeableArray(path string) bool {
	return path == "/spec/volumes" || path == "/spec/initContainers" || mergeableArrayRe.MatchString(path)
}

// patchElementName pulls the "name" field out of a patch value (volume,
// volumeMount, initContainer, env). JSON round-trip so it works for both
// map[string]any (auto-inject) and typed corev1 values (restore builders).
// Returns "" when there's no name (then the element can't be deduped).
func patchElementName(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var named struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &named) != nil {
		return ""
	}
	return named.Name
}

// mergePatchPlan reconciles the concatenation of auto-inject + restore
// patches so they don't clobber each other (nvsnap#93). Both sides build
// array bootstraps from the ORIGINAL pod, so a pod with empty spec.volumes
// (or empty main-container volumeMounts/env) gets TWO `add <array> [..]`
// ops — and under JSON Patch the second REPLACES the first, dropping the
// auto-injected nvsnap-lib volume/mount. The same arrays can also receive a
// duplicate element (two nvsnap-lib volumes).
//
// Normalization, preserving order:
//   - the FIRST bootstrap `add <array> [..]` for a mergeable array is kept
//     (its own elements deduped by name);
//   - any LATER bootstrap of the same array is rewritten into per-element
//     `add <array>/- <elem>` appends;
//   - `add <array>/- <elem>` ops are dropped when an element of that name
//     was already emitted for the array.
//
// Non-mergeable paths (command, args, securityContext, annotations, …) and
// non-add ops pass through untouched.
func mergePatchPlan(patches []PatchOp) []PatchOp {
	bootstrapped := map[string]bool{}    // array path -> already has a bootstrap add
	seen := map[string]map[string]bool{} // array path -> element names emitted
	markSeen := func(arr, name string) {
		if name == "" {
			return
		}
		if seen[arr] == nil {
			seen[arr] = map[string]bool{}
		}
		seen[arr][name] = true
	}
	out := make([]PatchOp, 0, len(patches))
	for _, p := range patches {
		if p.Op != "add" {
			out = append(out, p)
			continue
		}
		// Element-append form: /spec/volumes/- etc.
		if base := strings.TrimSuffix(p.Path, "/-"); base != p.Path && isMergeableArray(base) {
			name := patchElementName(p.Value)
			if name != "" && seen[base][name] {
				continue // duplicate element (e.g. a second nvsnap-lib) — drop
			}
			markSeen(base, name)
			out = append(out, p)
			continue
		}
		// Whole-array bootstrap form: /spec/volumes with a slice value.
		if isMergeableArray(p.Path) {
			elems, ok := p.Value.([]any)
			if !ok {
				out = append(out, p) // not the bootstrap shape we manage
				continue
			}
			if !bootstrapped[p.Path] {
				bootstrapped[p.Path] = true
				kept := make([]any, 0, len(elems))
				for _, e := range elems {
					name := patchElementName(e)
					if name != "" && seen[p.Path][name] {
						continue
					}
					markSeen(p.Path, name)
					kept = append(kept, e)
				}
				out = append(out, PatchOp{Op: "add", Path: p.Path, Value: kept})
				continue
			}
			// Array already exists — append each fresh element instead of
			// replacing the array.
			for _, e := range elems {
				name := patchElementName(e)
				if name != "" && seen[p.Path][name] {
					continue
				}
				markSeen(p.Path, name)
				out = append(out, PatchOp{Op: "add", Path: p.Path + "/-", Value: e})
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// Mutator decides what to inject into a pod based on its
// nvsnap.io/restore-from annotation.
//
// Concurrency: safe; the underlying Backend must be safe for concurrent
// Stat/Mount calls (Local is, GPD-ROX in #99 will be).
type Mutator struct {
	// Backend is the L1/L3 storage backend (rootfs hostPath + peer
	// cascade). Required. Must satisfy checkpointstore.Backend (Store
	// + Mounter).
	Backend checkpointstore.Backend

	// L2Backend is the optional per-capture PVC backend (nvsnap#63).
	// When set, Mutate calls its Mount() first; on success the
	// pod gets a PVC volumeMount instead of the L1 hostPath +
	// nodeAffinity. On ErrNotFound (rox-<hash> not Bound yet, or no
	// L2 promote for this hash) the mutator falls through to the
	// L1 path.
	L2Backend checkpointstore.Backend

	// L2MountPath is where the rox-<hash> PVC mounts inside the
	// restored pod's main container. restore-entrypoint reads from
	// $CHECKPOINT_PATH, which the mutator points at this directory.
	// Default "/nvsnap-checkpoint".
	L2MountPath string

	// CacheDir enables "cachedir" mode (the ember cache-reuse path) when
	// non-empty (e.g. "/opt/nvsnap"). On capture pods the webhook injects
	// the cache/model env vars + an emptyDir here; on restore pods whose
	// manifest.CaptureMethod=="cachedir" it mounts the rox read-only here
	// directly (no overlayfs) + the prewarm shim. MUST match the agent's
	// --pod-cache-dir. Empty = standard rootfs/criu paths only.
	CacheDir string

	// CacheEnvFile is the path to a mounted ConfigMap file holding the
	// cachedir env template (NAME=value lines, {root}/{cache}/{model}
	// placeholders). Read on CAPTURE inject only — lets ops add/remove
	// cache env vars without an agent rebuild. Empty or unreadable →
	// built-in default template (fail-safe). Restore never reads it; it
	// replays the env stamped into the manifest at capture. Wired from
	// the agent's --cachedir-env-file flag (a mounted ConfigMap).
	CacheEnvFile string

	// L2WaitImage is the nvsnap-l2-wait init-container image ref
	// (nvsnap#147). When non-empty, tryL2Mount prepends a
	// nvsnap-l2-wait init container that polls nvsnap-server's
	// /pvc-state endpoint until the L2 promote reaches a terminal
	// state. Without it the rox PVC reference would be in the pod
	// spec but kubelet would try to mount it before the agent's
	// snap+clone finished — racing the WaitForFirstConsumer
	// binder. Empty disables the inject (back-compat for clusters
	// where nvsnap-l2-wait isn't deployed yet); pods will Pending
	// until the rox PVC binds, which works on already-promoted
	// hashes but stalls on in-flight ones.
	L2WaitImage string

	// NvSnapServerURL is the in-cluster URL the nvsnap-l2-wait init
	// container POSTs to. Typically
	// http://nvsnap-server.<ns>.svc.cluster.local:8080. Empty
	// disables the L2 wait inject (paired with empty L2WaitImage).
	NvSnapServerURL string

	// L2WaitTimeout caps the nvsnap-l2-wait deadline. Empty defaults
	// to the binary's own 15 min default. Operators bump this for
	// very large checkpoints (~200 GiB+ on the slow default
	// snapshot class — not the hdml-images one).
	L2WaitTimeout string

	// HostBundleRoot is the on-host directory where the nvsnap-agent
	// DaemonSet stages the restore bundle. Function pods mount
	// {root}/nvsnap + {root}/nvsnap-lib via hostPath. Empty → default
	// "/var/lib/nvsnap/bundle" (DefaultHostBundleRoot in
	// restore_entrypoint.go).
	//
	// Changing this path requires coordinated edits in:
	//   - the agent DaemonSet's host-bundle hostPath volume
	//   - the DaemonSet's nvsnap-bundle-stage initContainer env vars
	//   - this field
	// Operators don't get a Helm knob, by design: a single
	// config value can't safely span all three.
	HostBundleRoot string

	// Composer translates a pod spec to a checkpointstore.HashInput when
	// the annotation value is "auto". Required for "auto" handling; if
	// nil and the annotation is "auto", Mutate fails open (returns nil,
	// nil) so a misconfigured webhook doesn't break admission.
	Composer *rootfsonly.HashInputComposer

	// MainContainer is the index of the container the cache is injected
	// into. Most nvsnap workloads are single-container (0).
	MainContainer int

	// EnsureLocal makes the capture's bytes available to the local
	// Backend BEFORE Backend.Mount is called. Without this, a webhook
	// call that lands on a node which doesn't have the capture cached
	// locally will fail Mount and the pod cold-starts. The agent wires
	// its own *agent.Agent.EnsureCaptureLocal here; on lookup miss it
	// peer-fetches from any node in Manifest.CapturedOnNodes before
	// returning. Nil is allowed — the Mutator skips the call and the
	// admission either succeeds (local hit) or fails open (Mount error).
	EnsureLocal func(ctx context.Context, hash string) error

	// Log is the structured logger; nil disables logging.
	Log logrus.FieldLogger

	// AutoInject configures the image refs used when the webhook
	// auto-injects sitecustomize plumbing for pods carrying the
	// nvsnap.io/auto-inject: "true" annotation. Empty/zero = the
	// auto-inject branch is a no-op (failing open).
	AutoInject AutoInjectImages

	// OverlayPreparer hands the webhook a per-restore-pod writable
	// OverlayFS union layered on top of any captured volume — both
	// rootfs-extract subpaths (nvsnap#194) AND hostPath/emptyDir
	// user-data volumes (nvsnap#88). Without it, those injections still
	// work but the resulting bind-mount is read-only — workloads that
	// write into the captured tree crash with EROFS. When set, the
	// webhook calls PrepareOverlay synchronously during admission, then
	// substitutes the merged overlay path into the patched volume so
	// kubelet's eventual bind picks up the union. Defined as an
	// interface (rather than *agent.OverlayManager) to keep the webhook
	// package free of an agent import.
	OverlayPreparer OverlayPreparer

	// RestorePrepStrategy chooses how restore-side OverlayFS mounts
	// are prepared (nvsnap#202).
	//
	//   "inline" (default): webhook calls PrepareOverlay
	//     synchronously during admission. Simple but blows the K8s
	//     mutating-webhook timeout budget for workloads with many
	//     extract paths (DeepSeek-V4-Flash produced 355 mounts ~70 s).
	//   "init-container": webhook emits a nvsnap-mount-prep init
	//     container into the restored pod plus hostPath volumes
	//     pointing at the deterministic merged paths. The init
	//     container POSTs the prep request to the agent's
	//     /v1/restore/prep, polls until ready, then exits. Mount
	//     work happens in pod lifecycle (minutes-long budget) instead
	//     of admission (seconds-long budget). Failure modes surface
	//     in kubectl describe pod, not silently.
	//
	// Wired through Helm: nvsnap.webhook.restorePrepStrategy.
	RestorePrepStrategy string

	// MountPrepInitImage is the image ref for the nvsnap-mount-prep
	// init container the init-container strategy injects. Required
	// when RestorePrepStrategy is "init-container"; ignored
	// otherwise. Typically matches the nvsnap-agent version so the
	// init binary and agent API are in lockstep.
	MountPrepInitImage string

	// AgentHostPort is the agent's HTTP listen port (default 8081).
	// The nvsnap-mount-prep init container reads it from NVSNAP_AGENT_URL
	// = http://<host-ip>:<AgentHostPort>; host-ip is plumbed via
	// downward API in the patched pod spec.
	AgentHostPort int
}

// OverlayPreparer is implemented by *agent.Agent via PrepareOverlay.
// The webhook calls it during pod admission for each captured volume
// (rootfs-extract subpath OR hostPath/emptyDir user-data volume); the
// returned path is the merged OverlayFS mountpoint to bind into the
// pod, in place of the lower captured tree.
//
// targetNode is the node where the captured tree lives and where the
// restored pod will be scheduled (via nodeAffinity). When the
// admitting agent is on a different node, the implementation routes
// the prepare call via peer HTTP to the target node's agent so the
// overlay is mounted where kubelet will actually bind it. Empty
// targetNode = local prep on the admitting agent (single-node test
// path).
type OverlayPreparer interface {
	PrepareOverlay(podUID, captureHash string, vol checkpointstore.VolumeMeta, targetNode string) (string, error)

	// MountpointFor returns the merged-overlay hostPath path WITHOUT
	// doing any mount work. Used by the init-container strategy
	// (nvsnap#202) so the webhook can emit pod-spec hostPath
	// volumes referencing paths the nvsnap-mount-prep init container
	// will later make ready. Pure function of (overlayRoot, podUID,
	// vol.MountPath) — never returns an error for valid inputs in
	// the production *agent.Agent implementation.
	MountpointFor(podUID string, vol checkpointstore.VolumeMeta) (string, error)
}

// Mutate returns the patch ops to apply to pod, or nil patches when the
// pod should be admitted unchanged.
//
// Fail-open philosophy: any internal error returns (nil, err) — the HTTP
// layer logs and admits the pod unchanged. The customer's pod must
// always be admittable; the webhook is an *optimization*, never a gate.
//
// Returns nil patches (no error) when:
//   - annotation absent or empty
//   - annotation is "auto" but Composer is nil (misconfig — fail open)
//   - resolved hash has no matching capture in the backend (cold start path)
func (m *Mutator) Mutate(ctx context.Context, pod *corev1.Pod) ([]PatchOp, error) {
	if pod == nil {
		return nil, nil
	}
	if m.Backend == nil {
		return nil, errors.New("webhook: Mutator.Backend is nil")
	}

	ctx, span := tracing.Tracer().Start(ctx, "webhook.mutate")
	defer span.End()
	if pod.Namespace != "" {
		span.SetAttributes(attribute.String("nvsnap.pod", pod.Namespace+"/"+pod.Name))
	}

	// Auto-inject sitecustomize plumbing first so the restore-from
	// branch sees a pod that already has /nvsnap-lib volume + mounts
	// + env vars. Both can run on the same pod (auto-injected
	// boilerplate + restore mounts).
	injectPatches := m.autoInjectPatches(pod)

	raw, ok := pod.Annotations[RestoreFromAnnotation]
	if !ok || raw == "" {
		// No restore-from — this is a CAPTURE/cold pod. In cachedir mode,
		// inject the cache/model env vars + the /opt/nvsnap emptyDir so the
		// engine funnels its caches there and the agent captures that dir.
		// No-op when cachedir mode is off. (Restore pods get the rox mount
		// + env from the cachedir restore path below, not this.)
		injectPatches = append(injectPatches, m.cacheDirCapturePatches(pod)...)
		// Return whatever auto-inject + cachedir-capture produced (may be
		// nil, which is fine — pod admitted unchanged).
		if len(injectPatches) > 0 {
			m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).
				WithField("patches", len(injectPatches)).
				Info("auto-inject only")
		}
		return injectPatches, nil
	}

	span.SetAttributes(attribute.Bool("nvsnap.restore", true))
	resolveCtx, resolveSpan := tracing.Tracer().Start(ctx, "webhook.resolve_hash")
	hash, err := m.resolveHash(resolveCtx, pod, raw)
	if err != nil {
		resolveSpan.RecordError(err)
		resolveSpan.End()
		return nil, fmt.Errorf("resolve hash: %w", err)
	}
	resolveSpan.End()
	if hash == "" {
		return nil, nil
	}
	span.SetAttributes(attribute.String("nvsnap.hash", hash))

	// Resolve the manifest so the L2 dispatch can branch on the capture
	// TYPE, not merely on "does a rox PVC exist". A rootfs capture
	// promoted to an L2 PVC is a filesystem tree, NOT a CRIU dump —
	// routing it to the CRIU restore-entrypoint hangs the pod forever
	// waiting for inventory.img. See docs/design/ROOTFS-RESTORE-INJECTION.md.
	//
	// ErrNotFound is tolerated here, NOT fatal: a CRIU capture lives only
	// on the L2 PVC, so the L1/ConfigMap Backend may legitimately not have
	// a manifest. In that case we can't read the type — preserve the
	// historical behavior (treat as CRIU: tryL2Mount → restore-entrypoint),
	// and only cold-admit after the L2 path also misses.
	manifest, statErr := m.Backend.Stat(ctx, hash)
	if statErr != nil && !errors.Is(statErr, checkpointstore.ErrNotFound) {
		return nil, fmt.Errorf("backend stat: %w", statErr)
	}
	haveManifest := statErr == nil
	// Dispatch on the AUTHORITATIVE capture method stamped at capture
	// time (v0.0.56). Fall back to the shape heuristic only for
	// pre-v0.0.56 manifests with no CaptureMethod set.
	rootfs := haveManifest && (manifest.CaptureMethod == "rootfs" ||
		(manifest.CaptureMethod == "" && manifestIsRootfs(manifest)))
	// cachedir: the rox is a single cache+model tree; mount it RO at
	// m.CacheDir directly (no overlay) + prewarm shim (ember path).
	cachedir := haveManifest && manifest.CaptureMethod == "cachedir"

	// L2 per-capture rox PVC fast path — the fan-out hero. The rox PVC is
	// ReadOnlyMany so the SAME PVC mounts into N restore pods across N
	// nodes, with NO node pin and no per-node copy. Two capture types,
	// two restore mechanisms off the same PVC:
	//   - CRIU dump: read-only at restore (restore-entrypoint mmaps the
	//     image files) → mount the rox at /nvsnap-checkpoint directly.
	//   - rootfs: the engine WRITES into its warmed cache/model dirs, so
	//     wrap the rox in a per-pod OverlayFS (rox RO lower + emptyDir
	//     upper) — fan-out AND writable. See rootfs_l2_overlay.go.
	// Either falls through to the L1 host-overlay path (buildPatches,
	// pinned to the capture node) on ErrNotFound (rox not Bound).
	if m.L2Backend != nil {
		var patches []PatchOp
		var l2err error
		path := "l2-pvc-criu"
		switch {
		case cachedir:
			path = "l2-pvc-cachedir"
			patches, l2err = m.tryL2CacheDir(ctx, pod, hash, manifest)
		case rootfs:
			path = "l2-pvc-rootfs-overlay"
			patches, l2err = m.tryL2RootfsOverlay(ctx, pod, hash, manifest)
		default:
			patches, l2err = m.tryL2Mount(ctx, pod, hash)
		}
		if l2err == nil && patches != nil {
			patches = mergePatchPlan(append(injectPatches, patches...))
			m.logger().WithFields(logrus.Fields{
				"hash":    checkpointstore.ShortHash(hash),
				"pod":     pod.Namespace + "/" + pod.Name,
				"patches": len(patches),
				"path":    path,
			}).Info("L2 PVC mount injected")
			return patches, nil
		}
		// ErrNotFound (rox not Bound) → fall through to L1 below. Other
		// errors logged.
		if l2err != nil && !errors.Is(l2err, checkpointstore.ErrNotFound) {
			m.logger().WithError(l2err).WithField("hash", checkpointstore.ShortHash(hash)).
				Debug("L2 mount failed; falling back to L1 hostPath path")
		}
	}

	// L1 path needs the manifest. If L1 doesn't have it (CRIU-only-on-L2
	// that also missed the L2 branch, or a truly absent capture), cold-start.
	if !haveManifest {
		m.logger().WithField("hash", checkpointstore.ShortHash(hash)).
			Info("no capture for hash; admitting pod unchanged (cold start)")
		return nil, nil
	}

	// Note: we do NOT peer-fetch the capture bytes here. The webhook
	// answers AdmissionReview within a 5s timeout, far too short to
	// move a 100GB+ rootfs across nodes. Instead the Mutator emits a
	// nodeAffinity patch (below, via nodeAffinityPatches) pinning the
	// scheduled pod to a node in Manifest.CapturedOnNodes — where the
	// bytes already live. Kubelet on that node resolves the injected
	// hostPath volumeMounts against its own local /var/lib/nvsnap/cache.
	// EnsureLocal remains a hook on Mutator for "any GPU node" flows
	// where the scheduler may pick a node without the data and an
	// async init container needs to pull it before main starts — that
	// path is not exercised by today's CapturedOnNodes-pinned demo.
	if m.EnsureLocal != nil {
		if errEL := m.EnsureLocal(ctx, hash); errEL != nil {
			return nil, fmt.Errorf("ensure-local cascade: %w", errEL)
		}
	}

	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}

	patches, err := m.buildPatches(ctx, pod, hash, manifest)
	if err != nil {
		return nil, err
	}
	// Prepend any auto-inject patches so they're applied first by the
	// API server (in-order JSON Patch application). Restore mount
	// patches reference indices that auto-inject may also touch
	// (initContainers list, env list); doing inject-then-restore keeps
	// path math straightforward.
	patches = mergePatchPlan(append(injectPatches, patches...))
	if len(patches) > 0 {
		m.logger().WithFields(logrus.Fields{
			"hash":        checkpointstore.ShortHash(hash),
			"pod":         pod.Namespace + "/" + pod.Name,
			"patches":     len(patches),
			"auto_inject": len(injectPatches),
		}).Info("rootfs-only mutation applied")
	}
	return patches, nil
}

// resolveHash maps the annotation value to a full hash:
//   - "auto" → compose HashInput from pod, hash it
//   - "<full-or-short>" → canonicalize to the full sha256 via Backend.Stat
//     so all downstream callers (nvsnap-mount-prep, overlay paths, agent
//     /v1/captures endpoints, peer cascade) see the same form.
//     Concretely: the on-disk capture cache uses the full 64-char hash
//     (internal/checkpointstore/local.go), but the CM NAME uses the
//     short 32-char hash (DNS-1123 length limit). If we pass a short
//     hash to the init container, it builds <cache_dir>/<short>/...
//     which never exists — and the customer's restore fails with
//     "lowerDir ...: no such file or directory" (observed 2026-06-08
//     on whisper rootfs restore). Stat returns the canonical Manifest
//     whose Hash field is always the full form, regardless of input.
func (m *Mutator) resolveHash(ctx context.Context, pod *corev1.Pod, raw string) (string, error) {
	var seed string
	switch raw {
	case "auto":
		if m.Composer == nil {
			m.logger().Warn("annotation is 'auto' but Composer is nil; failing open")
			return "", nil
		}
		seed = checkpointstore.ComputeHash(m.Composer.Compose(pod, m.MainContainer))
	default:
		seed = raw
	}
	// Try to canonicalize to the full sha256 via Backend.Stat. On
	// success the caller gets manifest.Hash (always full) and passes it
	// downstream — nvsnap-mount-prep then finds the cache at
	// <cache_dir>/<full_hash>/... instead of <short>/... which doesn't
	// exist (observed 2026-06-08 on whisper rootfs restore).
	//
	// On ErrNotFound we return `seed` unchanged so the cold-start path
	// downstream still gets to inject the restore-bundle (nvsnap-tools +
	// nvsnap-lib hostPath, command rewrite, securityContext). Those
	// patches must apply regardless of capture presence — the pod waits
	// for the cascade to populate the cache. The next Stat call at the
	// rootfs-mount branch will then re-confirm ErrNotFound and admit
	// the pod unchanged for the rootfs path while restore-bundle
	// patches still fire.
	if manifest, err := m.Backend.Stat(ctx, seed); err == nil {
		return manifest.Hash, nil
	} else if !errors.Is(err, checkpointstore.ErrNotFound) {
		return "", err
	}
	return seed, nil
}

// buildPatches constructs the JSON Patch ops to transparently restore a
// captured workload into a customer pod, with two streams of inputs:
//
//  1. manifest.Volumes  — captured user-data volumes (hostPath / emptyDir).
//     The customer already has these volumes declared; we mount the
//     captured copy at the same MountPath, shadowing whatever was there.
//
//  2. manifest.RootfsExtractPaths — engine cache subpaths discovered
//     inside the rootfs upperdir at capture time (HF cache, Triton
//     cache, NIM workspace, etc.). Customer's pod yaml does NOT declare
//     these; the webhook adds the volume + readOnly mount itself,
//     shadowing the rootfs view of the path so the engine reads our
//     cached version.
//
// Whole-rootfs entries (Type="rootfs") are skipped — rootfs upperdir
// injection requires init-container tar-x + entrypoint wrapping; the
// catalog-driven RootfsExtractPaths flow covers the cacheable subset
// without modifying the customer's container.
//
// Customer-mount conflict policy: if the customer's main container
// already has a volumeMount at our target MountPath, skip our injection
// for that path. Customer intent always wins. The capture is then a
// no-op for that path, which is safe (cold-start fallback).
//
// Path-existence handling: K8s API server tolerates `add` to a missing
// array path by creating it. To keep behavior deterministic across
// admission engines, we still bootstrap empty slices when missing.
func (m *Mutator) buildPatches(
	ctx context.Context, pod *corev1.Pod, hash string, manifest checkpointstore.Manifest,
) ([]PatchOp, error) {
	var patches []PatchOp

	if m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range", m.MainContainer)
	}
	main := pod.Spec.Containers[m.MainContainer]

	// Build the set of MountPaths the customer's main container already
	// uses. Our injections must NOT collide — customer intent wins.
	customerMountPaths := make(map[string]struct{}, len(main.VolumeMounts))
	for _, vm := range main.VolumeMounts {
		customerMountPaths[vm.MountPath] = struct{}{}
	}

	// Bootstrap missing slices so subsequent `add /-` ops are unambiguous.
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{}})
	}
	if main.VolumeMounts == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer),
			Value: []any{},
		})
	}
	needInitArray := pod.Spec.InitContainers == nil
	bootstrappedInit := false

	// addedVolumes dedupes spec.volumes entries by Volume.Name. K8s
	// kubelet pod-worker treats each spec.volumes entry independently, but
	// CSI plugins mount once per (PV, pod). When two extract paths share
	// the same backing PVC (GPD-ROX backend: every mount on hash H goes
	// through the same reader PVC), emitting two spec.volumes with the
	// same name confuses pod-worker — kubelet creates ONE mount dir, then
	// loops "unmounted volumes=[<name>]" forever. Dedupe by name; emit
	// the volumeMount (with its subPath) every time.
	addedVolumes := map[string]struct{}{}

	emitMount := func(vm checkpointstore.VolumeMeta) error {
		// Conflict check: customer already mounts something here? Skip.
		if _, conflict := customerMountPaths[vm.MountPath]; conflict {
			m.logger().WithFields(logrus.Fields{
				"hash":       checkpointstore.ShortHash(hash),
				"mountPath":  vm.MountPath,
				"volumeName": vm.Name,
				"volumeType": vm.Type,
			}).Info("skip injection: customer pod already has volumeMount at this path")
			return nil
		}
		pm, err := m.Backend.Mount(ctx, hash, vm)
		if err != nil {
			return fmt.Errorf("backend mount %s (%s): %w", vm.Name, vm.MountPath, err)
		}
		if _, dupe := addedVolumes[pm.Volume.Name]; !dupe {
			patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: pm.Volume})
			addedVolumes[pm.Volume.Name] = struct{}{}
		}
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: pm.VolumeMount,
		})
		for i := range pm.InitContainers {
			if needInitArray && !bootstrappedInit {
				patches = append(patches, PatchOp{
					Op: "add", Path: "/spec/initContainers", Value: []any{},
				})
				bootstrappedInit = true
			}
			patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: pm.InitContainers[i]})
		}
		// Track what we just added so a duplicate (e.g. extract path
		// matching a user-data path) doesn't double-mount.
		customerMountPaths[vm.MountPath] = struct{}{}
		return nil
	}

	// pod.UID is NOT populated in mutating admission webhooks for
	// CREATE (despite some K8s docs suggesting otherwise — empirically
	// confirmed 2026-06-05 on GCP-H100-a). Fall back to ns/name as
	// the overlay key; the pod-delete watcher uses the same mapping.
	overlayKey := overlayKeyFor(pod)

	// targetNode is the node where the captured tree lives. The pod
	// will be nodeAffinity-pinned to the same list via step 3, so
	// kubelet binds there — the overlay MUST live there too. Empty
	// list (e.g. shared-storage backends) falls back to local prep.
	overlayTargetNode := ""
	if len(manifest.CapturedOnNodes) > 0 {
		overlayTargetNode = manifest.CapturedOnNodes[0]
	}

	// Strategy selection: "init-container" defers mount work to the
	// nvsnap-mount-prep init container in the restored pod (nvsnap#202).
	// "inline" (default) does the mount work synchronously here.
	// init-container is only viable when all four prereqs are met;
	// missing any falls back to inline so the webhook stays correct
	// for partial-rollout clusters.
	useInitContainer := m.RestorePrepStrategy == "init-container" &&
		m.OverlayPreparer != nil && overlayKey != "" && m.MountPrepInitImage != ""
	// Mounts collected during the section-1/2 loops; passed to the
	// init container as a JSON env var so it knows what to prep.
	var initMounts []checkpointstore.VolumeMeta

	// tryOverlayMount attempts to wrap a captured volume in a per-pod
	// OverlayFS union and emit a writable bind. Returns true when the
	// overlay was successfully injected. Returns false (without erroring)
	// when there's no OverlayPreparer wired, the overlay key is empty,
	// or the prepare call fails — caller falls back to RO emitMount.
	tryOverlayMount := func(vm checkpointstore.VolumeMeta) bool {
		if m.OverlayPreparer == nil || overlayKey == "" {
			return false
		}
		if _, conflict := customerMountPaths[vm.MountPath]; conflict {
			m.logger().WithFields(logrus.Fields{
				"hash":      checkpointstore.ShortHash(hash),
				"mountPath": vm.MountPath,
			}).Info("skip overlay injection: customer pod already mounts this path")
			return true // treated as handled; caller skips
		}
		// init-container path: predict the merged mountpoint with
		// MountpointFor (pure function), emit the hostPath volume
		// referencing it, queue the vm for the init container's
		// prep request. NO mount work in admission.
		if useInitContainer {
			mp, err := m.OverlayPreparer.MountpointFor(overlayKey, vm)
			if err != nil {
				m.logger().WithError(err).WithFields(logrus.Fields{
					"hash":       checkpointstore.ShortHash(hash),
					"overlayKey": overlayKey,
					"volumeType": vm.Type,
					"mountPath":  vm.MountPath,
				}).Warn("MountpointFor failed; falling back to RO bind for this path")
				return false
			}
			m.emitOverlayMount(&patches, addedVolumes, vm, mp)
			initMounts = append(initMounts, vm)
			return true
		}
		// Inline path: do the mount syscall now. See RestorePrepStrategy
		// for why this is the not-default path going forward.
		mp, err := m.OverlayPreparer.PrepareOverlay(overlayKey, hash, vm, overlayTargetNode)
		if err != nil {
			m.logger().WithError(err).WithFields(logrus.Fields{
				"hash":       checkpointstore.ShortHash(hash),
				"overlayKey": overlayKey,
				"volumeType": vm.Type,
				"mountPath":  vm.MountPath,
			}).Warn("overlay prepare failed; falling back to RO bind for this path")
			return false
		}
		m.logger().WithFields(logrus.Fields{
			"overlayKey": overlayKey,
			"volumeType": vm.Type,
			"mountPath":  vm.MountPath,
			"mountpoint": mp,
		}).Info("nvsnap#88: overlay prepared, injecting writable bind")
		m.emitOverlayMount(&patches, addedVolumes, vm, mp)
		return true
	}

	// 1. Captured user-data volumes (hostPath / emptyDir). Whole-rootfs
	//    entries are skipped here. nvsnap#88: try OverlayFS wrap first
	//    so workloads that write into a captured hostPath cache (e.g.
	//    HF Transformers writing refs/main into the model cache) don't
	//    crash with EROFS. Falls back to the original RO bind when no
	//    OverlayPreparer is wired or the prepare call fails.
	for _, vm := range manifest.Volumes {
		if vm.Type == "rootfs" {
			continue
		}
		if tryOverlayMount(vm) {
			continue
		}
		if err := emitMount(vm); err != nil {
			return nil, err
		}
	}

	// 2. Rootfs extract paths (engine cache subpaths). Each becomes a
	//    synthetic VolumeMeta keyed by the absolute MountPath; the
	//    Backend resolves to tree/rootfs/<path>. Same OverlayFS treatment
	//    as section 1 — see nvsnap#194 for the original motivation.
	// Coalesce extract paths sharing a parent (e.g. /root/.tilelang/cache/<hash>)
	// into one mount at the parent so runtime os.rename calls across siblings
	// stay on the same overlay (EXDEV avoidance — see extract_coalesce.go).
	for _, p := range coalesceExtractCacheRoots(manifest.RootfsExtractPaths) {
		vm := checkpointstore.VolumeMeta{
			Name:      "rootfs-extract-" + p.Category,
			MountPath: p.Path,
			Type:      "rootfs-extract",
			SizeBytes: p.SizeBytes,
			FileCount: p.FileCount,
		}
		if tryOverlayMount(vm) {
			continue
		}
		if err := emitMount(vm); err != nil {
			return nil, err
		}
	}

	// 2b. Init-container mount-prep (nvsnap#202). When the
	//     init-container strategy was used, emit ONE nvsnap-mount-prep
	//     init container that POSTs the collected initMounts to the
	//     agent's /v1/restore/prep, polls until ready, exits. The
	//     hostPath volumes the loops above emitted reference the
	//     deterministic merged path the init container will set up.
	//     If initMounts is empty (nothing to prep), the init container
	//     would be a no-op so we just skip emitting it.
	if useInitContainer && len(initMounts) > 0 {
		if err := m.emitMountPrepInitContainer(
			&patches, &bootstrappedInit, needInitArray,
			overlayKey, hash, overlayTargetNode, initMounts,
		); err != nil {
			return nil, fmt.Errorf("emit nvsnap-mount-prep init container: %w", err)
		}
		m.logger().WithFields(logrus.Fields{
			"hash":       checkpointstore.ShortHash(hash),
			"overlayKey": overlayKey,
			"mounts":     len(initMounts),
		}).Info("nvsnap#202: nvsnap-mount-prep init container injected")
	}

	// 3. Pin FRESH pod to a node where the cache data lives (only
	//    relevant for per-node-storage backends like Local; for
	//    shared-storage backends manifest.CapturedOnNodes is empty
	//    and this is a no-op).
	//
	//    nvsnap.io/target-node overrides the auto-pin: when set, the
	//    pod is pinned to that single node instead, and the agent's
	//    EnsureCaptureLocal cascade fetches the bytes if they aren't
	//    already cached locally.
	affinityNodes := manifest.CapturedOnNodes
	if target := pod.Annotations[TargetNodeAnnotation]; target != "" {
		affinityNodes = []string{target}
	}
	patches = append(patches, m.nodeAffinityPatches(pod, affinityNodes)...)

	// 4. securityContext: runAsUser=0 for rootfs warm restore.
	//    The captured tree is root-owned, and the engine WRITES into its
	//    warmed cache/model dirs at startup through the per-pod OverlayFS
	//    (NIM/Riva rewrites the NGC filemap metadata, extracts engines).
	//    NVCA admits function pods with a hardened securityContext
	//    (capabilities drop ALL, no runAsUser), so the image's default
	//    non-root user can't write into the root-owned overlay and the
	//    engine aborts with "Permission denied (os error 13)"
	//    (whisper-large-v3, GCP-H100-a 2026-06-11). runAsUser=0 fixes
	//    that. We do NOT set privileged (see the privileged=false note
	//    below). Gated to rootfs captures only.
	if manifest.CaptureMethod == "rootfs" || manifestIsRootfs(manifest) {
		// privileged=false: the host overlay is mounted by the agent and
		// bind-mounted in via hostPath, so the main container only needs
		// runAsUser=0 to write. privileged here would expose all host
		// /dev/nvidiaN nodes and break single-GPU isolation (see
		// securityContextPatches doc).
		patches = append(patches, securityContextPatches(m.MainContainer, main.SecurityContext, false, rootfsWriteCaps)...)
	}

	return patches, nil
}

// nodeAffinityPatches produces JSON Patch ops that ensure the FRESH pod
// only schedules on a node that holds the cache data. For backends where
// the cache is on per-node hostPath, scheduling elsewhere would result
// in an empty mount.
//
// Strategy: append a NodeSelectorTerm to
//
//	pod.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms
//
// matching kubernetes.io/hostname IN <nodes>. The whole nodeAffinity
// stanza is built up step by step depending on what already exists, so
// we don't clobber customer-set affinity.
//
// nodes empty → no patches. Existing customer affinity is preserved
// (we ADD a term; multiple terms within nodeSelectorTerms OR together,
// so customer's other constraints still match).
func (m *Mutator) nodeAffinityPatches(pod *corev1.Pod, nodes []string) []PatchOp {
	if len(nodes) == 0 {
		return nil
	}
	values := make([]any, 0, len(nodes))
	for _, n := range nodes {
		if n == "" {
			continue
		}
		values = append(values, n)
	}
	if len(values) == 0 {
		return nil
	}

	nvsnapTerm := map[string]any{
		"matchExpressions": []any{
			map[string]any{
				"key":      "kubernetes.io/hostname",
				"operator": "In",
				"values":   values,
			},
		},
	}

	var patches []PatchOp
	switch {
	case pod.Spec.Affinity == nil:
		patches = append(patches, PatchOp{
			Op:   "add",
			Path: "/spec/affinity",
			Value: map[string]any{
				"nodeAffinity": map[string]any{
					"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
						"nodeSelectorTerms": []any{nvsnapTerm},
					},
				},
			},
		})
	case pod.Spec.Affinity.NodeAffinity == nil:
		patches = append(patches, PatchOp{
			Op:   "add",
			Path: "/spec/affinity/nodeAffinity",
			Value: map[string]any{
				"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
					"nodeSelectorTerms": []any{nvsnapTerm},
				},
			},
		})
	case pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil:
		patches = append(patches, PatchOp{
			Op:   "add",
			Path: "/spec/affinity/nodeAffinity/requiredDuringSchedulingIgnoredDuringExecution",
			Value: map[string]any{
				"nodeSelectorTerms": []any{nvsnapTerm},
			},
		})
	default:
		// Append to existing nodeSelectorTerms. Each term must match
		// independently for the pod to be schedulable; appending OUR
		// term to the list unions with existing terms (OR semantics
		// across nodeSelectorTerms is K8s standard, so customer's
		// existing terms are preserved as alternatives).
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  "/spec/affinity/nodeAffinity/requiredDuringSchedulingIgnoredDuringExecution/nodeSelectorTerms/-",
			Value: nvsnapTerm,
		})
	}
	return patches
}

// emitOverlayMount synthesises the volumes/volumeMounts patches for a
// RootfsExtractPath that's already been prepared as an OverlayFS union
// on the host (nvsnap#194). The merged mountpoint is bound directly
// (no init container, no backend resolution), readOnly=false so the
// pod can write into the cache at runtime.
func (m *Mutator) emitOverlayMount(
	patches *[]PatchOp,
	addedVolumes map[string]struct{},
	vm checkpointstore.VolumeMeta,
	mountpoint string,
) {
	// DirectoryOrCreate (not Directory): in init-container strategy
	// the merged dir doesn't exist at admission time — the agent's
	// async prep job creates and mounts it after the init container
	// POSTs. Kubelet runs the hostPath type check during volume
	// setup, before any container starts, so a strict Directory check
	// would fail with "is not a directory" and stall the pod.
	// DirectoryOrCreate lets kubelet make an empty dir as a
	// placeholder; the agent's later Mount() overlays on top, and the
	// main container's bind picks up the overlay at container start.
	// Safe for inline mode too: the agent's Prepare() already
	// MkdirAll'd the path, so kubelet finds it existing.
	hostPathType := corev1.HostPathDirectoryOrCreate
	vol := corev1.Volume{
		Name: vm.Name,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: mountpoint,
				Type: &hostPathType,
			},
		},
	}
	vMount := corev1.VolumeMount{
		Name:      vm.Name,
		MountPath: vm.MountPath,
		ReadOnly:  false,
	}
	if _, dupe := addedVolumes[vol.Name]; !dupe {
		*patches = append(*patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: vol})
		addedVolumes[vol.Name] = struct{}{}
	}
	*patches = append(*patches, PatchOp{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
		Value: vMount,
	})
}

// overlayKeyFor returns a stable per-restore-pod identifier suitable for
// keying the OverlayManager's per-pod scratch tree. Mutating admission
// webhooks for CREATE don't get pod.UID populated, so we fall back to
// ns/name. Filesystem-safe (underscore separator) since the manager
// uses it as a directory name.
func overlayKeyFor(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	if uid := string(pod.UID); uid != "" {
		return uid
	}
	if pod.Name == "" || pod.Namespace == "" {
		return ""
	}
	return pod.Namespace + "_" + pod.Name
}

func (m *Mutator) logger() logrus.FieldLogger {
	if m.Log != nil {
		return m.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "webhook.mutate")
}
