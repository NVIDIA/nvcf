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

// StorageProfile selection (nvsnap#171). At startup the agent reads its
// L2 StorageClass, takes the CSI provisioner + parameters.type, and
// resolves a StorageProfile that picks the Promoter strategy. Resolution
// is a layered map: a compiled-in table of providers we've validated,
// overlaid by an optional nvsnap-storage-profiles ConfigMap so a new or
// 3rd-party backend is one `kubectl edit cm` away — no agent rebuild.
//
// Lookup key is provisioner[/parameters.type]: the /type segment
// disambiguates drivers that multiplex volume types (notably
// pd.csi.storage.gke.io, which backs both Hyperdisk-ML and regular PD).
//
// Design: docs/design/STORAGE-AGNOSTIC-L2-PROMOTION.md.

package checkpointstore

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Strategy names. Kept as strings (not an enum) so a ConfigMap profile
// can name one without a code change.
const (
	StrategySnapshotClone = "snapshot-clone"
	StrategySharedVolume  = "shared-volume"
	StrategyCopyPerPod    = "copy-per-pod"
)

// StorageProfile is the resolved per-StorageClass L2 behavior. The JSON
// tags match the nvsnap-storage-profiles ConfigMap (parsed via
// sigs.k8s.io/yaml, which honors json tags).
type StorageProfile struct {
	// Strategy selects the Promoter (StrategySnapshotClone/SharedVolume/CopyPerPod).
	Strategy string `json:"strategy"`
	// SnapshotClass is the VolumeSnapshotClass for snapshot-clone. Empty
	// for shared-volume.
	SnapshotClass string `json:"snapshotClass,omitempty"`
	// ReadOnlyMany: snapshot-clone produces one shared ROX claim (true) vs
	// a per-pod RWO clone (false). Ignored for shared-volume (always ROX).
	ReadOnlyMany bool `json:"readOnlyMany,omitempty"`
	// VolumeHandleTransform names the shared-volume handle rewrite
	// ("nvmesh" | "none"). Ignored for snapshot-clone.
	VolumeHandleTransform string `json:"volumeHandleTransform,omitempty"`
	// MountOptions are set on the minted read-only PV (shared-volume) so N
	// pods can mount the same backing filesystem read-only. For NVMesh
	// (xfs block volume) this MUST include "nouuid" — xfs refuses a second
	// mount of the same filesystem UUID otherwise — plus "ro,norecovery"
	// (RO mount of a possibly-dirty log). Empty ⇒ inherit the primary PV's
	// options unchanged (fine for NFS-like backends: EFS, Filestore).
	MountOptions []string `json:"mountOptions,omitempty"`
	// Prewarm controls the nvsnap-prewarm init container the webhook adds
	// to a cachedir restore: a parallel sequential read of the whole rox
	// tree so the engine's weight load hits page cache. nil (absent) means
	// on. Whether it helps is a property of the volume, not the model: on
	// Hyperdisk ML the engine's per-fault mmap reads leave a high-throughput
	// volume idle and the sweep wins; on a volume a single reader already
	// saturates (NVMesh, measured 70B TP=4: 247 s vs 257 s) it is neutral.
	// A pod's own NVSNAP_PREWARM=0/1 overrides this either way.
	Prewarm *bool `json:"prewarm,omitempty"`
	// PrewarmParallelism is the number of concurrent readers in the sweep.
	// 0 means DefaultPrewarmParallelism.
	PrewarmParallelism int `json:"prewarmParallelism,omitempty"`
	// PrewarmMaxBytes caps the volume size the sweep is applied to, as a
	// Kubernetes quantity ("256Gi"). A tree larger than the node's page
	// cache evicts its own early pages while the sweep reads the late
	// ones, so the engine finds most of it cold anyway and the sweep only
	// costs time (GB300, 2026-10-01: a 1.56 TB tree on a 902 GiB node,
	// about 12 min per pod for nothing). Empty means DefaultPrewarmMaxBytes.
	PrewarmMaxBytes string `json:"prewarmMaxBytes,omitempty"`
	// ModelVolume configures the write-once model volume for Helm
	// functions (docs/proposals/helm-shared-model-volume.md). Mode "block"
	// is the default for shared-volume strategies (NVMesh): the writer's
	// claim on the L2 class becomes the read-only artifact. Mode "rwx"
	// needs a ReadWriteMany class of a distributed filesystem. Empty
	// mode with no default leaves Helm functions untouched.
	ModelVolume *ModelVolumeProfile `json:"modelVolume,omitempty"`
}

// ModelVolumeProfile is the per-storage-class model volume setting.
type ModelVolumeProfile struct {
	// Mode is "rwx" or "block".
	Mode string `json:"mode"`
	// StorageClass for the volume; empty uses the L2 class.
	StorageClass string `json:"storageClass,omitempty"`
	// Size of an rwx-mode claim, created at admission before anything is
	// downloaded, so a ceiling. Empty means "512Gi". Block-mode claims
	// ignore it: they are sized from the downloaded bytes.
	Size string `json:"size,omitempty"`
	// MinSize is the floor for a block-mode claim sized from the download.
	// Empty means "1Gi".
	MinSize string `json:"minSize,omitempty"`
	// ReaderMode for block volumes: "pvc" (default; policy-friendly, pods
	// wait on volume binding) or "hostPath" (schedules at once, agent binds;
	// needed under gang schedulers, requires hostPath allowed by policy).
	ReaderMode string `json:"readerMode,omitempty"`
}

// DefaultPrewarmParallelism is the reader count when a profile does not set
// one; matches the retired Go prewarmer.
const DefaultPrewarmParallelism = 6

// PrewarmEnabled reports the profile's prewarm default (on when unset).
func (p StorageProfile) PrewarmEnabled() bool {
	return p.Prewarm == nil || *p.Prewarm
}

// DefaultPrewarmMaxBytes is the largest volume the sweep is applied to
// when a profile does not set prewarmMaxBytes: 256 GiB fits the page cache
// of every GPU node class in use with room for the engine.
const DefaultPrewarmMaxBytes = int64(256) << 30

// PrewarmLimit returns the largest volume size the sweep is applied to.
func (p StorageProfile) PrewarmLimit() int64 {
	if p.PrewarmMaxBytes == "" {
		return DefaultPrewarmMaxBytes
	}
	q, err := resource.ParseQuantity(p.PrewarmMaxBytes)
	if err != nil || q.Value() <= 0 {
		return DefaultPrewarmMaxBytes
	}
	return q.Value()
}

// PrewarmWorkers returns the reader count, defaulted and clamped to >= 1.
func (p StorageProfile) PrewarmWorkers() int {
	if p.PrewarmParallelism < 1 {
		return DefaultPrewarmParallelism
	}
	return p.PrewarmParallelism
}

// builtinProfiles is the compiled-in provider table. Keyed by
// provisioner[/parameters.type]. A ConfigMap entry for the same key
// overrides the built-in.
var builtinProfiles = map[string]StorageProfile{
	// GKE PD CSI multiplexes volume types — disambiguate on parameters.type.
	"pd.csi.storage.gke.io/hyperdisk-ml": {Strategy: StrategySnapshotClone, SnapshotClass: "hdml-images-snapshot-class", ReadOnlyMany: true},
	"pd.csi.storage.gke.io/pd-ssd":       {Strategy: StrategySnapshotClone, SnapshotClass: "csi-gce-pd-snapshot-class", ReadOnlyMany: false},
	"pd.csi.storage.gke.io/pd-balanced":  {Strategy: StrategySnapshotClone, SnapshotClass: "csi-gce-pd-snapshot-class", ReadOnlyMany: false},
	// AWS EBS — RWO only ⇒ per-pod clone.
	"ebs.csi.aws.com": {Strategy: StrategySnapshotClone, SnapshotClass: "ebs-snapshot-class", ReadOnlyMany: false},
	// Zero-copy shared-volume backends.
	// Prewarm on with eight readers: an NVMesh volume reads 270 MB/s per
	// stream and scales to 2.1 GB/s at eight (dev1 2026-09-28), while the
	// engine's mmap load is a single stream (15 GB in 33 s). The sweep
	// must split files into ranges to use that; see webhook prewarmCommand.
	"nvmesh-csi.excelero.com":      {Strategy: StrategySharedVolume, VolumeHandleTransform: "nvmesh", MountOptions: []string{"ro", "norecovery", "nouuid"}, PrewarmParallelism: 8},
	"efs.csi.aws.com":              {Strategy: StrategySharedVolume, VolumeHandleTransform: "none"},
	"filestore.csi.storage.gke.io": {Strategy: StrategySharedVolume, VolumeHandleTransform: "none"},
}

// ResolveStorageProfile picks a profile for (provisioner, volType),
// overlaying `overlay` (from the ConfigMap) on the built-in table.
// Lookup order: provisioner/type then bare provisioner, ConfigMap before
// built-in at each. Returns (profile, matchedKey, true) on a hit, or
// (_, "", false) when nothing matches (caller disables L2).
func ResolveStorageProfile(provisioner, volType string, overlay map[string]StorageProfile) (StorageProfile, string, bool) {
	try := func(key string) (StorageProfile, bool) {
		if p, ok := overlay[key]; ok {
			return p, true
		}
		if p, ok := builtinProfiles[key]; ok {
			return p, true
		}
		return StorageProfile{}, false
	}
	if volType != "" {
		if p, ok := try(provisioner + "/" + volType); ok {
			return p, provisioner + "/" + volType, true
		}
	}
	if p, ok := try(provisioner); ok {
		return p, provisioner, true
	}
	return StorageProfile{}, "", false
}

// ParseConfigMapProfiles parses the profiles.yaml body of the
// nvsnap-storage-profiles ConfigMap into the overlay map. Empty input
// yields a nil map (no overlay). Keyed by provisioner[/type].
func ParseConfigMapProfiles(profilesYAML string) (map[string]StorageProfile, error) {
	if profilesYAML == "" {
		return nil, nil
	}
	var m map[string]StorageProfile
	if err := yaml.Unmarshal([]byte(profilesYAML), &m); err != nil {
		return nil, fmt.Errorf("parse nvsnap-storage-profiles profiles.yaml: %w", err)
	}
	return m, nil
}

// NewPromoterFromProfile constructs the Promoter for a resolved profile.
// sc is the L2 StorageClass name (used by both strategies to provision /
// match the durable artifact).
func NewPromoterFromProfile(p StorageProfile, sc string, kc kubernetes.Interface, dyn dynamic.Interface, log logrus.FieldLogger) (Promoter, error) {
	switch p.Strategy {
	case StrategySnapshotClone:
		return &SnapshotClonePromoter{
			KubeClient:    kc,
			DynClient:     dyn,
			StorageClass:  sc,
			SnapshotClass: p.SnapshotClass,
			ReadOnlyMany:  p.ReadOnlyMany,
			Log:           log,
		}, nil
	case StrategySharedVolume:
		tx, err := LookupVolumeHandleTransform(p.VolumeHandleTransform)
		if err != nil {
			return nil, err
		}
		return &SharedVolumePromoter{KubeClient: kc, StorageClass: sc, Transform: tx, MountOptions: p.MountOptions, Log: log}, nil
	case StrategyCopyPerPod:
		return nil, fmt.Errorf("storage profile strategy %q not implemented (use snapshot-clone or shared-volume; copy-per-pod is reserved)", p.Strategy)
	default:
		return nil, fmt.Errorf("unknown storage profile strategy %q", p.Strategy)
	}
}
