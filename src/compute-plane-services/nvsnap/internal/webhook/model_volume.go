// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelid"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// Model volume decoration (docs/proposals/helm-shared-model-volume.md).
// For a pod that will download a model, the webhook turns the download
// into a write-once step: one download Job per identity fills the primary
// volume, every pod of that identity is a reader that references a
// per-namespace view of the primary and waits for the completion marker.
// No pod is gated; waiting is an init container.
//
// The flow is the same on every storage. The storage profile's Mode
// decides only how the Job reaches the primary (modelvolume.Config): on
// block storage it stages into an emptyDir the agent copies out, and the
// readers' views can only be minted once the primary is complete; on a
// filesystem that is shared while written the Job writes through a
// read-write view in its namespace and readers mount their read-only view
// at admission. Block storage additionally offers a hostPath landing for
// gang-scheduled charts (profile modelVolume.readerMode).
const (
	modelVolumeName      = "nvsnap-model"
	injectedDownloadInit = "nvsnap-model-download"
	waitScriptDeadline   = 3600 // seconds, when no deadline is configured
)

// modelVolumePatches is the Helm-function decision. (nil, nil) means the
// pod is not a downloader or the feature is off, and Mutate continues
// with the older paths.
func (m *Mutator) modelVolumePatches(ctx context.Context, pod *corev1.Pod) ([]PatchOp, error) {
	if m.ModelVolume == nil {
		return nil, nil
	}
	if gpuRequest(pod) == 0 {
		return nil, nil
	}
	res, ok, err := modelid.ResolveWithGroup(ctx, pod, m.MainContainer, m.Groups)
	if err != nil {
		return nil, fmt.Errorf("resolve model identity: %w", err)
	}
	if !ok || res.Identity.Scheme == "path" {
		return nil, nil
	}
	uri := res.Identity.URI()
	log := m.logger().WithFields(logrus.Fields{"pod": election.PodIdentity(pod), "model": uri, "source": res.Source})
	if !res.Landing.Substitutable() {
		log.WithField("kind", res.Landing.Kind).Info("model volume: landing volume is the customer's; leaving pod alone")
		return nil, nil
	}
	main := &pod.Spec.Containers[m.MainContainer]
	land := res.Landing
	mp := newMetaPatcher(pod)
	patches := mp.annotation(modelvolume.IdentityAnnotation, uri)
	patches = append(patches, mp.label(modelvolume.IdentityLabel, modelvolume.Key(uri))...)

	st, err := m.ModelVolume.Lookup(ctx, uri)
	if err != nil {
		return nil, err
	}
	if st.Failed {
		// The last copy gave up recently. A reader referencing a claim
		// that never comes would hang, so the pod keeps its own download;
		// the record expires and a later deployment tries again.
		log.Info("model volume: recent failure recorded for this model; leaving pod on its own download")
		return nil, nil
	}
	step, derivable := m.downloadStep(pod, main, land, res.Identity)
	// The engine's own script fetches an artifact nvsnap has no download
	// recipe for (an NGC model pulled in the main container) into a pod
	// volume. Pre-filling that volume would miss the script's markers and
	// layout, so the first pod keeps downloading as the chart intends and
	// the agent on its node captures the finished tree once the pod is
	// Ready. Later pods reference the read-only copy; the script's own
	// "already present" check passes because the tree is exactly what it
	// wrote. PVC readers only: the volume must be in place before the
	// engine starts, which a hostPath bind cannot promise.
	captureSource := !derivable && land.Downloader == modelid.DownloaderEngine && land.VolumeName != "" &&
		m.ModelVolume.Cfg.ReaderMode() == modelvolume.ReaderPVC
	if !st.Complete {
		if captureSource {
			patches = append(patches, mp.label(modelvolume.CaptureLabel, "true")...)
			patches = append(patches, mp.annotation(modelvolume.CaptureVolumeAnnotation, land.VolumeName)...)
			// The compile caches are collected from this pod like from any
			// reader: without them the first warm deployment profiles and
			// compiles again (kimi-k3 on GB300, 2026-10-03: 4 min 38 s of
			// profiling the cold pods had already done).
			patches = append(patches, m.modelCacheEnvPatches(ctx, pod, main, land, uri)...)
			log.WithField("volume", land.VolumeName).Info("model volume: engine downloads a non-derivable artifact; pod keeps its own download and is captured after Ready")
			return patches, nil
		}
		if !derivable {
			log.Info("model volume: no download step can be derived (engine downloads a non-HF model); leaving pod alone")
			return nil, nil
		}
		inFlight, err := m.ensureDownload(ctx, pod, uri, step, log)
		if err != nil {
			return nil, err
		}
		st.InFlightPV = inFlight
	}
	patches = append(patches, mp.label(modelvolume.RoleLabel, "reader")...)
	landing, err := m.readerLanding(ctx, pod, main, land, uri, st, log)
	if err != nil {
		return nil, err
	}
	patches = append(patches, landing...)
	if !captureSource {
		// A captured tree needs no wait init and no offline switch: the
		// chart's own script finds its markers on the read-only volume.
		patches = append(patches, m.downloadStepPatches(pod, main, land, res.Identity, false)...)
	}
	if st.Complete && m.ModelVolume.Cfg.ReaderMode() != modelvolume.ReaderHostPath {
		switch limit := m.prewarmLimit(); {
		case st.PrimaryBytes > limit:
			// Larger than the node can keep in page cache: the sweep would
			// evict its own pages and only cost time (1.56 TB on 902 GiB,
			// GB300 2026-10-01). The engine reads the volume directly.
			log.WithFields(logrus.Fields{"bytes": st.PrimaryBytes, "prewarm_max_bytes": limit}).Info("model volume: too large for a page-cache sweep; no prewarm")
		case parallelLoader(main) != "":
			// The engine already reads in parallel (fastsafetensors pulled
			// 1.56 TB at 5.7 GB/s aggregate on GB300 with no sweep); the
			// sweep only helps the default single-thread loader.
			log.WithField("loader", parallelLoader(main)).Info("model volume: engine uses a parallel weight loader; no prewarm")
		default:
			patches = m.modelPrewarmPatches(pod, main, land, patches)
		}
	}
	patches = append(patches, m.modelCacheEnvPatches(ctx, pod, main, land, uri)...)
	return patches, nil
}

// ensureDownload creates the one download Job for uri. The Job runs in the
// pod's namespace because that is where the chart's registry credentials
// are. Where it writes is the one place the storage mode shows: on storage
// that is shared while written the primary claim is created now in the
// nvsnap namespace and the Job writes through a read-write view in its
// own namespace; on block storage the Job stages into a pod-local emptyDir
// and the agent on that node copies the tree into a claim sized from what
// landed, nothing being guessed at admission. Returns the primary PV when
// it is already bound, so readers admitted now can view it.
func (m *Mutator) ensureDownload(ctx context.Context, pod *corev1.Pod, uri string, step modelvolume.DownloadStep, log logrus.FieldLogger) (string, error) {
	cfg := m.ModelVolume.Cfg
	if !cfg.SharedWhileWriting() {
		job, err := m.ModelVolume.EnsureDownloadJob(ctx, uri, pod.Namespace, "", step)
		if err != nil {
			return "", err
		}
		log.WithFields(logrus.Fields{"job": job, "staging": true}).Info("model volume: download job ensured")
		return "", nil
	}
	if m.ViewMinter == nil {
		return "", fmt.Errorf("model volume: no view minter for the shared filesystem")
	}
	sysNS := cfg.SystemNamespace()
	claim, err := m.ModelVolume.EnsureWriterClaim(ctx, uri, sysNS)
	if err != nil {
		return "", err
	}
	// The Job writes through the writer view in the pod's namespace. The
	// view can only be minted once the primary is bound, and binding is
	// not gated on here: a re-used filesystem binds in about a second, a
	// brand-new one can take the better part of a minute (OCI FSS,
	// 2026-10-02), and admission has a 5 s budget. The Job is created
	// either way, referencing the view by name; while the view does not
	// exist the Job's pod pends on volume binding and the agents mint the
	// view as soon as the primary is bound (writer role on the Job's pod).
	view := cfg.WriterViewClaimName(uri)
	pv, err := m.ModelVolume.WaitBound(ctx, sysNS, claim, primaryBindWait)
	if err == nil {
		if err := m.ViewMinter.MintViewFromPVLabels(ctx, pv, cfg.WriterViewPVName(uri, pod.Namespace), view, pod.Namespace, cfg.ReadOnlyLabels(uri), false); err != nil {
			return "", fmt.Errorf("mint writer view: %w", err)
		}
	} else {
		pv = ""
		log.WithField("primary", sysNS+"/"+claim).Info("model volume: primary not bound yet; the agents mint the views once it is")
	}
	job, err := m.ModelVolume.EnsureDownloadJob(ctx, uri, pod.Namespace, view, step)
	if err != nil {
		return "", err
	}
	log.WithFields(logrus.Fields{"job": job, "primary": sysNS + "/" + claim, "pv": pv, "view": view}).Info("model volume: download job ensured")
	return pv, nil
}

// primaryBindWait bounds the admission-time wait for a shared-filesystem
// primary claim to bind; past it the agents take over.
const primaryBindWait = 3 * time.Second

// readerLanding gives the reader its model volume. PVC readers reference
// the read-only view of the primary in their namespace: minted now when
// the primary is complete, or already while the download runs when the
// storage is shared while written; otherwise the pod is marked pending and
// stays on volume binding until the agent mints the view after
// completion. hostPath readers (block storage, gang schedulers) land on a
// hostPath the agent binds the completed volume into.
func (m *Mutator) readerLanding(ctx context.Context, pod *corev1.Pod, main *corev1.Container, land modelid.Landing, uri string, st modelvolume.State, log logrus.FieldLogger) ([]PatchOp, error) {
	mp := newMetaPatcher(pod)
	cfg := m.ModelVolume.Cfg
	if cfg.ReaderMode() == modelvolume.ReaderHostPath {
		patches := mp.label(modelvolume.PendingLabel, "true")
		patches = append(patches, mp.annotation(modelvolume.LandingAnnotation, landingMount(land))...)
		patches = append(patches, m.hostPathLanding(pod, main, land, uri)...)
		log.WithField("complete", st.Complete).Info("model volume: reader on a hostPath landing; agent binds the volume after completion")
		return patches, nil
	}
	viewable := st.PrimaryPV
	if viewable == "" && cfg.SharedWhileWriting() {
		viewable = st.InFlightPV
	}
	var patches []PatchOp
	switch {
	case viewable != "" && m.ViewMinter != nil:
		if err := m.ViewMinter.MintReadOnlyFromPV(ctx, viewable, cfg.ReadOnlyPVName(uri, pod.Namespace), cfg.ReadOnlyClaimName(uri), pod.Namespace, modelvolume.Key(uri)); err != nil {
			return nil, fmt.Errorf("mint read-only view: %w", err)
		}
		if st.Complete {
			if err := m.ModelVolume.TouchLastUsed(ctx, st.PrimaryPV); err != nil {
				log.WithError(err).Warn("model volume: record last use failed")
			}
		}
	default:
		patches = append(patches, mp.label(modelvolume.PendingLabel, "true")...)
	}
	patches = append(patches, m.substituteLandingVolume(pod, main, land, cfg.ReadOnlyClaimName(uri))...)
	log.WithFields(logrus.Fields{"complete": st.Complete, "view_of": viewable}).Info("model volume: reader references the read-only view")
	return patches, nil
}

// modelPrewarmName is the init container that sweeps a complete model
// volume into the node's page cache before the engine starts.
const modelPrewarmName = "nvsnap-model-prewarm"

// parallelLoader names the parallel weight loader the engine's arguments
// select, or "" for the default single-thread safetensors path. Checked on
// args and command alike, since charts put the engine line in either.
func parallelLoader(main *corev1.Container) string {
	words := append(append([]string{}, main.Command...), main.Args...)
	for i, w := range words {
		for _, part := range strings.Fields(w) {
			if part == "--load-format" && i+1 < len(words) {
				if l := strings.TrimSpace(words[i+1]); isParallelLoader(l) {
					return l
				}
			}
			if v, ok := strings.CutPrefix(part, "--load-format="); ok && isParallelLoader(v) {
				return v
			}
		}
	}
	for _, e := range main.Env {
		if e.Name == "VLLM_LOAD_FORMAT" && isParallelLoader(e.Value) {
			return e.Value
		}
	}
	return ""
}

func isParallelLoader(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "fastsafetensors", "runai_streamer", "runai_streamer_sharded", "tensorizer":
		return true
	}
	return false
}

// modelPrewarmPatches gives a reader of a complete model volume the
// page-cache sweep (reader_steps.go). The engine's safetensors loader walks
// the shards from one thread, and over a network block device that single
// stream is latency-bound: measured on GB300 with NVMesh (2026-10-01),
// 33 GB took 40 to 45 s cold and 9 s once the node's page cache held it.
// The step runs as the engine does and never fails the pod. patches are
// the admission's patches so far; the result extends them.
func (m *Mutator) modelPrewarmPatches(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, patches []PatchOp) []PatchOp {
	name := land.VolumeName
	if name == "" {
		name = modelVolumeName
	}
	sweep, ok := m.sweepStep(modelPrewarmName, main, volumeAt{Volume: name, Path: landingMount(land)}, inheritPosture)
	if !ok {
		return patches
	}
	return appendInits(pod, patches, sweep)
}

// downloadStep derives the Job's container from the pod: the chart's own
// download init wrapped to touch the marker, or `hf download` on the
// engine image when the engine fetches the model itself.
func (m *Mutator) downloadStep(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, id modelid.Identity) (modelvolume.DownloadStep, bool) {
	step := modelvolume.DownloadStep{
		ImagePullSecrets: pod.Spec.ImagePullSecrets, Tolerations: pod.Spec.Tolerations, NodeSelector: pod.Spec.NodeSelector,
		PodSecurityContext: pod.Spec.SecurityContext, MainSecurityContext: main.SecurityContext,
	}
	if land.Downloader == modelid.DownloaderInit {
		for i := range pod.Spec.InitContainers {
			init := pod.Spec.InitContainers[i]
			if init.Name != land.InitContainer {
				continue
			}
			mount := initMountFor(&init, land)
			orig := shellJoin(append(append([]string{}, init.Command...), init.Args...))
			c := *init.DeepCopy()
			c.Command = []string{"/bin/sh", "-c"}
			c.Args = []string{writerScript(orig, path.Join(mount, modelvolume.MarkerFile))}
			// Keep every mount the init had; the landing one is redirected
			// to the claim by name, the rest (secrets, scripts) come along.
			step.VolumeName = landVolumeName(land)
			if land.VolumeName == "" {
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: step.VolumeName, MountPath: mount})
			}
			mounted := map[string]bool{}
			for _, vm := range c.VolumeMounts {
				mounted[vm.Name] = true
			}
			for i := range pod.Spec.Volumes {
				v := &pod.Spec.Volumes[i]
				if mounted[v.Name] && v.Name != step.VolumeName {
					step.Volumes = append(step.Volumes, *v.DeepCopy())
				}
			}
			step.Container = c
			return step, true
		}
		return step, false
	}
	if id.Scheme != "hf" {
		return step, false
	}
	step.VolumeName = landVolumeName(land)
	step.Container = corev1.Container{
		Image:        main.Image,
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{writerScript(hfDownloadCommand(id), path.Join(downloadMount, modelvolume.MarkerFile))},
		Env:          downloadEnv(main),
		VolumeMounts: []corev1.VolumeMount{{Name: step.VolumeName, MountPath: downloadMount}},
	}
	return step, true
}

// downloadMount is where the injected Hugging Face download, Job or init,
// mounts the landing volume. It is deliberately not the engine's own path:
// the default landing is /root/.cache/huggingface, and the download runs
// with the engine's user posture, so a non-root engine cannot even
// traverse /root (dev1, 2026-10-01: PermissionError on
// /root/.cache/huggingface/token for a public model). The volume holds the
// same bytes whatever path it is mounted at; the engine keeps its own.
const downloadMount = "/nvsnap-model"

// downloadEnv points the Hugging Face tooling at the mounted landing for
// both its cache and its home, so nothing it reads or writes lies under a
// directory the download user may not own, and copies the token and hub
// settings the engine was given.
func downloadEnv(main *corev1.Container) []corev1.EnvVar {
	return append([]corev1.EnvVar{{Name: "HF_HOME", Value: downloadMount}, {Name: "HOME", Value: downloadMount}}, tokenEnv(main)...)
}

func landVolumeName(land modelid.Landing) string {
	if land.VolumeName != "" {
		return land.VolumeName
	}
	return modelVolumeName
}

func gpuRequest(pod *corev1.Pod) int64 {
	var n int64
	for i := range pod.Spec.Containers {
		if q, ok := pod.Spec.Containers[i].Resources.Limits["nvidia.com/gpu"]; ok {
			n += q.Value()
		}
	}
	return n
}

// landingMount is the mount path the model volume occupies: the existing
// volume's mount when there is one, else the landing path itself.
func landingMount(l modelid.Landing) string {
	if l.MountPath != "" {
		return l.MountPath
	}
	return l.Path
}

// substituteLandingVolume replaces the emptyDir under the landing path
// with the claim, or adds the claim as a new volume mounted at the landing
// path when the download wrote into the container filesystem. The main
// container's mount becomes read-only: the model is immutable once
// downloaded, and nothing in the engine may dirty the shared copy.
func (m *Mutator) substituteLandingVolume(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, claim string) []PatchOp {
	src := corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}
	if land.VolumeName != "" {
		for i := range pod.Spec.Volumes {
			if pod.Spec.Volumes[i].Name != land.VolumeName {
				continue
			}
			patches := []PatchOp{{Op: "replace", Path: fmt.Sprintf("/spec/volumes/%d", i), Value: corev1.Volume{Name: land.VolumeName, VolumeSource: src}}}
			for j := range main.VolumeMounts {
				if main.VolumeMounts[j].Name == land.VolumeName {
					patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/%d/readOnly", m.MainContainer, j), Value: true})
				}
			}
			return patches
		}
	}
	patches := []PatchOp{}
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{}})
	}
	if main.VolumeMounts == nil {
		patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer), Value: []any{}})
	}
	patches = append(patches,
		PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: modelVolumeName, VolumeSource: src}},
		PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer), Value: corev1.VolumeMount{Name: modelVolumeName, MountPath: land.Path, ReadOnly: true}},
	)
	return patches
}

// hostPathLanding gives a Block-mode reader a hostPath at the landing path
// under the agent's model host root (the Bidirectional overlays root, so
// agent mounts reach kubelet). The agent bind-mounts the completed
// read-only volume there once it exists; HostToContainer propagation lets
// the already-running wait init and the engine see it appear. The pod
// schedules immediately: a hostPath never blocks volume binding.
func (m *Mutator) hostPathLanding(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, uri string) []PatchOp {
	root := m.ModelHostRoot
	if root == "" {
		root = "/var/lib/containerd/nvsnap-models"
	}
	hp := corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path.Join(root, modelvolume.Key(uri)), Type: hostPathType(corev1.HostPathDirectoryOrCreate)}}
	prop := corev1.MountPropagationHostToContainer
	if land.VolumeName != "" {
		var patches []PatchOp
		for i := range pod.Spec.Volumes {
			if pod.Spec.Volumes[i].Name == land.VolumeName {
				patches = append(patches, PatchOp{Op: "replace", Path: fmt.Sprintf("/spec/volumes/%d", i), Value: corev1.Volume{Name: land.VolumeName, VolumeSource: hp}})
			}
		}
		for j := range main.VolumeMounts {
			if main.VolumeMounts[j].Name == land.VolumeName {
				patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/%d/mountPropagation", m.MainContainer, j), Value: prop})
			}
		}
		for i := range pod.Spec.InitContainers {
			for j := range pod.Spec.InitContainers[i].VolumeMounts {
				if pod.Spec.InitContainers[i].VolumeMounts[j].Name == land.VolumeName {
					patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/initContainers/%d/volumeMounts/%d/mountPropagation", i, j), Value: prop})
				}
			}
		}
		return patches
	}
	patches := []PatchOp{}
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{}})
	}
	if main.VolumeMounts == nil {
		patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer), Value: []any{}})
	}
	return append(patches,
		PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: modelVolumeName, VolumeSource: hp}},
		PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer), Value: corev1.VolumeMount{Name: modelVolumeName, MountPath: land.Path, MountPropagation: &prop}},
	)
}

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }

// downloadStepPatches turns the download into a write-once step. With an
// init container: the writer's init is wrapped to touch the marker on
// success, a reader's init to wait for the marker and skip. Without one
// (the engine downloads): an init is injected that runs the download for
// the writer and waits for readers, and the engine is started offline so
// it reads the volume instead of the network.
func (m *Mutator) downloadStepPatches(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, id modelid.Identity, writer bool) []PatchOp {
	marker := path.Join(landingMount(land), modelvolume.MarkerFile)
	deadline := m.waitDeadlineSeconds()
	if land.Downloader == modelid.DownloaderInit {
		for i := range pod.Spec.InitContainers {
			init := &pod.Spec.InitContainers[i]
			if init.Name != land.InitContainer {
				continue
			}
			imarker := path.Join(initMountFor(init, land), modelvolume.MarkerFile)
			orig := shellJoin(append(append([]string{}, init.Command...), init.Args...))
			script := writerScript(orig, imarker)
			if !writer {
				script = readerScript(orig, imarker, deadline)
			}
			return []PatchOp{
				{Op: "replace", Path: fmt.Sprintf("/spec/initContainers/%d/command", i), Value: []string{"/bin/sh", "-c"}},
				{Op: "replace", Path: fmt.Sprintf("/spec/initContainers/%d/args", i), Value: []string{script}},
			}
		}
		return nil
	}
	// Engine downloads itself. Only Hugging Face repos can be fetched by
	// an injected init; NIM and others complete when the engine is Ready
	// (the agent marks them), and readers still wait for the marker.
	var patches []PatchOp
	if pod.Spec.InitContainers == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{}})
	}
	download := ""
	if id.Scheme == "hf" {
		download = hfDownloadCommand(id)
	}
	// The injected init sees the landing at downloadMount (see there); the
	// marker is the same file in the volume whatever path it is read at.
	marker = path.Join(downloadMount, modelvolume.MarkerFile)
	script := readerScript(download, marker, deadline)
	if writer {
		if download == "" {
			return nil // engine writes; completion is Ready, marked by the agent
		}
		script = writerScript(download, marker)
	}
	init := corev1.Container{
		Name:    injectedDownloadInit,
		Image:   main.Image,
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{script},
		Env:     downloadEnv(main),
	}
	// The init mostly waits; the fallback download is the one case that
	// needs real resources, and policy needs limits either way.
	modelvolume.Harden(&init, modelvolume.DownloadResources, main.SecurityContext)
	// Only a hostPath landing receives a bind from the host after start.
	var prop *corev1.MountPropagationMode
	if m.hostPathReaders() {
		p := corev1.MountPropagationHostToContainer
		prop = &p
	}
	landVol := modelVolumeName
	for _, vm := range main.VolumeMounts {
		if vm.Name == land.VolumeName || vm.Name == modelVolumeName {
			landVol = vm.Name
			break
		}
	}
	init.VolumeMounts = []corev1.VolumeMount{{Name: landVol, MountPath: downloadMount, MountPropagation: prop}}
	patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/0", Value: init})
	if id.Scheme == "hf" {
		if main.Env == nil {
			patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/env", m.MainContainer), Value: []any{}})
		}
		patches = append(patches, appendEnv(m.MainContainer, corev1.EnvVar{Name: "HF_HUB_OFFLINE", Value: "1"}))
	}
	return patches
}

// initMountFor is the path the download init sees the landing volume at;
// it may differ from the main container's mount.
func initMountFor(init *corev1.Container, land modelid.Landing) string {
	for _, vm := range init.VolumeMounts {
		if vm.Name == land.VolumeName {
			return vm.MountPath
		}
	}
	return landingMount(land)
}

// writerScript runs the download, then leaves the completion marker at
// the volume root. The tree's byte count, when du can measure it, goes
// into the marker and into the container's termination message: kubelet
// unmounts a finished Job pod's volumes at once, so the agents read the
// count from the pod status rather than from the volume, and size the
// read-only views from it on storage whose claim size is nominal. Readers
// only test that the marker exists.
// writableWaitSeconds bounds the writer's wait for its landing to open.
const writableWaitSeconds = 600

func writerScript(download, marker string) string {
	dir := path.Dir(marker)
	// A fresh shared filesystem is root-owned until the agent on this node
	// opens it; the Job runs as the function's user and waits rather than
	// crash into the Job's backoff.
	wait := fmt.Sprintf("d=0\nwhile [ ! -w %[1]s ]; do if [ $d -ge %[2]d ]; then echo 'nvsnap: landing %[1]s is not writable' >&2; break; fi; sleep 2; d=$((d+2)); done\n", shellQuote(dir), writableWaitSeconds)
	failed := shellQuote(path.Join(dir, modelvolume.FailedMarkerFile))
	return fmt.Sprintf("set -e\nif [ -f %[1]s ]; then echo 'nvsnap: model already complete'; exit 0; fi\n%[4]srm -f %[5]s\n%[2]s\nsync\nn=$(du -sb %[3]s 2>/dev/null | cut -f1 || true)\nprintf '%%s' \"$n\" > %[1]s\nprintf '%%s' \"$n\" > /dev/termination-log 2>/dev/null || true\n", shellQuote(marker), download, shellQuote(dir), wait, failed)
}

// readerScript waits for the marker; past the deadline it runs the
// download itself (decided: always fall back, never deadlock).
func readerScript(download, marker string, deadline int) string {
	fallback := "echo 'nvsnap: no download step to fall back to'; exit 0"
	if download != "" {
		fallback = download
	}
	failed := shellQuote(path.Join(path.Dir(marker), modelvolume.FailedMarkerFile))
	return fmt.Sprintf("set -e\nd=0\nwhile [ ! -f %[1]s ]; do if [ -f %[4]s ]; then echo \"nvsnap: shared download failed: $(cat %[4]s 2>/dev/null)\"; %[3]s; exit 0; fi; if [ $d -ge %[2]d ]; then echo 'nvsnap: marker deadline passed; downloading locally'; %[3]s; exit 0; fi; sleep 5; d=$((d+5)); done\necho 'nvsnap: model complete, skipping download'\n", shellQuote(marker), deadline, fallback, failed)
}

func (m *Mutator) waitDeadlineSeconds() int {
	if m.ModelWaitDeadline > 0 {
		return int(m.ModelWaitDeadline.Seconds())
	}
	return waitScriptDeadline
}

// tokenEnv copies registry credentials the engine carries to the injected
// download init, by value or by reference.
func tokenEnv(main *corev1.Container) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, e := range main.Env {
		switch e.Name {
		case "HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "HF_HUB_ENABLE_HF_TRANSFER", "HF_HUB_DISABLE_XET", "HF_ENDPOINT":
			out = append(out, e)
		}
	}
	return out
}

// modelCacheEnvPatches redirects the compile caches into the pod-local
// cachedir, the same on every storage: the reader's view of the model
// volume is read-only, so nothing under the landing is writable ("Read-only
// file system: /model/.nvsnap", OCI FSS 2026-10-02, when a shared
// filesystem once put them there). The model entries of the template are
// dropped: the model lives in the landing volume, not under the cachedir.
func (m *Mutator) modelCacheEnvPatches(ctx context.Context, pod *corev1.Pod, main *corev1.Container, land modelid.Landing, uri string) []PatchOp {
	if m.CacheDir == "" {
		return nil
	}
	root := path.Join(m.CacheDir, "cache")
	patches := make([]PatchOp, 0, 8)
	if main.Env == nil {
		patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/env", m.MainContainer), Value: []any{}})
	}
	// Caches under this branch's root; the model stays with the landing
	// volume, which the chart addresses itself; switches as written
	// (reader_steps.go, one rule for both branches).
	for _, e := range m.cacheEnvFor(root, "") {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}
	// transformers writes trust_remote_code module sources to
	// HF_MODULES_CACHE, which defaults to $HF_HOME/modules. Charts that
	// point HF_HOME at the model directory then write into the shared
	// volume, which is read-only for every reader (GB300, 2026-09-30:
	// "Read-only file system: /config/models/modules"). Keep that cache
	// with the other per-pod caches unless the chart placed it itself.
	if hfHomeUnderLanding(main, land) && !hasEnv(main, "HF_MODULES_CACHE") {
		patches = append(patches, appendEnv(m.MainContainer, corev1.EnvVar{Name: "HF_MODULES_CACHE", Value: path.Join(root, "hf_modules")}))
	}
	// The local cachedir the capture reads, and the per-key cache set
	// that shares the compile caches between pods of one configuration.
	patches = append(patches, m.cacheDirVolumeOnly(pod, main)...)
	return m.cacheVolumePatches(ctx, pod, main, m.logger().WithFields(logrus.Fields{"pod": election.PodIdentity(pod), "model": uri}), patches)
}

// hfHomeUnderLanding reports whether the container's literal HF_HOME is
// the landing path or inside the volume mounted there.
func hfHomeUnderLanding(main *corev1.Container, land modelid.Landing) bool {
	hf := ""
	for _, e := range main.Env {
		if e.Name == "HF_HOME" {
			hf = e.Value
		}
	}
	if hf == "" {
		return false
	}
	hf = path.Clean(hf)
	for _, base := range []string{landingMount(land), land.Path} {
		if base != "" && (hf == base || strings.HasPrefix(hf, base+"/")) {
			return true
		}
	}
	return false
}

func hasEnv(c *corev1.Container, name string) bool {
	for _, e := range c.Env {
		if e.Name == name {
			return true
		}
	}
	return false
}

// cacheDirVolumeOnly adds the /opt/nvsnap emptyDir for compile caches
// without the model env of the capture decoration.
func (m *Mutator) cacheDirVolumeOnly(pod *corev1.Pod, main *corev1.Container) []PatchOp {
	for _, vm := range main.VolumeMounts {
		if vm.Name == cacheDirVolumeName || vm.MountPath == m.CacheDir {
			return nil
		}
	}
	patches := []PatchOp{}
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{}})
	}
	if main.VolumeMounts == nil {
		patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer), Value: []any{}})
	}
	patches = append(patches,
		PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: cacheDirVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer), Value: corev1.VolumeMount{Name: cacheDirVolumeName, MountPath: m.CacheDir}},
	)
	return append(patches, m.cacheDirInitPatches(pod, main)...)
}

// shellJoin renders an exec argv as one shell command line.
func shellJoin(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`&|;<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hfDownloadCommand fetches a Hugging Face repo into HF_HOME. huggingface_hub
// 1.x renamed the CLI to `hf` and made `huggingface-cli` a stub that only
// prints a deprecation notice (seen in vllm/vllm-openai:v0.20.0), so prefer
// `hf` and fall back for older images.
func hfDownloadCommand(id modelid.Identity) string {
	args := shellQuote(id.Ref)
	if id.Revision != "" {
		args += " --revision " + shellQuote(id.Revision)
	}
	return fmt.Sprintf("if command -v hf >/dev/null 2>&1; then hf download %[1]s; else huggingface-cli download %[1]s; fi", args)
}

// hostPathReaders reports whether readers land on a hostPath the agent
// binds into (as opposed to referencing the read-only view).
func (m *Mutator) hostPathReaders() bool {
	return m.ModelVolume != nil && m.ModelVolume.Cfg.ReaderMode() == modelvolume.ReaderHostPath
}
