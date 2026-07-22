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

package webhook

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// putHash is a helper: makes a small tree, Puts it under hash, returns
// the hash and the populated Manifest.
func putHash(t *testing.T, b *checkpointstore.Local, hash string, vols []checkpointstore.VolumeMeta) checkpointstore.Manifest {
	t.Helper()
	srcDir := t.TempDir()
	m := checkpointstore.Manifest{Volumes: vols}
	stored, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, m)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func newBackend(t *testing.T) *checkpointstore.Local {
	t.Helper()
	b, err := checkpointstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// podWithAnnotation returns a minimal pod with the given annotation value.
func podWithAnnotation(value string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "p",
			Annotations: map[string]string{RestoreFromAnnotation: value},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "vllm/vllm-openai:v0.11.2",
			}},
		},
	}
}

func TestMutate_NoAnnotation(t *testing.T) {
	mut := &Mutator{Backend: newBackend(t)}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{}}}}
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 0 {
		t.Fatalf("no annotation: expected nil patches, got %+v", patches)
	}
}

func TestMutate_NilPod(t *testing.T) {
	mut := &Mutator{Backend: newBackend(t)}
	patches, err := mut.Mutate(context.Background(), nil)
	if err != nil || patches != nil {
		t.Fatalf("nil pod: want (nil, nil), got (%+v, %v)", patches, err)
	}
}

func TestMutate_NoBackend(t *testing.T) {
	mut := &Mutator{} // Backend nil
	pod := podWithAnnotation("auto")
	_, err := mut.Mutate(context.Background(), pod)
	if err == nil {
		t.Fatal("expected error for nil Backend")
	}
}

func TestMutate_HashNotFound(t *testing.T) {
	mut := &Mutator{Backend: newBackend(t)}
	pod := podWithAnnotation("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("hash not found should NOT error (cold-start path); got %v", err)
	}
	if len(patches) != 0 {
		t.Fatalf("hash not found: expected nil patches, got %+v", patches)
	}
}

func TestMutate_AutoWithoutComposerFailsOpen(t *testing.T) {
	mut := &Mutator{Backend: newBackend(t)} // no Composer
	pod := podWithAnnotation("auto")
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatalf("auto without Composer should fail open; got err %v", err)
	}
	if len(patches) != 0 {
		t.Fatalf("auto without Composer: expected nil patches, got %+v", patches)
	}
}

// TestMutate_InjectsVolumesAndMounts is the happy path: a capture exists
// for an explicit hash, the mutator emits volume + mount patches.
func TestMutate_InjectsVolumesAndMounts(t *testing.T) {
	b := newBackend(t)
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	vols := []checkpointstore.VolumeMeta{
		{Name: "nim-cache", MountPath: "/opt/nim/.cache", Type: "emptyDir", SizeBytes: 1, FileCount: 1},
	}
	putHash(t, b, hash, vols)

	mut := &Mutator{Backend: b, MainContainer: 0}
	pod := podWithAnnotation(hash)
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) == 0 {
		t.Fatalf("expected patches, got none")
	}

	// Walk the patches and check shape: volumes bootstrap (since pod has
	// no Volumes), then add /spec/volumes/-, then add /spec/containers/0/volumeMounts (bootstrap),
	// then add /spec/containers/0/volumeMounts/-.
	wantPaths := map[string]int{
		"/spec/volumes":                     1,
		"/spec/volumes/-":                   1,
		"/spec/containers/0/volumeMounts":   1,
		"/spec/containers/0/volumeMounts/-": 1,
	}
	got := make(map[string]int, len(patches))
	for _, p := range patches {
		got[p.Path]++
	}
	for k, v := range wantPaths {
		if got[k] != v {
			t.Errorf("patches: path %q count = %d, want %d (all=%+v)", k, got[k], v, patches)
		}
	}
}

// TestMutate_RootfsIsSkipped — manifest with a whole-rootfs entry (the
// Type="rootfs" upperdir record) must NOT generate patches for it. The
// webhook can't inject a whole rootfs without rewriting the customer's
// container; that's Phase 2+ territory. Catalog-driven RootfsExtractPaths
// is the production path for surfacing rootfs-resident caches.
func TestMutate_RootfsIsSkipped(t *testing.T) {
	b := newBackend(t)
	hash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	putHash(t, b, hash, []checkpointstore.VolumeMeta{
		{Name: "rootfs", MountPath: "/", Type: "rootfs"},
		{Name: "hf-cache", MountPath: "/root/.cache/huggingface", Type: "hostPath"},
	})

	mut := &Mutator{Backend: b}
	pod := podWithAnnotation(hash)
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		if v, ok := p.Value.(corev1.Volume); ok && v.Name == "rootfs" {
			t.Fatalf("rootfs volume should not be injected by webhook (Phase 1): %+v", v)
		}
	}
	// At least the hf-cache volume should be added.
	addVol := 0
	for _, p := range patches {
		if p.Path == "/spec/volumes/-" {
			addVol++
		}
	}
	if addVol != 1 {
		t.Fatalf("expected 1 volume add for hf-cache, got %d", addVol)
	}
}

// TestMutate_RootfsExtractPaths_Injected verifies that catalog-driven
// engine cache subpaths discovered at capture time are shadow-mounted
// onto the customer's pod with NO yaml change required of them. This
// is the production restore path.
func TestMutate_RootfsExtractPaths_Injected(t *testing.T) {
	b := newBackend(t)
	hash := "1111111111111111111111111111111111111111111111111111111111111111"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "rootfs", MountPath: "/", Type: "rootfs", FileCount: 1, SizeBytes: 1},
		},
		RootfsExtractPaths: []checkpointstore.ExtractPath{
			{Path: "/root/.cache/huggingface", Category: "hf-cache",
				SizeBytes: 16 * 1 << 30, FileCount: 4},
			{Path: "/root/.triton", Category: "triton-cache",
				SizeBytes: 5 * 1 << 20, FileCount: 12},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mut := &Mutator{Backend: b}
	pod := podWithAnnotation(hash)
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}

	// Should see two volume adds + two volumeMount adds (one per extract path).
	gotMountPaths := map[string]bool{}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/volumeMounts/-" {
			vm, ok := p.Value.(corev1.VolumeMount)
			if !ok {
				t.Fatalf("volumeMount value wrong type: %T", p.Value)
			}
			gotMountPaths[vm.MountPath] = vm.ReadOnly
		}
	}
	for _, want := range []string{"/root/.cache/huggingface", "/root/.triton"} {
		ro, ok := gotMountPaths[want]
		if !ok {
			t.Errorf("missing volumeMount at %q in %v", want, gotMountPaths)
			continue
		}
		if !ro {
			t.Errorf("volumeMount at %q should be readOnly: customer can't safely write to a shared cache", want)
		}
	}
}

// TestMutate_Rootfs_InjectsRunAsRootNotPrivileged verifies that a rootfs
// warm restore gets securityContext runAsUser=0 on the main container,
// but NOT privileged. The captured tree is root-owned and the engine
// writes into its warmed cache/model dirs through the per-pod OverlayFS
// at startup (NIM/Riva); under NVCA's hardened default (caps drop ALL, no
// runAsUser) the image's non-root user can't write and the engine aborts
// with EACCES — runAsUser=0 fixes that. privileged must NOT be set: a
// privileged main container exposes all host /dev/nvidiaN nodes and
// breaks the device plugin's single-GPU isolation, collapsing fanned-out
// restore pods onto GPU 0 (GCP-H100-a, 2026-06-11). See the
// securityContext block in buildPatches.
func TestMutate_Rootfs_InjectsRunAsRootNotPrivileged(t *testing.T) {
	b := newBackend(t)
	hash := "2222222222222222222222222222222222222222222222222222222222222222"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		CaptureMethod: "rootfs",
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "rootfs", MountPath: "/", Type: "rootfs", FileCount: 1, SizeBytes: 1},
		},
		RootfsExtractPaths: []checkpointstore.ExtractPath{
			{Path: "/opt/nim/.cache", Category: "nim-cache", SizeBytes: 1 << 30, FileCount: 3},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mut := &Mutator{Backend: b}
	patches, err := mut.Mutate(context.Background(), podWithAnnotation(hash))
	if err != nil {
		t.Fatal(err)
	}

	var sawPriv, sawRoot, sawDacOverride bool
	for _, p := range patches {
		// Pod here has no pre-existing securityContext → whole-object add.
		if p.Path != "/spec/containers/0/securityContext" {
			continue
		}
		sc, ok := p.Value.(corev1.SecurityContext)
		if !ok {
			t.Fatalf("securityContext value wrong type: %T", p.Value)
		}
		if sc.Privileged != nil && *sc.Privileged {
			sawPriv = true
		}
		if sc.RunAsUser != nil && *sc.RunAsUser == 0 {
			sawRoot = true
		}
		if sc.Capabilities != nil && hasCapability(sc.Capabilities.Add, "DAC_OVERRIDE") {
			sawDacOverride = true
		}
	}
	if sawPriv {
		t.Error("rootfs restore must NOT inject securityContext.privileged=true (breaks single-GPU isolation)")
	}
	if !sawRoot {
		t.Error("rootfs restore must inject securityContext.runAsUser=0")
	}
	if !sawDacOverride {
		t.Error("rootfs restore must add CAP_DAC_OVERRIDE so root can write the root-owned captured tree without privileged")
	}
}

// TestMutate_RootfsExtractPath_CustomerConflictWins verifies that if
// the customer's pod already has a volumeMount at the same path the
// webhook would inject, we skip our injection. Customer's intent always
// wins — never break their pod.
func TestMutate_RootfsExtractPath_CustomerConflictWins(t *testing.T) {
	b := newBackend(t)
	hash := "2222222222222222222222222222222222222222222222222222222222222222"
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: t.TempDir()}}, checkpointstore.Manifest{
		RootfsExtractPaths: []checkpointstore.ExtractPath{
			{Path: "/root/.cache/huggingface", Category: "hf-cache",
				SizeBytes: 16 * 1 << 30, FileCount: 4},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mut := &Mutator{Backend: b}
	pod := podWithAnnotation(hash)
	// Customer already mounts something at /root/.cache/huggingface.
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "customer-hf", MountPath: "/root/.cache/huggingface"},
	}
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		if p.Path == "/spec/containers/0/volumeMounts/-" {
			vm := p.Value.(corev1.VolumeMount)
			if vm.MountPath == "/root/.cache/huggingface" {
				t.Fatalf("webhook injected over customer's existing mount: %+v", vm)
			}
		}
	}
}

// TestMutate_AutoUsesComposerHash verifies that "auto" runs the composer
// and looks up the resulting hash. We pre-populate the backend with a
// hash that matches the pod's composed input.
func TestMutate_AutoUsesComposerHash(t *testing.T) {
	b := newBackend(t)
	composer := &rootfsonly.HashInputComposer{CUDADriverMajor: 580}
	pod := podWithAnnotation("auto")
	pod.Spec.Containers[0].Args = []string{"vllm serve --model m --tp 2"}

	in := composer.Compose(pod, 0)
	hash := checkpointstore.ComputeHash(in)
	putHash(t, b, hash, []checkpointstore.VolumeMeta{
		{Name: "v", MountPath: "/cache", Type: "emptyDir", FileCount: 1, SizeBytes: 1},
	})

	mut := &Mutator{Backend: b, Composer: composer, MainContainer: 0}
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) == 0 {
		t.Fatal("auto with matching capture: expected patches")
	}
}

// canonicalizingBackend mirrors the production ConfigMapBackend's
// behavior: Stat accepts either the short or full form (via CMNameFor's
// internal ShortHash truncation) and returns a manifest whose Hash
// field is always the canonical full sha256. We can't pull in
// ConfigMapBackend directly without a fake K8s client, so a tiny stub
// captures the only contract resolveHash relies on.
type canonicalizingBackend struct {
	fullHash string
	manifest checkpointstore.Manifest
}

func (c canonicalizingBackend) Put(context.Context, string, []checkpointstore.CaptureSource, checkpointstore.Manifest) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, nil
}
func (c canonicalizingBackend) Get(context.Context, string, string) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, nil
}
func (c canonicalizingBackend) Stat(_ context.Context, hash string) (checkpointstore.Manifest, error) {
	// Treat any prefix of fullHash as a hit (length >= 8 to avoid silly collisions in tests).
	if len(hash) >= 8 && strings.HasPrefix(c.fullHash, hash) {
		return c.manifest, nil
	}
	return checkpointstore.Manifest{}, checkpointstore.ErrNotFound
}
func (c canonicalizingBackend) Delete(context.Context, string) error { return nil }
func (c canonicalizingBackend) Mount(context.Context, string, checkpointstore.VolumeMeta) (checkpointstore.PodMount, error) {
	return checkpointstore.PodMount{}, nil
}

// TestMutate_HashCanonicalization_ShortToFull is the regression test for
// the rootfs restore failure we observed on whisper 2026-06-08. The user
// supplied the short 32-char hash in nvsnap.io/restore-from; the on-disk
// cache uses the full 64-char hash. resolveHash must call Backend.Stat
// and return manifest.Hash (always full) so the init container builds
// the right <cache_dir>/<full_hash>/... path.
func TestMutate_HashCanonicalization_ShortToFull(t *testing.T) {
	fullHash := "eba4aa9bbfbe7159df8ceb37cbfc0fa234453f0ae7faff0ab38261296de55016"
	b := canonicalizingBackend{
		fullHash: fullHash,
		manifest: checkpointstore.Manifest{
			Hash: fullHash,
			Volumes: []checkpointstore.VolumeMeta{
				{Name: "nim-cache", MountPath: "/opt/nim/.cache", Type: "emptyDir", FileCount: 4, SizeBytes: 1532542883},
			},
		},
	}
	mut := &Mutator{Backend: b, MainContainer: 0}

	// Short input — must canonicalize to full.
	shortHash := checkpointstore.ShortHash(fullHash)
	got, err := mut.resolveHash(context.Background(), podWithAnnotation(shortHash), shortHash)
	if err != nil {
		t.Fatalf("resolveHash(short): %v", err)
	}
	if got != fullHash {
		t.Errorf("resolveHash(short): got %q, want canonical full %q", got, fullHash)
	}

	// Full input — must round-trip unchanged.
	got, err = mut.resolveHash(context.Background(), podWithAnnotation(fullHash), fullHash)
	if err != nil {
		t.Fatalf("resolveHash(full): %v", err)
	}
	if got != fullHash {
		t.Errorf("resolveHash(full): got %q, want %q", got, fullHash)
	}

	// Unknown short — cold start. Must return the seed unchanged (so
	// downstream restore-bundle patches still fire) and not error.
	coldShort := "0000000000000000000000000000000000000000"[:checkpointstore.ShortHashLen]
	got, err = mut.resolveHash(context.Background(), podWithAnnotation(coldShort), coldShort)
	if err != nil {
		t.Fatalf("resolveHash(cold-short): %v", err)
	}
	if got != coldShort {
		t.Errorf("resolveHash(cold-short): got %q, want raw seed %q", got, coldShort)
	}
}

// TestMutate_BackendStatError — a non-NotFound Stat error propagates.
func TestMutate_BackendStatError(t *testing.T) {
	mut := &Mutator{Backend: errBackend{err: errors.New("backend down")}}
	pod := podWithAnnotation("abc123")
	_, err := mut.Mutate(context.Background(), pod)
	if err == nil {
		t.Fatal("expected error from backend Stat")
	}
}

// TestMutate_MainContainerOutOfRange — guard against misconfig.
func TestMutate_MainContainerOutOfRange(t *testing.T) {
	b := newBackend(t)
	hash := "abc"
	putHash(t, b, hash, []checkpointstore.VolumeMeta{{Name: "v", MountPath: "/x", Type: "emptyDir"}})
	mut := &Mutator{Backend: b, MainContainer: 5}
	pod := podWithAnnotation(hash)
	_, err := mut.Mutate(context.Background(), pod)
	if err == nil {
		t.Fatal("expected error for MainContainer out of range")
	}
}

// errBackend is a minimal Backend that returns an error from every method.
type errBackend struct{ err error }

func (e errBackend) Put(ctx context.Context, hash string, sources []checkpointstore.CaptureSource, m checkpointstore.Manifest) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, e.err
}
func (e errBackend) Get(ctx context.Context, hash, dst string) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, e.err
}
func (e errBackend) Stat(ctx context.Context, hash string) (checkpointstore.Manifest, error) {
	return checkpointstore.Manifest{}, e.err
}
func (e errBackend) Delete(ctx context.Context, hash string) error { return e.err }
func (e errBackend) Mount(ctx context.Context, hash string, vol checkpointstore.VolumeMeta) (checkpointstore.PodMount, error) {
	return checkpointstore.PodMount{}, e.err
}

// stubOverlay records every PrepareOverlay call and returns a synthetic
// per-volume mountpoint. Mirrors the real *agent.Agent shape so the
// webhook's overlay branch is exercised without spinning up an actual
// OverlayFS mount.
type stubOverlay struct {
	mu    sync.Mutex
	calls []checkpointstore.VolumeMeta
	err   error
}

func (s *stubOverlay) PrepareOverlay(podUID, captureHash string, vol checkpointstore.VolumeMeta, targetNode string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.mu.Lock()
	s.calls = append(s.calls, vol)
	s.mu.Unlock()
	return "/var/lib/nvsnap/overlays/" + podUID + "/merged" + vol.MountPath, nil
}

func (s *stubOverlay) MountpointFor(podUID string, vol checkpointstore.VolumeMeta) (string, error) {
	return "/var/lib/nvsnap/overlays/" + podUID + "/merged" + vol.MountPath, nil
}

// TestMutate_OverlayCoversAllVolumes verifies nvsnap#88: when an
// OverlayPreparer is wired, BOTH section 1 (hostPath/emptyDir
// user-data volumes) and section 2 (rootfs-extract subpaths) get
// writable overlay mountpoints — not just the rootfs-extracts.
// Without this, workloads that write into a captured hostPath cache
// (HF Transformers refs/main, sglang's flashinfer DB) crash with
// EROFS post-restore.
func TestMutate_OverlayCoversAllVolumes(t *testing.T) {
	b := newBackend(t)
	hash := "3333333333333333333333333333333333333333333333333333333333333333"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		// Section 1 entries: hostPath user-data volume captured into
		// tree/volumes/<name>/. This is what DeepSeek-V4-Flash's
		// hf-cache hostPath looked like on GCP-H100-a 2026-06-05.
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "hf-cache", MountPath: "/var/lib/hf-cache", Type: "hostPath", FileCount: 100, SizeBytes: 1 << 30},
		},
		// Section 2 entries: engine-cache subpath under tree/rootfs/.
		RootfsExtractPaths: []checkpointstore.ExtractPath{
			{Path: "/root/.cache/huggingface", Category: "hf-cache", SizeBytes: 16 << 30, FileCount: 4},
		},
	}); err != nil {
		t.Fatal(err)
	}

	overlay := &stubOverlay{}
	mut := &Mutator{Backend: b, OverlayPreparer: overlay}
	pod := podWithAnnotation(hash)
	patches, err := mut.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}

	// Both volumes must have been overlaid (one call each, in
	// section order: hostPath first, then rootfs-extract).
	if got, want := len(overlay.calls), 2; got != want {
		t.Fatalf("PrepareOverlay calls = %d, want %d (vols=%+v)", got, want, overlay.calls)
	}
	if overlay.calls[0].Type != "hostPath" || overlay.calls[0].MountPath != "/var/lib/hf-cache" {
		t.Errorf("section 1 call = %+v, want hostPath /var/lib/hf-cache", overlay.calls[0])
	}
	if overlay.calls[1].Type != "rootfs-extract" || overlay.calls[1].MountPath != "/root/.cache/huggingface" {
		t.Errorf("section 2 call = %+v, want rootfs-extract /root/.cache/huggingface", overlay.calls[1])
	}

	// And both volumeMounts in the patch must be writable (ReadOnly=false)
	// — that's the whole point of the overlay wrap.
	mounts := map[string]bool{} // mountPath → readOnly
	for _, p := range patches {
		if p.Path != "/spec/containers/0/volumeMounts/-" {
			continue
		}
		vm, ok := p.Value.(corev1.VolumeMount)
		if !ok {
			t.Fatalf("volumeMount value wrong type: %T", p.Value)
		}
		mounts[vm.MountPath] = vm.ReadOnly
	}
	for _, mp := range []string{"/var/lib/hf-cache", "/root/.cache/huggingface"} {
		ro, ok := mounts[mp]
		if !ok {
			t.Errorf("missing volumeMount at %q in %v", mp, mounts)
			continue
		}
		if ro {
			t.Errorf("volumeMount at %q must be writable when overlay is wrapped", mp)
		}
	}
}

// TestMutate_OverlayFailureFallsBackToROBind verifies that a failure
// from the OverlayPreparer does not block admission — we fall back to
// the original RO bind. Webhook is fail-open by contract.
func TestMutate_OverlayFailureFallsBackToROBind(t *testing.T) {
	b := newBackend(t)
	hash := "4444444444444444444444444444444444444444444444444444444444444444"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "hf-cache", MountPath: "/var/lib/hf-cache", Type: "hostPath", FileCount: 1, SizeBytes: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mut := &Mutator{Backend: b, OverlayPreparer: &stubOverlay{err: errors.New("overlay mount broken")}}
	patches, err := mut.Mutate(context.Background(), podWithAnnotation(hash))
	if err != nil {
		t.Fatalf("Mutate must not error on overlay failure: %v", err)
	}
	for _, p := range patches {
		if p.Path != "/spec/containers/0/volumeMounts/-" {
			continue
		}
		vm := p.Value.(corev1.VolumeMount)
		if vm.MountPath == "/var/lib/hf-cache" && !vm.ReadOnly {
			t.Fatal("fallback bind must be readOnly")
		}
	}
}

// TestMutate_InitContainerStrategy_DoesNotMountInline verifies #202's
// core promise: with RestorePrepStrategy=init-container, admission
// does NO OverlayFS work — PrepareOverlay must not be called, and a
// single nvsnap-mount-prep init container is emitted instead, carrying
// the full set of captured volumes in NVSNAP_PREP_MOUNTS for the
// init container to POST to the agent's async prep manager.
func TestMutate_InitContainerStrategy_DoesNotMountInline(t *testing.T) {
	b := newBackend(t)
	hash := "5555555555555555555555555555555555555555555555555555555555555555"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "hf-cache", MountPath: "/var/lib/hf-cache", Type: "hostPath", FileCount: 1, SizeBytes: 1},
		},
		RootfsExtractPaths: []checkpointstore.ExtractPath{
			{Path: "/root/.cache/huggingface", Category: "hf-cache", FileCount: 1, SizeBytes: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	overlay := &stubOverlay{}
	mut := &Mutator{
		Backend:             b,
		OverlayPreparer:     overlay,
		RestorePrepStrategy: "init-container",
		MountPrepInitImage:  "registry.example/nvsnap-agent:test",
		AgentHostPort:       8081,
	}
	patches, err := mut.Mutate(context.Background(), podWithAnnotation(hash))
	if err != nil {
		t.Fatal(err)
	}

	if got := len(overlay.calls); got != 0 {
		t.Fatalf("init-container strategy must skip inline mounts; got %d PrepareOverlay calls (%+v)", got, overlay.calls)
	}

	// Exactly one nvsnap-mount-prep init container must be emitted, and
	// NVSNAP_PREP_MOUNTS must enumerate both captured volumes so the
	// init container drives the agent through every overlay.
	var initContainer *corev1.Container
	for _, p := range patches {
		if p.Path != "/spec/initContainers/-" {
			continue
		}
		c, ok := p.Value.(corev1.Container)
		if !ok {
			t.Fatalf("initContainers patch value wrong type: %T", p.Value)
		}
		if c.Name == MountPrepContainerName {
			if initContainer != nil {
				t.Fatalf("expected ONE nvsnap-mount-prep init container, got duplicate")
			}
			cCopy := c
			initContainer = &cCopy
		}
	}
	if initContainer == nil {
		t.Fatalf("no nvsnap-mount-prep init container emitted (patches=%+v)", patches)
	}
	if initContainer.Image != "registry.example/nvsnap-agent:test" {
		t.Errorf("init container image = %q, want registry.example/nvsnap-agent:test", initContainer.Image)
	}

	envFor := func(name string) string {
		for _, e := range initContainer.Env {
			if e.Name == name {
				return e.Value
			}
		}
		return ""
	}
	if got := envFor("NVSNAP_RESTORE_HASH"); got != hash {
		t.Errorf("NVSNAP_RESTORE_HASH=%q, want %q", got, hash)
	}
	mounts := envFor("NVSNAP_PREP_MOUNTS")
	if mounts == "" {
		t.Fatal("NVSNAP_PREP_MOUNTS empty — init container would have nothing to prep")
	}
	if !strings.Contains(mounts, "/var/lib/hf-cache") {
		t.Errorf("NVSNAP_PREP_MOUNTS missing section-1 hostPath /var/lib/hf-cache: %q", mounts)
	}
	if !strings.Contains(mounts, "/root/.cache/huggingface") {
		t.Errorf("NVSNAP_PREP_MOUNTS missing section-2 rootfs-extract /root/.cache/huggingface: %q", mounts)
	}
}

// TestMutate_InitContainerStrategy_NeedsImage guards the misconfiguration
// where someone enables the strategy but forgets MountPrepInitImage.
// Mutate must fall through to the inline path rather than emit a
// broken init container with no image.
func TestMutate_InitContainerStrategy_NeedsImage(t *testing.T) {
	b := newBackend(t)
	hash := "6666666666666666666666666666666666666666666666666666666666666666"
	srcDir := t.TempDir()
	if _, err := b.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcDir}}, checkpointstore.Manifest{
		Volumes: []checkpointstore.VolumeMeta{
			{Name: "hf-cache", MountPath: "/var/lib/hf-cache", Type: "hostPath", FileCount: 1, SizeBytes: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	overlay := &stubOverlay{}
	mut := &Mutator{
		Backend:             b,
		OverlayPreparer:     overlay,
		RestorePrepStrategy: "init-container",
		// MountPrepInitImage intentionally empty
	}
	_, err := mut.Mutate(context.Background(), podWithAnnotation(hash))
	if err != nil {
		t.Fatalf("Mutate must not error when init-container image missing; got %v", err)
	}
	if len(overlay.calls) == 0 {
		t.Fatal("missing MountPrepInitImage must fall back to inline mounts; got 0 PrepareOverlay calls")
	}
}
