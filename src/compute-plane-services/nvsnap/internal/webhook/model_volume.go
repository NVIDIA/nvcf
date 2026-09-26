// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelid"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// Model volume decoration (docs/proposals/helm-shared-model-volume.md).
// For a pod that will download a model, the webhook replaces the volume
// the download lands in with the per-identity model volume and turns the
// download step into a write-once: the elected writer downloads and marks
// completion, every other pod waits for the marker and skips its own
// download. No pod is gated; waiting is an init container.
//
// Roles:
//   - writer: download step runs, then touches <volume>/.nvsnap-complete.
//     Its engine mounts the model read-only so the volume stays immutable.
//   - reader, RWX mode: mounts the same claim; its download step becomes
//     "wait for the marker, else download" (fallback after the deadline).
//   - reader, Block mode: keeps its emptyDir at the landing path and waits
//     for the agent to bind the completed volume in and drop the marker.

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
	if !st.Complete {
		// The download step is a Job, created once per identity; create is
		// atomic so concurrent admissions converge without an election.
		// The claim lives where the Job runs, the pod's namespace.
		claim, err := m.ModelVolume.EnsureWriterClaim(ctx, uri, pod.Namespace)
		if err != nil {
			return nil, err
		}
		step, ok := m.downloadStep(pod, main, land, res.Identity)
		if !ok {
			log.Info("model volume: no download step can be derived (engine downloads a non-HF model); leaving pod alone")
			return nil, nil
		}
		job, err := m.ModelVolume.EnsureDownloadJob(ctx, uri, pod.Namespace, claim, step)
		if err != nil {
			return nil, err
		}
		log.WithFields(logrus.Fields{"claim": claim, "job": job}).Info("model volume: download job ensured")
	}
	patches = append(patches, mp.label(modelvolume.RoleLabel, "reader")...)
	switch m.ModelVolume.Cfg.Mode {
	case modelvolume.ModeRWX:
		claim, err := m.ModelVolume.EnsureWriterClaim(ctx, uri, pod.Namespace)
		if err != nil {
			return nil, err
		}
		patches = append(patches, m.substituteLandingVolume(pod, main, land, claim)...)
		log.WithFields(logrus.Fields{"claim": claim, "complete": st.Complete}).Info("model volume: reader on shared filesystem; waits for the marker")
	default:
		// Block mode: hostPath landing; the agent binds the completed
		// read-only volume over it and the marker inside appears.
		patches = append(patches, mp.label(modelvolume.PendingLabel, "true")...)
		patches = append(patches, mp.annotation(modelvolume.LandingAnnotation, landingMount(land))...)
		patches = append(patches, m.hostPathLanding(pod, main, land, uri)...)
		log.WithField("complete", st.Complete).Info("model volume: reader on block storage; agent binds the volume after completion")
	}
	patches = append(patches, m.downloadStepPatches(pod, main, land, res.Identity, false)...)
	patches = append(patches, m.modelCacheEnvPatches(pod, main, land, uri)...)
	return patches, nil
}

// downloadStep derives the Job's container from the pod: the chart's own
// download init wrapped to touch the marker, or `hf download` on the
// engine image when the engine fetches the model itself.
func (m *Mutator) downloadStep(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, id modelid.Identity) (modelvolume.DownloadStep, bool) {
	step := modelvolume.DownloadStep{ImagePullSecrets: pod.Spec.ImagePullSecrets, Tolerations: pod.Spec.Tolerations, NodeSelector: pod.Spec.NodeSelector}
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
			c.VolumeMounts = []corev1.VolumeMount{{Name: landVolumeName(land), MountPath: mount}}
			step.Container = c
			step.VolumeName = landVolumeName(land)
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
		Args:         []string{writerScript(hfDownloadCommand(id), path.Join(land.Path, modelvolume.MarkerFile))},
		Env:          append([]corev1.EnvVar{{Name: "HF_HOME", Value: land.Path}}, tokenEnv(main)...),
		VolumeMounts: []corev1.VolumeMount{{Name: step.VolumeName, MountPath: land.Path}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
		},
	}
	return step, true
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
		Env:     append([]corev1.EnvVar{{Name: "HF_HOME", Value: land.Path}}, tokenEnv(main)...),
	}
	prop := corev1.MountPropagationHostToContainer
	for _, vm := range main.VolumeMounts {
		if vm.Name == land.VolumeName || vm.Name == modelVolumeName {
			init.VolumeMounts = append(init.VolumeMounts, corev1.VolumeMount{Name: vm.Name, MountPath: vm.MountPath, MountPropagation: &prop})
		}
	}
	if len(init.VolumeMounts) == 0 {
		init.VolumeMounts = []corev1.VolumeMount{{Name: modelVolumeName, MountPath: land.Path, MountPropagation: &prop}}
	}
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

func writerScript(download, marker string) string {
	return fmt.Sprintf("set -e\nif [ -f %[1]s ]; then echo 'nvsnap: model already complete'; exit 0; fi\n%[2]s\nsync\ntouch %[1]s\n", shellQuote(marker), download)
}

// readerScript waits for the marker; past the deadline it runs the
// download itself (decided: always fall back, never deadlock).
func readerScript(download, marker string, deadline int) string {
	fallback := "echo 'nvsnap: no download step to fall back to'; exit 0"
	if download != "" {
		fallback = download
	}
	return fmt.Sprintf("set -e\nd=0\nwhile [ ! -f %[1]s ]; do if [ $d -ge %[2]d ]; then echo 'nvsnap: marker deadline passed; downloading locally'; %[3]s; exit 0; fi; sleep 5; d=$((d+5)); done\necho 'nvsnap: model complete, skipping download'\n", shellQuote(marker), deadline, fallback)
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

// modelCacheEnvPatches redirects the compile caches. RWX: into the shared
// volume under a key of image plus identity plus role-neutral args, so
// every pod of that engine config shares one set. Block: into the pod's
// local cachedir (captured after Ready by the existing path). The model
// entries of the template are dropped: the model lives in the landing
// volume now, not under the cachedir.
func (m *Mutator) modelCacheEnvPatches(pod *corev1.Pod, main *corev1.Container, land modelid.Landing, uri string) []PatchOp {
	root := ""
	switch {
	case m.ModelVolume.Cfg.Mode == modelvolume.ModeRWX:
		key := modelvolume.Key(uri)
		if m.Composer != nil {
			key = checkpointstore.ShortHash(checkpointstore.ComputeHash(m.Composer.Compose(pod, m.MainContainer)))[:16]
		}
		root = path.Join(landingMount(land), ".nvsnap", "cache", key)
	case m.CacheDir != "":
		root = path.Join(m.CacheDir, "cache")
	default:
		return nil
	}
	patches := make([]PatchOp, 0, 8)
	if main.Env == nil {
		patches = append(patches, PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/env", m.MainContainer), Value: []any{}})
	}
	for _, e := range m.cacheEnvVars(m.CacheDir) {
		if !strings.HasPrefix(e.Value, path.Join(m.CacheDir, "cache")) {
			continue // model entries (HF_HOME, NIM_CACHE_PATH) stay with the landing volume
		}
		rel := strings.TrimPrefix(e.Value, path.Join(m.CacheDir, "cache"))
		patches = append(patches, appendEnv(m.MainContainer, corev1.EnvVar{Name: e.Name, Value: root + rel}))
	}
	if m.ModelVolume.Cfg.Mode != modelvolume.ModeRWX && m.CacheDir != "" {
		// Block mode keeps the local cachedir emptyDir the capture reads.
		patches = append(patches, m.cacheDirVolumeOnly(pod, main)...)
	}
	return patches
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
	return append(patches,
		PatchOp{Op: "add", Path: "/spec/volumes/-", Value: corev1.Volume{Name: cacheDirVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		PatchOp{Op: "add", Path: fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer), Value: corev1.VolumeMount{Name: cacheDirVolumeName, MountPath: m.CacheDir}},
	)
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
