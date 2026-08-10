// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpointstore

import "testing"

func TestResolveStorageProfile_BuiltinCompositeKey(t *testing.T) {
	// pd.csi multiplexes types — hyperdisk-ml ⇒ ROX shared, pd-ssd ⇒ per-pod.
	hd, key, ok := ResolveStorageProfile("pd.csi.storage.gke.io", "hyperdisk-ml", nil)
	if !ok || key != "pd.csi.storage.gke.io/hyperdisk-ml" {
		t.Fatalf("hyperdisk-ml resolve: ok=%v key=%q", ok, key)
	}
	if hd.Strategy != StrategySnapshotClone || !hd.ReadOnlyMany {
		t.Errorf("hyperdisk-ml profile = %+v, want snapshot-clone + ROX", hd)
	}
	pd, _, ok := ResolveStorageProfile("pd.csi.storage.gke.io", "pd-ssd", nil)
	if !ok || pd.ReadOnlyMany {
		t.Errorf("pd-ssd profile = %+v, want snapshot-clone per-pod (ReadOnlyMany=false)", pd)
	}
}

func TestResolveStorageProfile_SharedVolume(t *testing.T) {
	nv, _, ok := ResolveStorageProfile("nvmesh-csi.excelero.com", "", nil)
	if !ok || nv.Strategy != StrategySharedVolume || nv.VolumeHandleTransform != "nvmesh" {
		t.Errorf("nvmesh profile = %+v, want shared-volume + nvmesh transform", nv)
	}
	// NVMesh is xfs block: RO multi-mount needs ro,norecovery,nouuid.
	if len(nv.MountOptions) != 3 || nv.MountOptions[0] != "ro" || nv.MountOptions[1] != "norecovery" || nv.MountOptions[2] != "nouuid" {
		t.Errorf("nvmesh mountOptions = %v, want [ro norecovery nouuid]", nv.MountOptions)
	}
	efs, _, ok := ResolveStorageProfile("efs.csi.aws.com", "", nil)
	if !ok || efs.Strategy != StrategySharedVolume || efs.VolumeHandleTransform != "none" {
		t.Errorf("efs profile = %+v, want shared-volume + none transform", efs)
	}
	// EFS (NFS-like) needs no special RO options — must stay empty.
	if len(efs.MountOptions) != 0 {
		t.Errorf("efs mountOptions = %v, want empty (NFS-like, no xfs nouuid)", efs.MountOptions)
	}
}

func TestResolveStorageProfile_ConfigMapOverridesBuiltin(t *testing.T) {
	overlay := map[string]StorageProfile{
		// Override hyperdisk-ml + add a brand-new 3rd-party backend.
		"pd.csi.storage.gke.io/hyperdisk-ml": {Strategy: StrategySnapshotClone, SnapshotClass: "custom-snap", ReadOnlyMany: true},
		"vendor.example.com/fast":            {Strategy: StrategySharedVolume, VolumeHandleTransform: "none"},
	}
	hd, _, _ := ResolveStorageProfile("pd.csi.storage.gke.io", "hyperdisk-ml", overlay)
	if hd.SnapshotClass != "custom-snap" {
		t.Errorf("overlay did not override built-in: snapshotClass=%q", hd.SnapshotClass)
	}
	v, _, ok := ResolveStorageProfile("vendor.example.com", "fast", overlay)
	if !ok || v.Strategy != StrategySharedVolume {
		t.Errorf("3rd-party overlay entry not resolved: %+v ok=%v", v, ok)
	}
}

func TestResolveStorageProfile_NoMatch(t *testing.T) {
	if _, _, ok := ResolveStorageProfile("unknown.csi.driver", "", nil); ok {
		t.Error("unknown provisioner should not resolve (caller disables L2)")
	}
}

func TestParseConfigMapProfiles(t *testing.T) {
	if m, err := ParseConfigMapProfiles(""); err != nil || m != nil {
		t.Errorf("empty input: m=%v err=%v, want nil,nil", m, err)
	}
	yamlBody := `
nvmesh-csi.excelero.com:
  strategy: shared-volume
  volumeHandleTransform: nvmesh
ebs.csi.aws.com:
  strategy: snapshot-clone
  snapshotClass: ebs-snapshot-class
  readOnlyMany: false
`
	m, err := ParseConfigMapProfiles(yamlBody)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["nvmesh-csi.excelero.com"].VolumeHandleTransform != "nvmesh" {
		t.Errorf("nvmesh entry not parsed: %+v", m["nvmesh-csi.excelero.com"])
	}
	if m["ebs.csi.aws.com"].SnapshotClass != "ebs-snapshot-class" {
		t.Errorf("ebs entry not parsed: %+v", m["ebs.csi.aws.com"])
	}
}

func TestNewPromoterFromProfile(t *testing.T) {
	sc, _, _ := ResolveStorageProfile("pd.csi.storage.gke.io", "hyperdisk-ml", nil)
	p, err := NewPromoterFromProfile(sc, "hyperdisk-ml", nil, nil, nil)
	if err != nil {
		t.Fatalf("snapshot-clone construct: %v", err)
	}
	if p.Caps().Strategy != StrategySnapshotClone || !p.Caps().ReadOnlyMany {
		t.Errorf("snapshot-clone caps = %+v", p.Caps())
	}

	sv, _, _ := ResolveStorageProfile("nvmesh-csi.excelero.com", "", nil)
	p2, err := NewPromoterFromProfile(sv, "nvmesh-sc", nil, nil, nil)
	if err != nil {
		t.Fatalf("shared-volume construct: %v", err)
	}
	if !p2.Caps().SharedVolume {
		t.Errorf("shared-volume caps = %+v", p2.Caps())
	}

	// Unknown strategy errors.
	if _, err := NewPromoterFromProfile(StorageProfile{Strategy: "bogus"}, "x", nil, nil, nil); err == nil {
		t.Error("unknown strategy should error")
	}
}
