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

// Tests for the L2 PVC fast path in the mutating webhook (nvsnap#63).
// Mocks the L2 Backend (we don't want to spin up real K8s for this)
// and asserts:
//   - On rox-<hash> Bound: PVC volume + RO mount + CHECKPOINT_PATH env
//     are emitted; no hostPath, no nodeAffinity
//   - On ErrNotFound: fall through to existing L1 path (which is
//     tested separately)
//   - On unexpected error: also falls through (fail-open)

package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// stubL2Backend lets tests dictate exactly what Mount() returns.
// Implements checkpointstore.Backend (Store + Mounter) — the other
// Store methods aren't exercised in these tests.
type stubL2Backend struct {
	mountResult checkpointstore.PodMount
	mountErr    error
}

func (s *stubL2Backend) Put(ctx context.Context, hash string, sources []checkpointstore.CaptureSource, m checkpointstore.Manifest) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, errors.New("stub")
}
func (s *stubL2Backend) Get(ctx context.Context, hash, dst string) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, errors.New("stub")
}
func (s *stubL2Backend) Stat(ctx context.Context, hash string) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{Hash: hash}, nil
}
func (s *stubL2Backend) Delete(ctx context.Context, hash string) error { return nil }
func (s *stubL2Backend) Mount(ctx context.Context, hash string, vol checkpointstore.VolumeMeta) (checkpointstore.PodMount, error) {
	return s.mountResult, s.mountErr
}

func TestL2_PVCMountEmitted_WhenRoxBound(t *testing.T) {
	// Stub returns a PodMount as if rox-<hash> is Bound.
	l2 := &stubL2Backend{
		mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{
				Name: "nvsnap-checkpoint",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "rox-abc12345",
						ReadOnly:  true,
					},
				},
			},
			VolumeMount: corev1.VolumeMount{
				Name:      "nvsnap-checkpoint",
				MountPath: "/nvsnap-checkpoint",
				ReadOnly:  true,
			},
		},
	}
	// L1 backend will be ignored when L2 succeeds; use a Local that
	// has NO captures so we know L1 path didn't fire.
	l1 := newBackend(t)
	m := &Mutator{
		Backend:   l1,
		L2Backend: l2,
		// Inference container is index 0.
		MainContainer: 0,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "ns1",
			Name:        "vllm-restored",
			Annotations: map[string]string{RestoreFromAnnotation: "abc12345"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inference"}},
		},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(patches) == 0 {
		t.Fatalf("expected L2 PVC patches, got none — L1 path was probably hit")
	}

	// Verify the emitted patches contain:
	//   - a volume with persistentVolumeClaim.claimName = rox-abc12345
	//   - a volumeMount on container[0] at /nvsnap-checkpoint, readOnly
	//   - an env var CHECKPOINT_PATH = /nvsnap-checkpoint
	//   - NO nodeAffinity patches (L1's signature)
	saw := struct {
		volume       bool
		volumeMount  bool
		envVar       bool
		nodeAffinity bool
	}{}
	for _, p := range patches {
		switch {
		case p.Path == "/spec/volumes" || p.Path == "/spec/volumes/-":
			// Parse the value back to corev1.Volume via JSON.
			raw, _ := json.Marshal(p.Value)
			var single corev1.Volume
			if json.Unmarshal(raw, &single) == nil && single.PersistentVolumeClaim != nil {
				if single.PersistentVolumeClaim.ClaimName == "rox-abc12345" {
					saw.volume = true
				}
			}
			var list []corev1.Volume
			if json.Unmarshal(raw, &list) == nil && len(list) == 1 && list[0].PersistentVolumeClaim != nil {
				if list[0].PersistentVolumeClaim.ClaimName == "rox-abc12345" {
					saw.volume = true
				}
			}
		case p.Path == "/spec/containers/0/volumeMounts" || p.Path == "/spec/containers/0/volumeMounts/-":
			raw, _ := json.Marshal(p.Value)
			var single corev1.VolumeMount
			if json.Unmarshal(raw, &single) == nil && single.MountPath == "/nvsnap-checkpoint" && single.ReadOnly {
				saw.volumeMount = true
			}
			var list []corev1.VolumeMount
			if json.Unmarshal(raw, &list) == nil && len(list) == 1 && list[0].MountPath == "/nvsnap-checkpoint" {
				saw.volumeMount = true
			}
		case p.Path == "/spec/containers/0/env" || p.Path == "/spec/containers/0/env/-":
			raw, _ := json.Marshal(p.Value)
			var single corev1.EnvVar
			if json.Unmarshal(raw, &single) == nil && single.Name == "CHECKPOINT_PATH" && single.Value == "/nvsnap-checkpoint" {
				saw.envVar = true
			}
			var list []corev1.EnvVar
			if json.Unmarshal(raw, &list) == nil && len(list) == 1 && list[0].Name == "CHECKPOINT_PATH" {
				saw.envVar = true
			}
		case p.Path == "/spec/affinity" || p.Path == "/spec/affinity/nodeAffinity":
			saw.nodeAffinity = true
		}
	}
	if !saw.volume {
		t.Errorf("missing PVC volume patch with claimName=rox-abc12345; patches=%+v", patches)
	}
	if !saw.volumeMount {
		t.Errorf("missing volumeMount patch at /nvsnap-checkpoint; patches=%+v", patches)
	}
	if !saw.envVar {
		t.Errorf("missing CHECKPOINT_PATH env var patch; patches=%+v", patches)
	}
	if saw.nodeAffinity {
		t.Errorf("L2 path should NOT emit nodeAffinity; patches=%+v", patches)
	}
}

// TestL2_InjectsNvSnapL2WaitInitContainer asserts that when L2WaitImage
// is configured, the webhook prepends a nvsnap-l2-wait init container
// alongside the PVC volume/mount/env patches. This is the half of
// nvsnap#147 that makes restore actually use the rox PVC — without
// it the pod admits with a PVC reference that kubelet can't bind
// because the agent's snap+clone is still in flight.
func TestL2_InjectsNvSnapL2WaitInitContainer(t *testing.T) {
	l2 := &stubL2Backend{
		mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{
				Name: "nvsnap-checkpoint",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "rox-deadbeef",
						ReadOnly:  true,
					},
				},
			},
			VolumeMount: corev1.VolumeMount{
				Name:      "nvsnap-checkpoint",
				MountPath: "/nvsnap-checkpoint",
				ReadOnly:  true,
			},
		},
	}
	m := &Mutator{
		Backend:         newBackend(t),
		L2Backend:       l2,
		MainContainer:   0,
		L2WaitImage:     "nvcr.io/test/nvsnap-l2-wait:v0.0.1",
		NvSnapServerURL: "http://nvsnap-server.nvsnap-system.svc.cluster.local:8080",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "nvcf-backend",
			Name:        "vllm-restored",
			Annotations: map[string]string{RestoreFromAnnotation: "deadbeef00000000000000000000000000000000000000000000000000000000"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inference"}},
		},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	var waitC *corev1.Container
	var injectedAt string
	for _, p := range patches {
		if p.Path != "/spec/initContainers" && p.Path != "/spec/initContainers/0" {
			continue
		}
		raw, _ := json.Marshal(p.Value)
		var single corev1.Container
		if json.Unmarshal(raw, &single) == nil && single.Name == "nvsnap-l2-wait" {
			waitC = &single
			injectedAt = p.Path
			continue
		}
		var list []corev1.Container
		if json.Unmarshal(raw, &list) == nil && len(list) >= 1 && list[0].Name == "nvsnap-l2-wait" {
			waitC = &list[0]
			injectedAt = p.Path
		}
	}

	if waitC == nil {
		t.Fatalf("no nvsnap-l2-wait init container injected; patches=%+v", patches)
	}
	if injectedAt != "/spec/initContainers" && injectedAt != "/spec/initContainers/0" {
		t.Errorf("nvsnap-l2-wait must be at index 0 (runs before other init containers); got %q", injectedAt)
	}
	if waitC.Image != "nvcr.io/test/nvsnap-l2-wait:v0.0.1" {
		t.Errorf("Image = %q, want test image", waitC.Image)
	}

	// Env contract: NVSNAP_SERVER_URL + NVSNAP_CHECKPOINT_HASH must be
	// present, both non-empty. nvsnap-l2-wait's main() reads these
	// and exits with code 3 if either is missing.
	sawURL, sawHash := false, false
	for _, e := range waitC.Env {
		if e.Name == "NVSNAP_SERVER_URL" && e.Value == "http://nvsnap-server.nvsnap-system.svc.cluster.local:8080" {
			sawURL = true
		}
		if e.Name == "NVSNAP_CHECKPOINT_HASH" && e.Value == "deadbeef00000000000000000000000000000000000000000000000000000000" {
			sawHash = true
		}
	}
	if !sawURL {
		t.Errorf("missing NVSNAP_SERVER_URL env on nvsnap-l2-wait; env=%+v", waitC.Env)
	}
	if !sawHash {
		t.Errorf("missing NVSNAP_CHECKPOINT_HASH env on nvsnap-l2-wait; env=%+v", waitC.Env)
	}

	// Resource guard rails — the init container should NOT have unbounded
	// limits (single restore pod = single nvsnap-l2-wait; many restores =
	// many nvsnap-l2-wait; uncapped would let a buggy server response
	// cascade into memory exhaustion).
	if waitC.Resources.Limits == nil {
		t.Errorf("nvsnap-l2-wait must have resource limits (defense in depth); resources=%+v", waitC.Resources)
	}
}

// TestL2_NoNvSnapL2WaitInject_WhenImageEmpty — back-compat path: a
// cluster that hasn't deployed nvsnap-l2-wait yet (L2WaitImage="")
// must still get the PVC mount, just without the wait container.
// The pod will sit in ContainerCreating until kubelet's WFC binder
// can bind the rox PVC (which works once snap+clone is done).
func TestL2_NoNvSnapL2WaitInject_WhenImageEmpty(t *testing.T) {
	l2 := &stubL2Backend{
		mountResult: checkpointstore.PodMount{
			Volume: corev1.Volume{
				Name: "nvsnap-checkpoint",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "rox-abc12345",
					},
				},
			},
			VolumeMount: corev1.VolumeMount{Name: "nvsnap-checkpoint", MountPath: "/nvsnap-checkpoint"},
		},
	}
	m := &Mutator{
		Backend:       newBackend(t),
		L2Backend:     l2,
		MainContainer: 0,
		// L2WaitImage intentionally empty
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "ns",
			Name:        "vllm-restored",
			Annotations: map[string]string{RestoreFromAnnotation: "abc12345"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "inference"}}},
	}

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	for _, p := range patches {
		if p.Path == "/spec/initContainers" || p.Path == "/spec/initContainers/0" {
			raw, _ := json.Marshal(p.Value)
			if !json.Valid(raw) {
				continue
			}
			// Anything with name "nvsnap-l2-wait" → fail; back-compat path
			// shouldn't inject when image is unset.
			if len(raw) != 0 && (string(raw) != "null") {
				var single corev1.Container
				if json.Unmarshal(raw, &single) == nil && single.Name == "nvsnap-l2-wait" {
					t.Errorf("L2WaitImage empty but nvsnap-l2-wait injected anyway; patch=%+v", p)
				}
			}
		}
	}
}

func TestL2_FallsThroughToL1_OnNotFound(t *testing.T) {
	// L2 returns ErrNotFound — the rox-<hash> PVC isn't Bound (or
	// doesn't exist). Mutator should fall through to the L1 path.
	// L1 backend has no captures either, so the result is nil (cold
	// start) — but Mutate must NOT error.
	l2 := &stubL2Backend{mountErr: checkpointstore.ErrNotFound}
	l1 := newBackend(t)
	m := &Mutator{Backend: l1, L2Backend: l2, MainContainer: 0}

	pod := podWithAnnotation("abc12345")
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if patches != nil {
		// L1 has no captures so should return nil patches (cold start).
		t.Errorf("expected nil patches (L1 cold start); got %+v", patches)
	}
}

func TestL2_FallsThroughToL1_OnUnexpectedError(t *testing.T) {
	// Any other L2 error — broken K8s API client, transient — should
	// also fall through, not fail admission. Webhook fail-open principle.
	l2 := &stubL2Backend{mountErr: errors.New("k8s api dial timeout")}
	l1 := newBackend(t)
	m := &Mutator{Backend: l1, L2Backend: l2, MainContainer: 0}
	pod := podWithAnnotation("abc12345")

	_, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Errorf("Mutate must fail open on L2 errors; got %v", err)
	}
}

func TestL2_NilBackend_BehavesLikeL1Only(t *testing.T) {
	// L2Backend is nil — the L2 fast path is disabled entirely.
	// Mutate should behave exactly as it did pre-nvsnap#63.
	l1 := newBackend(t)
	m := &Mutator{Backend: l1, L2Backend: nil, MainContainer: 0}
	pod := podWithAnnotation("abc12345")

	_, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Errorf("Mutate with nil L2Backend: %v", err)
	}
}
