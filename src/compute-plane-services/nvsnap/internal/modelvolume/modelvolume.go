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

// Package modelvolume owns the per-identity model volume of
// docs/proposals/helm-shared-model-volume.md: one volume per model URI per
// cluster, written once, immutable afterwards, read by every other pod.
//
// One lifecycle, whatever the storage. The primary claim lives in the
// nvsnap namespace; completion labels the retained primary PV and releases
// the claim; readers in any namespace reference a view, a static PV over
// the same volume pre-bound to a claim in their namespace; the compile
// caches of every pod stay in its local cachedir and are shared through a
// cache set built the same way. The storage profile picks a Mode, and the
// Mode decides exactly two things:
//
//   - the access mode of the primary claim: ReadWriteOnce (ModeBlock,
//     NVMesh) or ReadWriteMany (ModeRWX, a distributed filesystem);
//   - how the download reaches the primary. Block storage cannot be read
//     while it is attached read-write and has no size until the bytes
//     exist, so the Job stages into a pod-local emptyDir and the agent on
//     that node copies the tree into a claim sized from what landed. A
//     shared filesystem is readable while written and its size is nominal,
//     so the claim exists at admission and the Job writes into it through
//     a read-write view in its own namespace; readers attach their
//     read-only view at once and wait for the marker.
//
// Nothing else may branch on the Mode. Capabilities that follow from it
// are asked through Config (SharedWhileWriting, ReaderMode).
package modelvolume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Mode is how the volume is shared.
type Mode string

// Modes: RWX on a distributed filesystem, Block on NVMesh.
const (
	ModeRWX   Mode = "rwx"
	ModeBlock Mode = "block"
)

// Labels and annotations on volumes and pods.
const (
	// IdentityLabel is the short identity key on claims, PVs and pods.
	IdentityLabel = "nvsnap.io/model"
	// CacheLabel is the identity key of a compile-cache volume (KindCache).
	CacheLabel = "nvsnap.io/cache"
	// IdentityAnnotation is the full model URI.
	IdentityAnnotation = "nvsnap.io/model-uri"
	// RoleLabel is writer | reader on pods.
	RoleLabel = "nvsnap.io/model-role"
	// CompleteLabel is "true" on the writer claim once the download exited 0.
	CompleteLabel = "nvsnap.io/model-complete"
	// PendingLabel is "true" on a Block-mode reader that still needs the
	// agent to bind the volume into its emptyDir.
	PendingLabel = "nvsnap.io/model-pending"
	// LandingAnnotation on a pod records the mount path the model must
	// appear at, for the agent's bind.
	LandingAnnotation = "nvsnap.io/model-landing"
	// MarkerFile at the volume root says the download completed.
	MarkerFile = ".nvsnap-complete"
	// DownloadInitAnnotation on a writer names the init container whose
	// exit 0 means the download completed.
	DownloadInitAnnotation = "nvsnap.io/model-download-init"
	// StagingAnnotation on a Block-mode download Job names the emptyDir
	// volume the download landed in; the agent on the Job's node copies it
	// into the sized claim.
	StagingAnnotation = "nvsnap.io/model-staging-volume"
	// CaptureLabel is "true" on a pod whose engine downloads an artifact
	// nvsnap has no download recipe for (a chart script pulling an NGC
	// model) into a pod volume. The pod keeps its own download; the agent
	// on its node copies the finished volume, markers and all, into the
	// primary once the pod is Ready, and later pods read that copy.
	CaptureLabel = "nvsnap.io/model-capture"
	// CaptureVolumeAnnotation names the pod volume the engine downloads into.
	CaptureVolumeAnnotation = "nvsnap.io/model-capture-volume"
	// Cache set: the compile caches of one configuration, all ranks in one
	// volume. Stamped by the webhook on capture candidates and read by the
	// agents that collect them.
	//
	// CacheKeyLabel is the set key on pods, for listing a group.
	CacheKeyLabel = "nvsnap.io/cache-key"
	// CacheOrdinalAnnotation is the pod's rank in its group.
	CacheOrdinalAnnotation = "nvsnap.io/cache-ordinal"
	// CacheGroupSizeAnnotation is how many ranks the set must contain.
	CacheGroupSizeAnnotation = "nvsnap.io/cache-group-size"
	// CacheRankReadyAnnotation is set by the agent on the pod's node once
	// the rank's cache tree has settled; CacheRankBytesAnnotation is its size.
	CacheRankReadyAnnotation = "nvsnap.io/cache-rank-ready"
	CacheRankBytesAnnotation = "nvsnap.io/cache-rank-bytes"
	// CacheRankDeltaAnnotation marks a warm rank whose cachedir holds
	// files the seeded set did not have; the bytes and a fingerprint of
	// the new paths ride along. Any such rank proposes a set refresh
	// (docs/proposals/helm-chart-cache-refresh.md).
	CacheRankDeltaAnnotation            = "nvsnap.io/cache-rank-delta"
	CacheRankDeltaBytesAnnotation       = "nvsnap.io/cache-rank-delta-bytes"
	CacheRankDeltaFingerprintAnnotation = "nvsnap.io/cache-rank-delta-fingerprint"
	// CacheSeedGenerationAnnotation on a seeded pod names the generation
	// it was seeded from. Only ranks seeded from the serving generation
	// take part in a refresh: an older rank's delta is already in the
	// newer generation, or belongs to a tree that no longer serves.
	CacheSeedGenerationAnnotation = "nvsnap.io/cache-seed-generation"
	// GenerationAnnotation on a complete set primary counts refreshes;
	// absent means 1. Lookup serves the newest complete generation.
	GenerationAnnotation = "nvsnap.io/cache-generation"
	// RefreshedFromAnnotation names the primary a refreshed generation
	// was collected to replace.
	RefreshedFromAnnotation = "nvsnap.io/cache-refreshed-from"
	// DeltaFingerprintAnnotation records the delta that produced a
	// generation; the same delta proposed again means the engine rewrites
	// those files every start, and the set is marked stable instead.
	DeltaFingerprintAnnotation = "nvsnap.io/cache-delta-fingerprint"
	// RefreshStableAnnotation stops further refreshes of a set.
	RefreshStableAnnotation = "nvsnap.io/cache-refresh-stable"
	// SourcePVLabel on a read-only view names the primary it was minted
	// from, so liveness and retirement work per generation.
	SourcePVLabel = "nvsnap.io/source-pv"

	// BytesAnnotation on a primary PV is the byte count of the tree it
	// holds, measured at completion. Views are sized from it where the
	// primary's own capacity is nominal (a shared filesystem claim), and
	// readers' page-cache decisions use it in every mode.
	BytesAnnotation = "nvsnap.io/volume-bytes"
	// LastUsedAnnotation on a primary PV is the RFC 3339 time a pod last
	// admitted against it (or it completed). Retention counts from here.
	LastUsedAnnotation = "nvsnap.io/last-used"
	// SourceNamespaceLabel on the primary PV records the namespace the
	// download Job ran in.
	SourceNamespaceLabel = "nvsnap.io/model-source-namespace"
	// ViewRoleLabel marks a per-namespace view PV or claim: ViewRoleReader
	// for the read-only views readers mount, ViewRoleWriter for the
	// read-write view a download Job writes through. Primaries carry
	// neither.
	ViewRoleLabel  = "nvsnap.io/role"
	ViewRoleReader = "reader-shared"
	ViewRoleWriter = "writer-shared"
	// DownloadContainer is the Job's download step; in Block mode it is an
	// init container and HoldContainer keeps the pod, and with it the
	// staged emptyDir, alive until the agent has copied it out.
	DownloadContainer = "download"
	HoldContainer     = "hold"
	// FailedAnnotation on the failure record carries the reason.
	FailedAnnotation = "nvsnap.io/model-failed"

	managedBy = "nvsnap"
)

// ReaderMode is how a Block-mode reader reaches the finished volume.
type ReaderMode string

// Reader modes. PVC: the pod references the read-only claim in its own
// namespace and stays Pending on volume binding until the agent mints it;
// works under Kyverno's disallow-host-path and needs no bind, but a pod
// pending on a claim holds a gang scheduler. HostPath: the pod schedules
// at once on a hostPath landing and the agent binds the volume in; needs
// hostPath allowed by policy. Default PVC.
const (
	ReaderPVC      ReaderMode = "pvc"
	ReaderHostPath ReaderMode = "hostPath"
)

// Config comes from the storage profile.
type Config struct {
	Mode         Mode
	StorageClass string
	// Size is the RWX-mode claim size: a shared filesystem claim is created
	// at admission, before anything is downloaded, so it is a ceiling that
	// the filesystem's own quota semantics make cheap. Block mode never
	// uses it; those claims are sized from the downloaded bytes.
	Size resource.Quantity
	// Reader selects the Block-mode reader mode; empty means ReaderPVC.
	Reader ReaderMode
	// Namespace holds Block-mode primary claims and their copy holders, so
	// no artifact is tied to a function namespace's lifetime. Empty means
	// nvsnap-system.
	Namespace string
	// MinSize is the floor for a sized Block-mode claim. Empty means 1Gi.
	MinSize resource.Quantity
	// Kind selects what the volumes hold: KindModel (default) or
	// KindCache, the compile caches captured from a running pod. The two
	// kinds share every mechanism and differ in names and labels only.
	Kind Kind
}

// Kind of volume a Provisioner manages.
type Kind string

// Kinds.
const (
	KindModel Kind = "model"
	KindCache Kind = "cache"
)

// Label is the identity label for this kind.
func (c Config) Label() string {
	if c.Kind == KindCache {
		return CacheLabel
	}
	return IdentityLabel
}

func (c Config) prefix() string {
	if c.Kind == KindCache {
		return "nvsnap-cache-"
	}
	return "nvsnap-model-"
}

// ClaimName is the primary claim for uri.
func (c Config) ClaimName(uri string) string { return c.prefix() + Key(uri) }

// GenerationClaimName is the primary claim for generation gen of uri:
// the first generation keeps ClaimName, later ones carry a suffix so the
// election for a refresh is its own atomic Create.
func (c Config) GenerationClaimName(uri string, gen int) string {
	if gen <= 1 {
		return c.ClaimName(uri)
	}
	return c.ClaimName(uri) + "-g" + strconv.Itoa(gen)
}

// ReadOnlyClaimName is the per-namespace read-only claim for uri.
func (c Config) ReadOnlyClaimName(uri string) string { return c.prefix() + Key(uri) + "-ro" }

// ReadOnlyPVName is the static PV behind ReadOnlyClaimName in ns.
func (c Config) ReadOnlyPVName(uri, ns string) string {
	sum := sha256.Sum256([]byte(ns))
	return c.prefix() + Key(uri) + "-ro-" + hex.EncodeToString(sum[:4])
}

// FailureRecordName is the ConfigMap that records a given-up attempt.
func (c Config) FailureRecordName(uri string) string { return c.prefix() + "failed-" + Key(uri) }

// ReadOnlyLabels are the labels a minted read-only PV and claim carry.
func (c Config) ReadOnlyLabels(uri string) map[string]string {
	return map[string]string{c.Label(): Key(uri)}
}

// SystemNamespace returns the namespace Block-mode primaries live in.
func (c Config) SystemNamespace() string {
	if c.Namespace == "" {
		return "nvsnap-system"
	}
	return c.Namespace
}

// headroomPercent is the slack added on top of the measured bytes: the
// filesystem's own metadata, the completion marker, and xfs allocation
// rounding on a freshly made volume.
const headroomPercent = 10

// VolumeSize is the claim size for a downloaded tree of bytes: measured
// plus headroom, rounded up to a whole GiB, never below MinSize.
func (c Config) VolumeSize(bytes int64) resource.Quantity {
	const gib = int64(1) << 30
	want := bytes + bytes/100*headroomPercent
	gibs := (want + gib - 1) / gib
	if gibs < 1 {
		gibs = 1
	}
	q := *resource.NewQuantity(gibs*gib, resource.BinarySI)
	if !c.MinSize.IsZero() && q.Cmp(c.MinSize) < 0 {
		return c.MinSize.DeepCopy()
	}
	return q
}

// ReaderMode returns the configured reader mode with its default.
func (c Config) ReaderMode() ReaderMode {
	if c.Reader == "" || c.SharedWhileWriting() {
		// A view of a shared filesystem binds at once, so the hostPath
		// landing, which exists to let block readers schedule before the
		// agent binds the volume in, has nothing to offer there.
		return ReaderPVC
	}
	return c.Reader
}

// SharedWhileWriting reports whether the volume can be read on other
// nodes while the download still has it open read-write. True on a
// distributed filesystem, false on block storage, where the read-only
// attach is refused until the writer detaches. This is the one place the
// Mode is consulted for a capability.
func (c Config) SharedWhileWriting() bool { return c.Mode == ModeRWX }

// WriterViewClaimName is the per-namespace read-write view the download
// Job writes through when the volume is shared while writing.
func (c Config) WriterViewClaimName(uri string) string { return c.prefix() + Key(uri) + "-rw" }

// WriterViewPVName is the static PV behind WriterViewClaimName in ns.
func (c Config) WriterViewPVName(uri, ns string) string {
	sum := sha256.Sum256([]byte(ns))
	return c.prefix() + Key(uri) + "-rw-" + hex.EncodeToString(sum[:4])
}

// Key is the short stable token for a model URI, used in object names
// and labels (label values may be at most 63 characters).
func Key(uri string) string {
	sum := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(sum[:8])
}

// ClaimName is the model writer claim in RWX mode and the primary in both.
func ClaimName(uri string) string { return Config{}.ClaimName(uri) }

// ReadOnlyClaimName is the Block-mode read-only model claim minted after completion.
func ReadOnlyClaimName(uri string) string { return Config{}.ReadOnlyClaimName(uri) }

// ReadOnlyPVName is the static PV behind ReadOnlyClaimName in ns.
func ReadOnlyPVName(uri, ns string) string { return Config{}.ReadOnlyPVName(uri, ns) }

// State of an identity on the cluster, as the webhook needs it.
type State struct {
	// Exists: a writer claim exists (a download is in flight or done).
	Exists bool
	// Complete: the download finished; readers may attach.
	Complete bool
	// ClaimNamespace is where the in-flight writer claim lives.
	ClaimNamespace string
	// PrimaryPV is the retained volume holding the model once complete;
	// read-only views are minted from it.
	PrimaryPV string
	// InFlightPV is the volume bound to the writer claim while the download
	// is still running, when the claim is bound; on storage that is
	// SharedWhileWriting, views may be minted from it before completion.
	InFlightPV string
	// Failed: the last attempt to produce the volume gave up recently.
	// Pods admitted while this holds are left alone and download for
	// themselves; the record expires so a later deployment retries.
	Failed bool
	// PrimaryBytes is PrimaryPV's capacity, what a reader's sweep would read.
	PrimaryBytes int64
	// Generation of PrimaryPV (1 when unannotated); PrimaryCreated is its
	// creation time, the reference for the refresh cooldown.
	Generation     int
	PrimaryCreated time.Time
	// DeltaFingerprint is the delta that produced this generation, empty
	// for a cold capture; RefreshStable says no further refresh runs.
	DeltaFingerprint string
	RefreshStable    bool
}

// Generation reads a primary's generation annotation; absent means 1.
func Generation(pv *corev1.PersistentVolume) int {
	if n, err := strconv.Atoi(pv.Annotations[GenerationAnnotation]); err == nil && n > 1 {
		return n
	}
	return 1
}

// FailureRecordName is the ConfigMap that records a given-up model download.
func FailureRecordName(uri string) string { return Config{}.FailureRecordName(uri) }

// FailureTTL is how long a failure record keeps pods on their own path.
const FailureTTL = time.Hour

// Provisioner creates and inspects model volumes.
type Provisioner struct {
	Kube kubernetes.Interface
	Cfg  Config
}

// EnsureWriterClaim creates the claim the download writes into, in ns, at
// the configured RWX size. Idempotent. RWX mode creates a ReadWriteMany
// claim readers share; Block mode callers use EnsureSizedClaim instead.
func (p *Provisioner) EnsureWriterClaim(ctx context.Context, uri, ns string) (string, error) {
	return p.EnsureSizedClaim(ctx, uri, ns, p.Cfg.Size)
}

// EnsureSizedClaim creates the claim for uri in ns at size. Idempotent: an
// existing claim is returned as is, whatever its size. Block mode creates
// a ReadWriteOnce claim that is released after the copy, leaving the
// retained PV as the artifact.
func (p *Provisioner) EnsureSizedClaim(ctx context.Context, uri, ns string, size resource.Quantity) (string, error) {
	name, _, err := p.ClaimSizedClaim(ctx, uri, ns, size)
	return name, err
}

// ClaimSizedClaim creates the sized claim and reports whether this call
// created it. It is the election: the API server's Create is atomic, so
// of any number of agents racing for one identity exactly one sees
// created and owns the copy; the rest get AlreadyExists and back off. A
// Get before the Create is not an election (five agents once passed
// through that gap together, ct1 2026-09-30), so there is none.
func (p *Provisioner) ClaimSizedClaim(ctx context.Context, uri, ns string, size resource.Quantity) (name string, created bool, err error) {
	return p.ClaimSizedClaimNamed(ctx, p.Cfg.ClaimName(uri), uri, ns, size)
}

// ClaimSizedClaimNamed is ClaimSizedClaim for a claim of a given name,
// such as a later generation of a set.
func (p *Provisioner) ClaimSizedClaimNamed(ctx context.Context, name, uri, ns string, size resource.Quantity) (string, bool, error) {
	mode := corev1.ReadWriteOnce
	if p.Cfg.Mode == ModeRWX {
		mode = corev1.ReadWriteMany
	}
	sc := p.Cfg.StorageClass
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": managedBy,
				p.Cfg.Label():                  Key(uri),
			},
			Annotations: map[string]string{IdentityAnnotation: uri},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{mode},
			StorageClassName: &sc,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
	_, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	switch {
	case err == nil:
	case apierrors.IsAlreadyExists(err):
		return name, false, nil
	default:
		return "", false, fmt.Errorf("create claim %s/%s: %w", ns, name, err)
	}
	// The Create is the election, but the previous winner releases its
	// claim the moment it labels the volume complete. A caller that looked
	// up the set before that release and reaches Create after it wins an
	// election for work that is already done. Re-check now that the claim
	// is ours: a complete volume at or past this generation means the
	// election is void, so give the claim back and report no winner.
	st, lerr := p.Lookup(ctx, uri)
	if lerr == nil && st.Complete && st.Generation >= p.Cfg.claimGeneration(uri, name) {
		if derr := p.Kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return "", false, fmt.Errorf("release void claim %s/%s: %w", ns, name, derr)
		}
		return name, false, nil
	}
	return name, true, nil
}

// claimGeneration is the inverse of GenerationClaimName: the generation a
// claim name stands for, 1 for the base name.
func (c Config) claimGeneration(uri, name string) int {
	base := c.ClaimName(uri)
	if n, err := strconv.Atoi(strings.TrimPrefix(name, base+"-g")); err == nil && strings.HasPrefix(name, base+"-g") && n > 1 {
		return n
	}
	return 1
}

// WaitBound polls until the claim in ns has a bound volume and returns
// the PV name. Block-mode classes bind immediately; this covers the
// provisioner's round trip.
func (p *Provisioner) WaitBound(ctx context.Context, ns, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		pvc, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get claim %s/%s: %w", ns, name, err)
		}
		if pvc.Spec.VolumeName != "" && pvc.Status.Phase == corev1.ClaimBound {
			return pvc.Spec.VolumeName, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("claim %s/%s not bound after %s (phase %s)", ns, name, timeout, pvc.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Lookup reports the identity's state: the failure record, then the
// newest complete primary PV (the artifact in every mode), then an
// in-flight writer claim. The same answer on block storage and on a
// shared filesystem.
func (p *Provisioner) Lookup(ctx context.Context, uri string) (State, error) {
	st := State{}
	if cm, err := p.Kube.CoreV1().ConfigMaps(p.Cfg.SystemNamespace()).Get(ctx, p.Cfg.FailureRecordName(uri), metav1.GetOptions{}); err == nil {
		if at, perr := time.Parse(time.RFC3339, cm.Data["failedAt"]); perr == nil && time.Since(at) < FailureTTL {
			st.Failed = true
		}
	} else if !apierrors.IsNotFound(err) {
		return State{}, fmt.Errorf("get failure record for %s: %w", uri, err)
	}
	pvs, err := p.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{LabelSelector: p.Cfg.Label() + "=" + Key(uri) + "," + CompleteLabel + "=true"})
	if err != nil {
		return State{}, fmt.Errorf("list volumes for %s: %w", uri, err)
	}
	// Several complete primaries are generations of one set; the
	// newest serves, the rest age out.
	var best *corev1.PersistentVolume
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if IsViewPV(pv) || pv.Spec.CSI == nil {
			continue
		}
		if best == nil || Generation(pv) > Generation(best) || (Generation(pv) == Generation(best) && pv.CreationTimestamp.After(best.CreationTimestamp.Time)) {
			best = pv
		}
	}
	if best != nil {
		st.Exists, st.Complete, st.PrimaryPV = true, true, best.Name
		if q, ok := best.Spec.Capacity[corev1.ResourceStorage]; ok {
			st.PrimaryBytes = q.Value()
		}
		if b, err := strconv.ParseInt(best.Annotations[BytesAnnotation], 10, 64); err == nil && b > 0 {
			st.PrimaryBytes = b
		}
		st.Generation, st.PrimaryCreated = Generation(best), best.CreationTimestamp.Time
		st.DeltaFingerprint = best.Annotations[DeltaFingerprintAnnotation]
		st.RefreshStable = best.Annotations[RefreshStableAnnotation] == "true"
		return st, nil
	}
	list, err := p.Kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{LabelSelector: p.Cfg.Label() + "=" + Key(uri)})
	if err != nil {
		return State{}, fmt.Errorf("list claims for %s: %w", uri, err)
	}
	for i := range list.Items {
		c := &list.Items[i]
		if c.Name != p.Cfg.ClaimName(uri) {
			continue
		}
		st.Exists = true
		st.ClaimNamespace = c.Namespace
		if c.Spec.VolumeName != "" && c.Status.Phase == corev1.ClaimBound {
			st.InFlightPV = c.Spec.VolumeName
		}
	}
	return st, nil
}

// IsViewPV reports whether pv is a per-namespace view of a primary (read-only
// for readers, read-write for a download Job) rather than a primary itself.
func IsViewPV(pv *corev1.PersistentVolume) bool {
	switch pv.Labels[ViewRoleLabel] {
	case ViewRoleReader, ViewRoleWriter:
		return true
	}
	return pv.Spec.CSI != nil && pv.Spec.CSI.ReadOnly
}

// MarkComplete records that the download finished: the primary PV is
// labelled complete and retained, and the writer claim is released. The
// PV is the artifact in every mode; on block storage the release is also
// what lets read-only attaches elsewhere succeed (a claim still bound
// keeps the volume attached read-write to the writer's node, dev1
// 2026-09-26). Idempotent.
func (p *Provisioner) MarkComplete(ctx context.Context, uri, ns string) error {
	return p.MarkCompleteClaim(ctx, uri, ns, p.Cfg.ClaimName(uri), nil)
}

// MarkCompleteClaim is MarkComplete for a named claim, stamping meta as
// annotations on the primary (a refreshed generation's number, origin
// and delta fingerprint).
func (p *Provisioner) MarkCompleteClaim(ctx context.Context, uri, ns, name string, meta map[string]string) error {
	pvc, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			gen, _ := strconv.Atoi(meta[GenerationAnnotation])
			if st, lerr := p.Lookup(ctx, uri); lerr == nil && st.Complete && (gen <= 1 || st.Generation >= gen) {
				return nil // released already
			}
		}
		return fmt.Errorf("get claim %s/%s: %w", ns, name, err)
	}
	if pvc.Spec.VolumeName == "" {
		return fmt.Errorf("claim %s/%s has no bound volume", ns, name)
	}
	pv, err := p.Kube.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get volume %s: %w", pvc.Spec.VolumeName, err)
	}
	if pv.Labels[CompleteLabel] != "true" {
		if pv.Labels == nil {
			pv.Labels = map[string]string{}
		}
		pv.Labels["app.kubernetes.io/managed-by"] = managedBy
		pv.Labels[p.Cfg.Label()] = Key(uri)
		pv.Labels[CompleteLabel] = "true"
		pv.Labels[SourceNamespaceLabel] = ns
		if pv.Annotations == nil {
			pv.Annotations = map[string]string{}
		}
		pv.Annotations[IdentityAnnotation] = uri
		pv.Annotations[LastUsedAnnotation] = time.Now().UTC().Format(time.RFC3339)
		for k, v := range meta {
			pv.Annotations[k] = v
		}
		pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		if _, err := p.Kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil && !apierrors.IsConflict(err) {
			return fmt.Errorf("label volume %s complete: %w", pv.Name, err)
		}
	}
	if err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("release writer claim %s/%s: %w", ns, name, err)
	}
	return nil
}

// ReleaseWriterView releases the read-write view a download Job wrote
// through in ns once the primary is complete. Only the claim goes: kubelet
// still has the volume staged until the Job's pod is reaped, and it
// unstages against the PV, so the PV is left to turn Released and the
// reaper retires it like any other view (a PV deleted under a staged
// mount leaked the NFS mount on the node, OCI FSS 2026-10-02).
func (p *Provisioner) ReleaseWriterView(ctx context.Context, uri, ns string) error {
	if err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, p.Cfg.WriterViewClaimName(uri), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete writer view claim %s/%s: %w", ns, p.Cfg.WriterViewClaimName(uri), err)
	}
	return nil
}

// RecordBytes stamps the measured byte count of the tree on the primary
// PV. Idempotent; a conflict is retried by the next caller.
func (p *Provisioner) RecordBytes(ctx context.Context, pvName string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, BytesAnnotation, strconv.FormatInt(bytes, 10))
	if _, err := p.Kube.CoreV1().PersistentVolumes().Patch(ctx, pvName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("record bytes on volume %s: %w", pvName, err)
	}
	return nil
}

// PrimaryPVName is the volume behind uri's primary: the writer claim's
// bound volume while the download runs, the complete PV afterwards.
func (p *Provisioner) PrimaryPVName(ctx context.Context, uri string) (string, error) {
	st, err := p.Lookup(ctx, uri)
	if err != nil {
		return "", err
	}
	if st.PrimaryPV != "" {
		return st.PrimaryPV, nil
	}
	return st.InFlightPV, nil
}

// MarkerBytes parses the byte count a writer left in the completion
// marker; 0 when the marker is empty or not a number.
func MarkerBytes(data []byte) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Detached reports whether no VolumeAttachment references pv: the moment
// a Block-mode volume may be attached read-only on any node.
func (p *Provisioner) Detached(ctx context.Context, pv string) (bool, error) {
	vas, err := p.Kube.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("list VolumeAttachments: %w", err)
	}
	for i := range vas.Items {
		if src := vas.Items[i].Spec.Source.PersistentVolumeName; src != nil && *src == pv {
			return false, nil
		}
	}
	return true, nil
}

// JobName is the download Job for a model URI.
func JobName(uri string) string { return "nvsnap-model-dl-" + Key(uri) }

// DownloadStep is the container that fetches the model into the claim,
// derived by the webhook from the chart's own download init (or from the
// engine image plus `hf download`), already wrapped to touch the marker.
type DownloadStep struct {
	Container        corev1.Container
	ImagePullSecrets []corev1.LocalObjectReference
	Tolerations      []corev1.Toleration
	NodeSelector     map[string]string
	// VolumeName is the name the container mounts the claim under.
	VolumeName string
	// Volumes are the other volumes the container mounts (secrets with
	// registry keys, ConfigMaps with scripts), copied from the pod.
	Volumes []corev1.Volume
	// PodSecurityContext is the source pod's, so the Job writes the volume
	// with the same user, groups and fsGroup the readers will use.
	PodSecurityContext *corev1.PodSecurityContext
	// MainSecurityContext is the source pod's engine container posture,
	// inherited by the download container.
	MainSecurityContext *corev1.SecurityContext
}

// EnsureDownloadJob creates the one download Job for uri in ns. With a
// claim (the read-write view of a volume that is SharedWhileWriting) the
// Job writes straight into it. With an empty claim (block storage) the Job
// writes into a pod-local emptyDir on the node's disk and stays Succeeded
// until the agent on that node has copied the tree into a claim sized
// from what landed; the Job's staging annotation names the volume. Create
// is atomic, so N concurrent admissions produce one Job and need no
// election.
func (p *Provisioner) EnsureDownloadJob(ctx context.Context, uri, ns, claim string, step DownloadStep) (string, error) {
	name := JobName(uri)
	if _, err := p.Kube.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return name, nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get job %s/%s: %w", ns, name, err)
	}
	backoff := int32(6)
	// A finished pod keeps its volume attached, which blocks a read-only
	// attach elsewhere on some drivers (dev1 2026-09-26), so a finished Job
	// goes away shortly. In Block mode the Job never finishes on its own:
	// the agent deletes it after the copy (see the hold container below).
	ttl := int32(30)
	annotations := map[string]string{IdentityAnnotation: uri}
	landing := corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}
	if claim == "" {
		landing = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
		annotations[StagingAnnotation] = step.VolumeName
	}
	// The Job's pod carries the identity and the writer role so the agents
	// see it: on storage that is shared while written its landing claim is
	// the writer view, minted once the primary binds.
	labels := map[string]string{"app.kubernetes.io/managed-by": managedBy, p.Cfg.Label(): Key(uri), RoleLabel: "writer"}
	c := step.Container
	c.Name = DownloadContainer
	// The container keeps every mount the chart's init had (registry keys
	// under /var/secrets, scripts from a ConfigMap); only the landing
	// volume is redirected. The pod's projected service-account token is
	// dropped: the Job mounts no token.
	volumes := []corev1.Volume{{Name: step.VolumeName, VolumeSource: landing}}
	for i := range step.Volumes {
		v := &step.Volumes[i]
		if v.Name == step.VolumeName || strings.HasPrefix(v.Name, "kube-api-access-") {
			continue
		}
		volumes = append(volumes, *v)
	}
	for i := range c.VolumeMounts {
		if strings.HasPrefix(c.VolumeMounts[i].Name, "kube-api-access-") {
			c.VolumeMounts = append(c.VolumeMounts[:i], c.VolumeMounts[i+1:]...)
			break
		}
	}
	Harden(&c, DownloadResources, step.MainSecurityContext)
	psc := &corev1.PodSecurityContext{}
	if step.PodSecurityContext != nil {
		psc = step.PodSecurityContext.DeepCopy()
	}
	if psc.SeccompProfile == nil {
		psc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	}
	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyOnFailure,
		AutomountServiceAccountToken: new(bool),
		SecurityContext:              psc,
		ImagePullSecrets:             step.ImagePullSecrets,
		Tolerations:                  step.Tolerations,
		NodeSelector:                 step.NodeSelector,
		Containers:                   []corev1.Container{c},
		Volumes:                      volumes,
	}
	var activeDeadline *int64
	if claim == "" {
		// kubelet removes a pod's emptyDir as soon as the pod terminates,
		// so the staged bytes only exist while the pod runs. The download
		// is an init container (exit 0 is the completion signal, retried
		// under OnFailure) and a hold container keeps the pod Running
		// until the agent has copied the tree and deletes the Job. The
		// active deadline bounds a node whose agent never gets to it.
		hold := corev1.Container{
			Name:    HoldContainer,
			Image:   c.Image,
			Command: []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 60; done"},
		}
		Harden(&hold, HoldResources, step.MainSecurityContext)
		podSpec.InitContainers = []corev1.Container{c}
		podSpec.Containers = []corev1.Container{hold}
		deadline := int64(6 * 3600)
		activeDeadline = &deadline
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: annotations},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec:       podSpec,
			},
		},
	}
	if _, err := p.Kube.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create job %s/%s: %w", ns, name, err)
	}
	return name, nil
}

// JobSucceeded reports whether the download Job for uri in ns finished.
func (p *Provisioner) JobSucceeded(ctx context.Context, uri, ns string) (bool, error) {
	job, err := p.Kube.BatchV1().Jobs(ns).Get(ctx, JobName(uri), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return job.Status.Succeeded > 0, nil
}

// RecordFailure writes the failure record for uri so admissions for the
// next FailureTTL leave pods alone. Idempotent (overwrites the time).
func (p *Provisioner) RecordFailure(ctx context.Context, uri, reason string) error {
	ns := p.Cfg.SystemNamespace()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: p.Cfg.FailureRecordName(uri), Namespace: ns,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": managedBy, p.Cfg.Label(): Key(uri)},
			Annotations: map[string]string{IdentityAnnotation: uri, FailedAnnotation: reason}},
		Data: map[string]string{"failedAt": time.Now().UTC().Format(time.RFC3339)},
	}
	if _, err := p.Kube.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("record failure for %s: %w", uri, err)
		}
		if _, err := p.Kube.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update failure record for %s: %w", uri, err)
		}
	}
	return nil
}

// ClearFailure removes the failure record for uri. Idempotent.
func (p *Provisioner) ClearFailure(ctx context.Context, uri string) error {
	if err := p.Kube.CoreV1().ConfigMaps(p.Cfg.SystemNamespace()).Delete(ctx, p.Cfg.FailureRecordName(uri), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("clear failure record for %s: %w", uri, err)
	}
	return nil
}

// DeleteJob removes the download Job for uri in ns and its pod, freeing
// the staged emptyDir. Idempotent.
func (p *Provisioner) DeleteJob(ctx context.Context, uri, ns string) error {
	prop := metav1.DeletePropagationBackground
	if err := p.Kube.BatchV1().Jobs(ns).Delete(ctx, JobName(uri), metav1.DeleteOptions{PropagationPolicy: &prop}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete job %s/%s: %w", ns, JobName(uri), err)
	}
	return nil
}

// DownloadResources are the defaults for a container that downloads a
// model: enough CPU and memory for a parallel fetch, bounded for policy.
var DownloadResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("16Gi")},
}

// HoldResources are the defaults for the container that only keeps a
// staging pod alive.
var HoldResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
}

// StagingReady reports whether a Block-mode staging pod holds a finished
// download: the pod runs and its download init exited 0.
func StagingReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for i := range pod.Status.InitContainerStatuses {
		s := &pod.Status.InitContainerStatuses[i]
		if s.Name == DownloadContainer {
			return s.State.Terminated != nil && s.State.Terminated.ExitCode == 0
		}
	}
	return false
}

// TouchLastUsed records that a pod was just admitted against the primary
// PV, so retention counts from now. Best effort: a failure here must not
// fail an admission, and a missing PV is not an error.
func (p *Provisioner) TouchLastUsed(ctx context.Context, pvName string) error {
	if pvName == "" {
		return nil
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, LastUsedAnnotation, time.Now().UTC().Format(time.RFC3339))
	_, err := p.Kube.CoreV1().PersistentVolumes().Patch(ctx, pvName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// StagingFailed reports whether a Block-mode staging pod's download init
// cannot be made to work: it has been restarted attempts times or more
// under the Job's OnFailure policy. An image that does not run on the
// node (exec format error), a missing download CLI, or a rejected token
// all look like this, and none of them heals with more restarts. The
// reason carries the kubelet's last termination message so the failure
// record says what went wrong.
func StagingFailed(pod *corev1.Pod, attempts int) (string, bool) {
	if pod == nil || attempts <= 0 || pod.DeletionTimestamp != nil {
		return "", false
	}
	for i := range pod.Status.InitContainerStatuses {
		s := &pod.Status.InitContainerStatuses[i]
		if s.Name != DownloadContainer {
			continue
		}
		if s.State.Terminated != nil && s.State.Terminated.ExitCode == 0 {
			return "", false
		}
		if int(s.RestartCount) < attempts {
			return "", false
		}
		last := s.LastTerminationState.Terminated
		if last == nil {
			last = s.State.Terminated
		}
		reason := fmt.Sprintf("download init restarted %d times", s.RestartCount)
		if last != nil {
			reason += fmt.Sprintf(": exit %d %s %s", last.ExitCode, last.Reason, strings.TrimSpace(last.Message))
		}
		return strings.TrimSpace(reason), true
	}
	return "", false
}

// Harden gives a container the fields function-namespace baselines
// require (Kyverno on NVCF clusters: requests and limits on every
// container, no privilege escalation, dropped capabilities with NET_RAW
// named because one rule checks for it by name). Fields the chart
// already set are kept; resources are defaulted only when both requests
// and limits are absent, so a chart's own sizing wins. The user posture
// (runAsNonRoot, runAsUser, runAsGroup, seccomp) is inherited from the
// chart's main container when given: the download runs the same image
// as the engine and must own what it writes, so it runs as the same
// user the engine will read as.
func Harden(c *corev1.Container, resources corev1.ResourceRequirements, from *corev1.SecurityContext) {
	if c.Resources.Limits == nil && c.Resources.Requests == nil {
		c.Resources = *resources.DeepCopy()
	} else if c.Resources.Limits == nil {
		c.Resources.Limits = resources.Limits.DeepCopy()
	}
	if c.SecurityContext == nil {
		c.SecurityContext = &corev1.SecurityContext{}
	}
	sc := c.SecurityContext
	if sc.AllowPrivilegeEscalation == nil {
		sc.AllowPrivilegeEscalation = new(bool)
	}
	if sc.Capabilities == nil {
		sc.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL", "NET_RAW"}}
	}
	if from == nil {
		return
	}
	if sc.RunAsNonRoot == nil && from.RunAsNonRoot != nil {
		sc.RunAsNonRoot = from.RunAsNonRoot
	}
	if sc.RunAsUser == nil && from.RunAsUser != nil {
		sc.RunAsUser = from.RunAsUser
	}
	if sc.RunAsGroup == nil && from.RunAsGroup != nil {
		sc.RunAsGroup = from.RunAsGroup
	}
	if sc.SeccompProfile == nil && from.SeccompProfile != nil {
		sc.SeccompProfile = from.SeccompProfile.DeepCopy()
	}
}

// MarkRefreshStable records on the serving primary that no further
// refresh of this set runs: the same delta came back after a refresh.
func (p *Provisioner) MarkRefreshStable(ctx context.Context, pvName string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, RefreshStableAnnotation)
	_, err := p.Kube.CoreV1().PersistentVolumes().Patch(ctx, pvName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
