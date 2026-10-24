// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// fakeElector returns a fixed role and records whether it was asked.
type fakeElector struct {
	role   election.Role
	err    error
	called int
	hash   string
}

func (f *fakeElector) Elect(_ context.Context, hash string, _ *corev1.Pod) (election.Role, string, error) {
	f.called++
	f.hash = hash
	if f.role == election.RoleLeader {
		return f.role, "id-1", f.err
	}
	return f.role, "", f.err
}

// fixedArch establishes every pod's architecture as cc, or none.
type fixedArch struct {
	cc  string
	ok  bool
	err error
}

func (f fixedArch) ComputeCapability(context.Context, *corev1.Pod) (cc string, ok bool, err error) {
	return f.cc, f.ok, f.err
}

// testCC is the compute capability electionMutator establishes.
const testCC = "9.0"

// electionHash is the hash the election composes for pod on testCC.
func electionHash(pod *corev1.Pod) string {
	in := (&rootfsonly.HashInputComposer{CUDADriverMajor: 580}).Compose(pod, 0)
	in.GPUComputeCapability = testCC
	return checkpointstore.ComputeHash(in)
}

// pendingStub is an L2 backend that can (or cannot) name the claim ahead
// of the promote, on top of the shared stub's Mount behaviour.
type pendingStub struct {
	stubL2Backend
	pendingOK bool
}

func (p *pendingStub) PendingMountSpec(hash string, vol checkpointstore.VolumeMeta) (checkpointstore.PodMount, bool) {
	if !p.pendingOK {
		return checkpointstore.PodMount{}, false
	}
	name := vol.Name
	return checkpointstore.PodMount{
		Volume: corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-" + checkpointstore.ShortHash(hash), ReadOnly: true}}},
		VolumeMount: corev1.VolumeMount{Name: name, MountPath: vol.MountPath, ReadOnly: true},
	}, true
}

// dynamoWorker is a chart-shaped model worker: Deployment pod with no
// name yet, GPU request, Dynamo list-form args, no nvsnap annotations.
func dynamoWorker() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fn-ns", GenerateName: "dgd-worker-"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main", Image: "vllm/vllm-openai:v0.20.0",
			Command:   []string{"python3", "-m", "dynamo.vllm"},
			Args:      []string{"--model", "Qwen/Qwen3-0.6B", "--is-decode-worker"},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}},
		}}},
	}
}

func electionMutator(t *testing.T, el election.Elector, l2 checkpointstore.Backend) *Mutator {
	t.Helper()
	return &Mutator{
		Backend:         newBackend(t),
		L2Backend:       l2,
		CacheDir:        "/opt/nvsnap",
		Composer:        &rootfsonly.HashInputComposer{CUDADriverMajor: 580},
		Elector:         el,
		ArchScope:       fixedArch{cc: testCC, ok: true},
		L2WaitImage:     "nvsnap-l2-wait:test",
		NvSnapServerURL: "http://nvsnap-server:8080",
	}
}

type patchView struct {
	labels, annotations map[string]string
	inits               []string
	claims              []string
	gates               int
	commandTouched      bool
	cacheEmptyDir       bool
	envNames            map[string]bool
}

func viewPatches(patches []PatchOp) patchView {
	v := patchView{labels: map[string]string{}, annotations: map[string]string{}, envNames: map[string]bool{}}
	for i := range patches {
		p := &patches[i]
		switch {
		case strings.HasPrefix(p.Path, "/metadata/labels/"):
			v.labels[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/labels/"), "~1", "/")] = p.Value.(string)
		case strings.HasPrefix(p.Path, "/metadata/annotations/"):
			v.annotations[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/annotations/"), "~1", "/")] = p.Value.(string)
		case strings.HasSuffix(p.Path, "/command") || strings.HasSuffix(p.Path, "/args"):
			v.commandTouched = true
		case strings.HasPrefix(p.Path, "/spec/schedulingGates"):
			v.gates++
		}
		switch val := p.Value.(type) {
		case corev1.Container:
			if strings.HasPrefix(p.Path, "/spec/initContainers") {
				if strings.HasSuffix(p.Path, "/0") {
					v.inits = append([]string{val.Name}, v.inits...)
				} else {
					v.inits = append(v.inits, val.Name)
				}
			}
		case []corev1.Container:
			for i := range val {
				v.inits = append(v.inits, val[i].Name)
			}
		case corev1.Volume:
			if val.PersistentVolumeClaim != nil {
				v.claims = append(v.claims, val.PersistentVolumeClaim.ClaimName)
			}
			if val.Name == cacheDirVolumeName && val.EmptyDir != nil {
				v.cacheEmptyDir = true
			}
		case corev1.EnvVar:
			v.envNames[val.Name] = true
		}
	}
	return v
}

func TestElection_NotAModelWorkloadIsIgnored(t *testing.T) {
	el := &fakeElector{role: election.RoleLeader}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	frontend := dynamoWorker()
	frontend.Spec.Containers[0].Command = []string{"python3", "-m", "dynamo.frontend"}
	frontend.Spec.Containers[0].Args = []string{"--router-mode", "kv"}
	frontend.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	patches, err := m.Mutate(context.Background(), frontend)
	if err != nil || len(patches) != 0 {
		t.Fatalf("frontend must be admitted unchanged, got %d patches err=%v", len(patches), err)
	}
	if el.called != 0 {
		t.Error("election must not run for a pod without GPU and model")
	}
}

func TestElection_LeaderGetsCaptureDecoration(t *testing.T) {
	el := &fakeElector{role: election.RoleLeader}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	pod := dynamoWorker()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewPatches(patches)
	if el.called != 1 {
		t.Fatalf("elector called %d times, want 1", el.called)
	}
	if v.annotations[election.RoleAnnotation] != "leader" {
		t.Errorf("role annotation = %q, want leader", v.annotations[election.RoleAnnotation])
	}
	if v.annotations[election.HashAnnotation] != el.hash || len(el.hash) != 64 {
		t.Errorf("hash annotation %q must be the full hash the elector saw %q", v.annotations[election.HashAnnotation], el.hash)
	}
	if v.labels[election.HashLabel] != checkpointstore.ShortHash(el.hash) {
		t.Errorf("hash label = %q, want short hash", v.labels[election.HashLabel])
	}
	if v.labels[CaptureLabel] != "true" {
		t.Error("leader must carry the capture label so the watcher captures it")
	}
	if v.annotations[election.ElectionIDAnnotation] != "id-1" {
		t.Errorf("leader must carry the election id the Lease holds, got %q", v.annotations[election.ElectionIDAnnotation])
	}
	if !v.cacheEmptyDir || !v.envNames["HF_HOME"] {
		t.Errorf("leader must get the capture decoration (cache emptyDir + cache env), got emptyDir=%v env=%v", v.cacheEmptyDir, v.envNames)
	}
	if v.gates != 0 || len(v.claims) != 0 || v.labels[election.GatedLabel] != "" {
		t.Errorf("leader must not be gated or mounted on a claim: gates=%d claims=%v", v.gates, v.claims)
	}
	if v.commandTouched {
		t.Error("leader command must be left as authored")
	}
}

func TestElection_FollowerIsGatedOnThePendingClaim(t *testing.T) {
	el := &fakeElector{role: election.RoleFollower}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	pod := dynamoWorker()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewPatches(patches)
	if v.annotations[election.RoleAnnotation] != "follower" || v.labels[election.GatedLabel] != "true" {
		t.Errorf("follower stamp wrong: role=%q gated=%q", v.annotations[election.RoleAnnotation], v.labels[election.GatedLabel])
	}
	if v.gates != 1 {
		t.Errorf("follower must carry exactly one scheduling gate, got %d", v.gates)
	}
	want := "rox-" + checkpointstore.ShortHash(el.hash)
	if len(v.claims) != 1 || v.claims[0] != want {
		t.Errorf("follower must mount the pending claim %s, got %v", want, v.claims)
	}
	if len(v.inits) < 3 || v.inits[0] != "nvsnap-l2-wait" || v.inits[1] != "nvsnap-seed-cache" || v.inits[2] != "nvsnap-prewarm" {
		t.Errorf("follower inits = %v, want [nvsnap-l2-wait nvsnap-seed-cache nvsnap-prewarm]", v.inits)
	}
	if v.labels[CaptureLabel] != "" {
		t.Error("follower must not be a capture source")
	}
	if !v.envNames["HF_HOME"] {
		t.Error("follower must get the cache env so the engine reads the mounted cache")
	}
	if v.commandTouched {
		t.Error("follower command must be left as authored")
	}
}

func TestElection_FollowerOnPerPodCloneStorageStartsCold(t *testing.T) {
	el := &fakeElector{role: election.RoleFollower}
	m := electionMutator(t, el, &pendingStub{pendingOK: false})
	patches, err := m.Mutate(context.Background(), dynamoWorker())
	if err != nil || len(patches) != 0 {
		t.Fatalf("follower without a nameable claim must be admitted unchanged, got %d patches err=%v", len(patches), err)
	}
}

func TestElection_ErrorAdmitsUnchanged(t *testing.T) {
	el := &fakeElector{err: errors.New("apiserver down")}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	patches, err := m.Mutate(context.Background(), dynamoWorker())
	if err != nil || len(patches) != 0 {
		t.Fatalf("election error must fail open, got %d patches err=%v", len(patches), err)
	}
}

func TestElection_PromotedCaptureRestoresWithoutElecting(t *testing.T) {
	el := &fakeElector{role: election.RoleLeader}
	pod := dynamoWorker()
	hash := electionHash(pod)
	l2 := &pendingStub{pendingOK: true}
	l2.mountResult = checkpointstore.PodMount{
		Volume: corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-" + checkpointstore.ShortHash(hash)}}},
		VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
	}
	m := electionMutator(t, el, l2)
	manifest := checkpointstore.Manifest{CaptureMethod: "cachedir", Volumes: []checkpointstore.VolumeMeta{{Name: "v", MountPath: "/cache", Type: "emptyDir", FileCount: 1, SizeBytes: 1}}}
	if _, err := m.Backend.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcForManifest(t, manifest)}}, manifest); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewPatches(patches)
	if el.called != 0 {
		t.Error("a promoted capture must be restored without running an election")
	}
	if v.annotations[election.RoleAnnotation] != "restore" || v.gates != 0 || len(v.claims) != 1 {
		t.Errorf("restore decoration wrong: role=%q gates=%d claims=%v", v.annotations[election.RoleAnnotation], v.gates, v.claims)
	}
}

func TestElection_CaptureWithoutBoundClaimIsLeftAlone(t *testing.T) {
	el := &fakeElector{role: election.RoleLeader}
	pod := dynamoWorker()
	hash := electionHash(pod)
	l2 := &pendingStub{pendingOK: true}
	l2.mountErr = checkpointstore.ErrNotFound
	m := electionMutator(t, el, l2)
	manifest := checkpointstore.Manifest{CaptureMethod: "cachedir", Volumes: []checkpointstore.VolumeMeta{{Name: "v", MountPath: "/cache", Type: "emptyDir", FileCount: 1, SizeBytes: 1}}}
	if _, err := m.Backend.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcForManifest(t, manifest)}}, manifest); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil || len(patches) != 0 || el.called != 0 {
		t.Fatalf("manifest present but rox unbound: want unchanged and no election, got %d patches err=%v elected=%d", len(patches), err, el.called)
	}
}

func TestElection_ExplicitRestoreFromBypassesElection(t *testing.T) {
	el := &fakeElector{role: election.RoleLeader}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	pod := dynamoWorker()
	pod.Annotations = map[string]string{RestoreFromAnnotation: "deadbeef"}
	_, _ = m.Mutate(context.Background(), pod)
	if el.called != 0 {
		t.Error("an explicit restore-from must take the old path, not the election")
	}
}

// The pod arrives with no labels and no annotations; the maps must be
// created exactly once each, or a second create would wipe earlier keys.
func TestElection_MetadataMapsBootstrappedOnce(t *testing.T) {
	m := electionMutator(t, &fakeElector{role: election.RoleFollower}, &pendingStub{pendingOK: true})
	patches, err := m.Mutate(context.Background(), dynamoWorker())
	if err != nil {
		t.Fatal(err)
	}
	creates := map[string]int{}
	for _, p := range patches {
		if p.Path == "/metadata/labels" || p.Path == "/metadata/annotations" {
			creates[p.Path]++
		}
	}
	if creates["/metadata/labels"] != 1 || creates["/metadata/annotations"] != 1 {
		t.Errorf("map bootstraps = %v, want exactly one each", creates)
	}
}

// nodeSelector collects the node selector entries patches add.
func nodeSelector(patches []PatchOp) map[string]string {
	out := map[string]string{}
	for _, p := range patches {
		if k, ok := strings.CutPrefix(p.Path, "/spec/nodeSelector/"); ok {
			out[strings.ReplaceAll(k, "~1", "/")] = p.Value.(string)
		}
	}
	return out
}

// Every pod the election decorates, leader, follower or restore, is pinned
// to the compute capability its hash was composed for, so a cache never
// reaches a GPU it was not built for.
func TestElection_EveryRolePinnedToTheArchitecture(t *testing.T) {
	want := map[string]string{election.ComputeMajorLabel: "9", election.ComputeMinorLabel: "0"}
	for _, role := range []election.Role{election.RoleLeader, election.RoleFollower} {
		m := electionMutator(t, &fakeElector{role: role}, &pendingStub{pendingOK: true})
		patches, err := m.Mutate(context.Background(), dynamoWorker())
		if err != nil {
			t.Fatal(err)
		}
		if got := nodeSelector(patches); got[election.ComputeMajorLabel] != "9" || got[election.ComputeMinorLabel] != "0" {
			t.Errorf("%s: node selector %v, want %v", role, got, want)
		}
		creates := 0
		for _, p := range patches {
			if p.Path == "/spec/nodeSelector" {
				creates++
			}
		}
		if creates != 1 {
			t.Errorf("%s: a pod without a node selector gets it created once, got %d", role, creates)
		}
	}
	pod := dynamoWorker()
	pod.Spec.NodeSelector = map[string]string{"pool": "gpu"}
	hash := electionHash(pod)
	l2 := &pendingStub{pendingOK: true}
	l2.mountResult = checkpointstore.PodMount{
		Volume: corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "rox-" + checkpointstore.ShortHash(hash)}}},
		VolumeMount: corev1.VolumeMount{Name: "x", MountPath: "/opt/nvsnap"},
	}
	m := electionMutator(t, &fakeElector{role: election.RoleLeader}, l2)
	manifest := checkpointstore.Manifest{CaptureMethod: "cachedir", Volumes: []checkpointstore.VolumeMeta{{Name: "v", MountPath: "/cache", Type: "emptyDir", FileCount: 1, SizeBytes: 1}}}
	if _, err := m.Backend.Put(context.Background(), hash, []checkpointstore.CaptureSource{{SrcPath: srcForManifest(t, manifest)}}, manifest); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodeSelector(patches); got[election.ComputeMajorLabel] != "9" || got[election.ComputeMinorLabel] != "0" {
		t.Errorf("restore: node selector %v, want %v", got, want)
	}
	for _, p := range patches {
		if p.Path == "/spec/nodeSelector" {
			t.Error("an existing node selector is extended, not replaced")
		}
	}
}

// A pod whose GPU architecture cannot be established stays out of the
// election and starts cold, as before the election.
func TestElection_UnestablishedArchitectureStaysOut(t *testing.T) {
	for name, scope := range map[string]fixedArch{
		"not established": {},
		"lookup failed":   {err: errors.New("nodes unreadable")},
	} {
		el := &fakeElector{role: election.RoleLeader}
		m := electionMutator(t, el, &pendingStub{pendingOK: true})
		m.ArchScope = scope
		patches, err := m.Mutate(context.Background(), dynamoWorker())
		if err != nil {
			t.Fatal(err)
		}
		if el.called != 0 || viewPatches(patches).annotations[election.HashAnnotation] != "" {
			t.Errorf("%s: elected=%d stamped=%q, want no election", name, el.called, viewPatches(patches).annotations[election.HashAnnotation])
		}
	}
	el := &fakeElector{role: election.RoleLeader}
	m := electionMutator(t, el, &pendingStub{pendingOK: true})
	m.ArchScope = nil
	if _, err := m.Mutate(context.Background(), dynamoWorker()); err != nil || el.called != 0 {
		t.Errorf("no architecture scope: elected=%d err=%v, want no election", el.called, err)
	}
}

// The same model, image and arguments on two architectures are two
// elections, with two caches.
func TestElection_ArchitectureSeparatesElections(t *testing.T) {
	hashes := map[string]string{}
	for _, cc := range []string{"9.0", "8.0"} {
		el := &fakeElector{role: election.RoleLeader}
		m := electionMutator(t, el, &pendingStub{pendingOK: true})
		m.ArchScope = fixedArch{cc: cc, ok: true}
		if _, err := m.Mutate(context.Background(), dynamoWorker()); err != nil {
			t.Fatal(err)
		}
		hashes[cc] = el.hash
	}
	if hashes["9.0"] == "" || hashes["9.0"] == hashes["8.0"] {
		t.Errorf("hashes %v: each architecture must elect under its own hash", hashes)
	}
}
