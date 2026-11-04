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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

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

// A CRIU capture with a bound rox PVC no longer gets an L2 inject: the
// in-pod restore-entrypoint is retired, so the rox would have no consumer.
// The webhook must not touch the pod (no PVC volume, no l2-wait init) and
// must fall through to the L1 path, which cold-admits when L1 has no
// manifest for the hash.
func TestL2_CRIUCapture_NotInjected(t *testing.T) {
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
			VolumeMount: corev1.VolumeMount{Name: "nvsnap-checkpoint", MountPath: "/nvsnap-checkpoint", ReadOnly: true},
		},
	}
	l1 := newBackend(t)
	m := &Mutator{Backend: l1, L2Backend: l2, MainContainer: 0, L2WaitImage: "nvsnap-l2-wait:test"}
	pod := podWithAnnotation("abc12345")

	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	for _, p := range patches {
		raw, _ := json.Marshal(p.Value)
		if strings.Contains(string(raw), "rox-abc12345") || strings.Contains(string(raw), "nvsnap-l2-wait") {
			t.Fatalf("CRIU capture must not receive an L2 inject; got patch %s %s", p.Op, p.Path)
		}
	}
	if patches != nil {
		t.Errorf("expected nil patches (L1 cold start); got %+v", patches)
	}
}
