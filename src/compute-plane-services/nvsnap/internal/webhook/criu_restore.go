// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// CRIU restore of a scaled-up pod. When a pod's nvsnap.io/restore-from
// resolves to a CRIU capture, the pod becomes its own restore placeholder:
// its main container idles instead of starting the engine, the checkpoints
// directory is mounted, and the agent on its node restores the captured
// process into it once it runs (internal/agent/criu_auto_restore.go). The
// restored process then answers the pod's readiness probe like a fresh
// start would, only sooner.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

const (
	// CRIURestoreAnnotation names the checkpoint the agent restores into
	// this pod; CRIURestoreContainerAnnotation names the container.
	CRIURestoreAnnotation          = "nvsnap.io/criu-restore"
	CRIURestoreContainerAnnotation = "nvsnap.io/criu-restore-container"

	// CRIURestoredCondition gates a restore placeholder's readiness: the
	// agent sets it True once the restore is done. The pod's own readiness
	// probe cannot tell: a process of the restored tree that holds no GPU
	// (a health server) runs, and answers, while the GPU processes still
	// load their memory.
	CRIURestoredCondition corev1.PodConditionType = "nvsnap.io/restored"

	criuCheckpointsVolume = "nvsnap-criu-checkpoints"
	// criuCheckpointsMount is where the restore reads the images
	// (/checkpoints/<id>), the same path the agent's placeholder uses.
	criuCheckpointsMount = "/checkpoints"
)

// criuCheckpointID returns the CRIU checkpoint a capture record points to,
// or "" when the record is not a CRIU capture.
func criuCheckpointID(man checkpointstore.Manifest) string { return CRIUCheckpointID(man) }

// CRIUCheckpointID returns the CRIU checkpoint a capture record points to,
// or "" when the record is not a CRIU capture.
func CRIUCheckpointID(man checkpointstore.Manifest) string {
	if man.CaptureMethod != "criu" && man.SourcePodMeta["engine"] != "criu" {
		return ""
	}
	return man.SourcePodMeta["checkpoint_id"]
}

// criuRestoreFor looks up the capture record for hash on the L2 backend
// (where CRIU captures are recorded) and returns its checkpoint id when the
// pod should be restored with CRIU.
func (m *Mutator) criuRestoreFor(ctx context.Context, pod *corev1.Pod, hash string, log logrus.FieldLogger) (string, checkpointstore.Manifest) {
	var man checkpointstore.Manifest
	var id string
	if name := NamesOwnInstance(pod); name != "" {
		log.WithField("env", name).Info("CRIU restore: the pod names its own instance in its configuration; it starts from the caches")
		return "", man
	}
	for _, b := range []checkpointstore.Backend{m.Backend, m.L2Backend} {
		if b == nil {
			continue
		}
		got, err := b.Stat(ctx, hash)
		if err != nil {
			continue
		}
		if id = criuCheckpointID(got); id != "" {
			man = got
			break
		}
	}
	if id == "" {
		return "", man
	}
	if why := m.criuScopeMismatch(ctx, pod, man); why != "" {
		log.WithField("checkpoint", id).Warn("CRIU restore: " + why + "; cold start")
		return "", checkpointstore.Manifest{}
	}
	if m.CRIURestoreBlocked != nil && m.CRIURestoreBlocked(ctx, id) {
		log.WithField("checkpoint", id).Warn("CRIU restore: this checkpoint failed to restore before; cold start")
		return "", man
	}
	return id, man
}

// criuScopeMismatch returns why pod may not restore the CRIU capture man,
// or "" when it may. A checkpoint holds the captured process's memory
// (tokens, keys, request data), and the restore-from annotation is the
// pod author's to write, so the capture must come from the pod's own
// tenant scope, recorded at capture time. A rank of an instance capture
// restores only with its instance, through the group record.
func (m *Mutator) criuScopeMismatch(ctx context.Context, pod *corev1.Pod, man checkpointstore.Manifest) string {
	if pod.Namespace == "" {
		return "the pod's namespace is unknown"
	}
	scope := CRIUNamespaceScope(pod.Namespace)
	if m.CRIUScope != nil {
		var err error
		if scope, err = m.CRIUScope(ctx, pod.Namespace); err != nil {
			return fmt.Sprintf("cannot read the namespace's tenant scope: %v", err)
		}
	}
	return CRIUScopeMismatch(man, scope)
}

// CRIUScopeMismatch returns why a pod of tenant scope scope may not
// restore on its own from the CRIU capture man, or "" when it may: the
// capture must record that same scope, and not be one rank of an instance
// capture, which restores only with its instance. The webhook applies it
// at admission, and the agent again before it fetches or restores, since
// a pod's restore annotations are its author's to write.
func CRIUScopeMismatch(man checkpointstore.Manifest, scope string) string {
	if man.SourcePodMeta[checkpointstore.InstanceCaptureMetaKey] == "true" {
		return "the checkpoint is one rank of an instance capture"
	}
	captured := man.SourcePodMeta[checkpointstore.CRIUScopeMetaKey]
	if captured == "" {
		return "the capture records no tenant scope"
	}
	if scope != captured {
		return "the capture belongs to another tenant scope"
	}
	return ""
}

// CRIUNamespaceScope is the tenant scope of a namespace the platform has
// not labelled with a function version: the namespace itself.
func CRIUNamespaceScope(ns string) string { return "namespace/" + ns }

// criuRestorePatches turns the pod into the restore placeholder for
// checkpointID. The liveness and startup probes go: they would kill the
// container before the restored process answers them. The readiness probe
// stays, so the pod becomes Ready when the restored engine serves, and
// not before the restore is done (CRIURestoredCondition).
func (m *Mutator) criuRestorePatches(pod *corev1.Pod, checkpointID string, man checkpointstore.Manifest) ([]PatchOp, error) {
	// The capture checkpoints the first container with GPUs, or the
	// container the record names (a companion's, captured by its
	// connections to the ranks); that one becomes the placeholder,
	// wherever the chart put it. The pod's other containers run as they
	// are.
	i := gpuContainerIndex(pod, m.MainContainer)
	if name := man.SourcePodMeta[restoreContainerMeta]; name != "" {
		i = containerIndex(pod, name)
		if i < 0 {
			return nil, fmt.Errorf("the checkpoint was taken from container %q, which this pod does not have", name)
		}
	}
	if i < 0 || i >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)", i, len(pod.Spec.Containers))
	}
	if m.CheckpointHostRoot == "" {
		return nil, fmt.Errorf("CRIU restore: the checkpoint host root is not configured")
	}
	c := &pod.Spec.Containers[i]
	base := fmt.Sprintf("/spec/containers/%d", i)
	setOp := func(present bool) string {
		if present {
			return "replace"
		}
		return "add"
	}
	// The container idles until the restored process takes over: the
	// helper (helper.go) runs a short child every two seconds, so once the
	// agent raises the namespace's next pid the reserved range shows, and
	// it reaps what the restore reparents to it.
	patches := []PatchOp{
		{Op: setOp(c.Command != nil), Path: base + "/command", Value: []string{IdleBin}},
	}
	if c.Args != nil {
		patches = append(patches, PatchOp{Op: "remove", Path: base + "/args"})
	}
	patches = append(patches, placeholderInitPatches(pod, c)...)
	if c.LivenessProbe != nil {
		patches = append(patches, PatchOp{Op: "remove", Path: base + "/livenessProbe"})
	}
	if c.StartupProbe != nil {
		patches = append(patches, PatchOp{Op: "remove", Path: base + "/startupProbe"})
	}
	// Only this checkpoint's directory: the node's other checkpoints hold
	// other workloads' memory. It need not exist yet: on a node without
	// the checkpoint the pod starts, and the agent fetches the checkpoint
	// into the directory before it restores.
	dir, orCreate := corev1.HostPathDirectory, corev1.HostPathDirectoryOrCreate
	vols := []corev1.Volume{{
		Name:         criuCheckpointsVolume,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: m.CheckpointHostRoot + "/" + checkpointID, Type: &orCreate}},
	}}
	mounts := []corev1.VolumeMount{
		// Writable: CRIU writes restore.log into the images directory.
		{Name: criuCheckpointsVolume, MountPath: criuCheckpointsMount + "/" + checkpointID},
		helperMount(),
	}
	if man.SourcePodMeta["gpushare"] == "true" {
		// The restored processes re-map the library from where it was and
		// load their GPU memory from the checkpoint's chunk store, at the
		// store path they saved it from.
		bundleRoot := m.HostBundleRoot
		if bundleRoot == "" {
			bundleRoot = DefaultHostBundleRoot
		}
		vols = append(vols, corev1.Volume{Name: gpushareStoreVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: m.CheckpointHostRoot + "/" + checkpointID + "/gpushare", Type: &orCreate}}})
		mounts = append(mounts, corev1.VolumeMount{Name: gpushareStoreVolume, MountPath: GPUShareStorePath})
		libMounted := false
		for _, vm := range c.VolumeMounts {
			libMounted = libMounted || vm.MountPath == nvsnapToolsMountPath
		}
		if !libMounted {
			vols = append(vols, corev1.Volume{Name: gpushareLibVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: m.bundleDir(restoreBundle(man, m.BundleID)), Type: &dir}}})
			mounts = append(mounts, corev1.VolumeMount{Name: gpushareLibVolume, MountPath: nvsnapToolsMountPath, ReadOnly: true})
		}
	}
	patches = append(patches, addVolumes(pod, vols)...)
	patches = m.withHelperVolume(pod, patches)
	patches = append(patches, addContainerMounts(i, c, mounts)...)
	if pod.Annotations == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/metadata/annotations", Value: map[string]string{}})
	}
	patches = append(patches,
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIURestoreAnnotation), Value: checkpointID},
		PatchOp{Op: "add", Path: "/metadata/annotations/" + jsonPointerEscape(CRIURestoreContainerAnnotation), Value: c.Name},
	)
	patches = append(patches, criuNodePreference(pod, man.CapturedOnNodes)...)
	patches = append(patches, restoredGatePatches(pod)...)
	return patches, nil
}

// restoredGatePatches adds the CRIURestoredCondition readiness gate.
func restoredGatePatches(pod *corev1.Pod) []PatchOp {
	gate := corev1.PodReadinessGate{ConditionType: CRIURestoredCondition}
	if HasRestoredGate(pod) {
		return nil
	}
	if len(pod.Spec.ReadinessGates) == 0 {
		return []PatchOp{{Op: "add", Path: "/spec/readinessGates", Value: []corev1.PodReadinessGate{gate}}}
	}
	return []PatchOp{{Op: "add", Path: "/spec/readinessGates/-", Value: gate}}
}

// HasRestoredGate reports whether pod's readiness waits on
// CRIURestoredCondition.
func HasRestoredGate(pod *corev1.Pod) bool {
	for _, g := range pod.Spec.ReadinessGates {
		if g.ConditionType == CRIURestoredCondition {
			return true
		}
	}
	return false
}

// gpuContainerIndex is the index of the first container with GPUs, else
// fallback.
// restoreContainerMeta is the manifest key naming the container a
// checkpoint was taken from.
const restoreContainerMeta = "restore_container"

// containerIndex returns the index of the container named name, or -1.
func containerIndex(pod *corev1.Pod, name string) int {
	for i, c := range pod.Spec.Containers {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func gpuContainerIndex(pod *corev1.Pod, fallback int) int {
	for i, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
			return i
		}
	}
	return fallback
}

// criuNodePreference prefers the nodes that already hold the checkpoint.
// Preferred, not required: on any other node the agent fetches it first,
// and a full capture node must not leave the pod unschedulable.
func criuNodePreference(pod *corev1.Pod, nodes []string) []PatchOp {
	if len(nodes) == 0 {
		return nil
	}
	term := corev1.PreferredSchedulingTerm{
		Weight: 100,
		Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: nodes,
		}}},
	}
	switch {
	case pod.Spec.Affinity == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity", Value: corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{term}}}}}
	case pod.Spec.Affinity.NodeAffinity == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity", Value: corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{term}}}}
	case pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity/preferredDuringSchedulingIgnoredDuringExecution",
			Value: []corev1.PreferredSchedulingTerm{term}}}
	default:
		return []PatchOp{{Op: "add", Path: "/spec/affinity/nodeAffinity/preferredDuringSchedulingIgnoredDuringExecution/-", Value: term}}
	}
}

// jsonPointerEscape escapes a key for a JSON Patch path (RFC 6901).
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// placeholderInitPatches skips, in a restore placeholder, the init
// containers whose output the restored process cannot see: the process
// already went through its startup when it was captured, so what an init
// container leaves matters only on a volume the main container mounts,
// and one that writes none only waits or checks. Waiting is harmful here:
// a rank that waits for its leader to serve (a Dynamo worker's
// wait-for-leader) never starts, and the leader is a placeholder until the
// whole group is restored. A skipped init runs the helper's skip instead,
// keeping its place so the other patches' indices hold.
//
// A native sidecar (an init container with restartPolicy Always) is no
// startup step: it serves beside the main container for the pod's life,
// like any other sidecar, and runs unchanged. Skipped, it would exit, be
// restarted, and hold back every init container after it.
func placeholderInitPatches(pod *corev1.Pod, main *corev1.Container) []PatchOp {
	volumes := map[string]corev1.Volume{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = v
	}
	seen := map[string]bool{}
	for _, vm := range main.VolumeMounts {
		seen[vm.Name] = true
	}
	var patches []PatchOp
	for i := range pod.Spec.InitContainers {
		init := &pod.Spec.InitContainers[i]
		if NativeSidecar(init) || writesSharedVolume(init, volumes, seen) {
			continue
		}
		base := fmt.Sprintf("/spec/initContainers/%d", i)
		op := "add"
		if init.Command != nil {
			op = "replace"
		}
		patches = append(patches, PatchOp{Op: op, Path: base + "/command",
			Value: []string{HelperBin, "skip", "--reason", "restore placeholder: " + init.Name + " writes nothing the restored process reads"}})
		if init.Args != nil {
			patches = append(patches, PatchOp{Op: "remove", Path: base + "/args"})
		}
		if init.VolumeMounts == nil {
			patches = append(patches, PatchOp{Op: "add", Path: base + "/volumeMounts", Value: []corev1.VolumeMount{helperMount()}})
		} else {
			patches = append(patches, PatchOp{Op: "add", Path: base + "/volumeMounts/-", Value: helperMount()})
		}
	}
	return patches
}

// writesSharedVolume reports whether init mounts, writable, a volume that
// holds data (not a ConfigMap, Secret or projection) the main container
// also mounts.
func writesSharedVolume(init *corev1.Container, volumes map[string]corev1.Volume, mainMounts map[string]bool) bool {
	for _, vm := range init.VolumeMounts {
		if vm.ReadOnly || !mainMounts[vm.Name] {
			continue
		}
		v, ok := volumes[vm.Name]
		if !ok {
			return true // not knowable here: keep the init
		}
		s := v.VolumeSource
		if s.EmptyDir != nil || s.PersistentVolumeClaim != nil || s.HostPath != nil || s.Ephemeral != nil || s.CSI != nil || s.NFS != nil {
			return true
		}
	}
	return false
}

// NativeSidecar reports an init container that runs for the pod's life.
func NativeSidecar(c *corev1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

const (
	// PIDReserveInit is the first init container of a restore placeholder
	// whose containers share one pid namespace: it waits until the agent
	// has raised the namespace's next pid to PIDReserveFloor, so every
	// process the pod starts after it (a native sidecar, a sidecar, the
	// placeholder) gets a pid above the range the restore recreates.
	// Without it a sidecar started before the placeholder can hold a pid
	// of the dumped tree, and the restore cannot recreate it.
	PIDReserveInit = "nvsnap-reserve-pids"
	// PIDReserveFloor is the next pid the agent raises a placeholder's pid
	// namespace to.
	PIDReserveFloor = 100000
)

var pidReserveResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
}

// withPIDReserve appends to a placeholder's patches the PIDReserveInit
// step, first among its init containers, when its containers share one pid
// namespace. It goes last: other patches address init containers by
// index or insert at the front.
func withPIDReserve(pod *corev1.Pod, patches []PatchOp) []PatchOp {
	if pod.Spec.ShareProcessNamespace == nil || !*pod.Spec.ShareProcessNamespace || len(pod.Spec.Containers) == 0 {
		return patches
	}
	main := &pod.Spec.Containers[0]
	c := corev1.Container{
		Name:         PIDReserveInit,
		Image:        main.Image,
		Command:      []string{HelperBin, "await-pid-floor", "--floor", strconv.Itoa(PIDReserveFloor)},
		VolumeMounts: []corev1.VolumeMount{helperMount()},
	}
	harden(&c, pidReserveResources, inheritPosture, main)
	listExists := len(pod.Spec.InitContainers) > 0
	for _, p := range patches {
		listExists = listExists || (p.Op == "add" && p.Path == "/spec/initContainers")
	}
	if !listExists {
		return append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []corev1.Container{c}})
	}
	return append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/0", Value: c})
}
