// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// The steps nvsnap adds to a pod (wait for a shared model, write it, seed
// a cache, sweep a volume into the page cache, hold a restore placeholder)
// run nvsnap-helper (cmd/nvsnap-helper) from the node's nvsnap bundle,
// mounted read-only. A step's container uses the workload's own image, so
// nothing extra is pulled, but the helper is static and needs nothing from
// that image: no shell, no coreutils, no particular architecture. A
// program of the workload (a chart's download, the image's Hugging Face
// downloader) is executed by the helper directly, found on the image's own
// PATH.

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelid"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

const (
	// helperVolumeName mounts the node's nvsnap bundle read-only into the
	// containers that run the helper.
	helperVolumeName = modelvolume.HelperVolumeName
	helperMountPath  = modelvolume.HelperMountPath
	// HelperBin is the helper as the step's container sees it.
	HelperBin = modelvolume.HelperBin
	// IdleBin keeps a container alive; the CRIU restore placeholder runs it.
	IdleBin = modelvolume.IdleBin
)

// hfDownloaders are the Hugging Face CLIs, newest first: huggingface_hub
// 1.x renamed the CLI to hf and left huggingface-cli a stub that only
// prints a deprecation notice. The helper runs the first one the image
// has.
var hfDownloaders = []string{"hf", "huggingface-cli"}

func helperMount() corev1.VolumeMount { return modelvolume.HelperMount() }

// AgentPlacement is where the agent DaemonSet runs: its nodeSelector and
// node affinity, as the chart sets them. Pods nvsnap creates that carry no
// GPU request (the model download Job) get it, so they run where an agent
// staged the helper and can copy what they download.
type AgentPlacement struct {
	NodeSelector map[string]string    `json:"nodeSelector,omitempty"`
	NodeAffinity *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`
}

// onAgentNodes returns the pod's nodeSelector with the agent's added, and
// the agent's node affinity.
func (m *Mutator) onAgentNodes(sel map[string]string) (map[string]string, *corev1.Affinity) {
	out := map[string]string{}
	for k, v := range m.AgentPlacement.NodeSelector {
		out[k] = v
	}
	for k, v := range sel {
		out[k] = v
	}
	if len(out) == 0 {
		out = nil
	}
	if m.AgentPlacement.NodeAffinity == nil {
		return out, nil
	}
	return out, &corev1.Affinity{NodeAffinity: m.AgentPlacement.NodeAffinity.DeepCopy()}
}

// helperVolume is the node directory the agent stages its bundle into,
// the helper among it.
func (m *Mutator) helperVolume() corev1.Volume {
	root := m.HostBundleRoot
	if root == "" {
		root = DefaultHostBundleRoot
	}
	dir := corev1.HostPathDirectory
	return corev1.Volume{Name: helperVolumeName, VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: root + "/nvsnap", Type: &dir}}}
}

// withHelperVolume adds the helper's volume to the pod once.
func (m *Mutator) withHelperVolume(pod *corev1.Pod, patches []PatchOp) []PatchOp {
	if podHasVolume(pod, helperVolumeName) || patchesAddVolume(patches, helperVolumeName) {
		return patches
	}
	return append(patches, addVolumes(pod, []corev1.Volume{m.helperVolume()})...)
}

func podHasVolume(pod *corev1.Pod, name string) bool {
	for _, v := range pod.Spec.Volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

func patchesAddVolume(patches []PatchOp, name string) bool {
	for _, p := range patches {
		if p.Op != "add" {
			continue
		}
		switch v := p.Value.(type) {
		case corev1.Volume:
			if v.Name == name {
				return true
			}
		case []corev1.Volume:
			for _, e := range v {
				if e.Name == name {
					return true
				}
			}
		case []any:
			for _, e := range v {
				if vv, ok := e.(corev1.Volume); ok && vv.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// helperWrite is the argv that runs a download into dir and records its
// completion marker. try names alternative programs (the first found on
// the image's PATH runs, with args); without it args is the command. The
// tree's byte count goes into the marker and into the container's
// termination message: kubelet unmounts a finished Job pod's volumes at
// once, so the agents read the count from the pod status and size the
// read-only views from it on storage whose claim size is nominal. Readers
// only test that the marker exists.
func helperWrite(marker, failed, dir string, try, args []string) []string {
	argv := []string{HelperBin, "write", "--marker", marker, "--failed", failed, "--dir", dir,
		"--writable-wait", strconv.Itoa(writableWaitSeconds)}
	for _, t := range try {
		argv = append(argv, "--try", t)
	}
	return append(append(argv, "--"), args...)
}

// helperWait is the argv that waits for a completion marker. With inPlace,
// past the deadline or on a recorded failure it runs the download (try,
// args) itself: always fall back, never deadlock. Without it the landing is
// the read-only view of the shared primary, where nothing can be written:
// the init stops with the reason, and the agent recreates the pod, which
// admission then leaves on its own download.
func helperWait(marker, failed string, deadline int, inPlace bool, try, args []string) []string {
	argv := []string{HelperBin, "wait", "--marker", marker, "--failed", failed, "--deadline", strconv.Itoa(deadline)}
	if inPlace {
		argv = append(argv, "--in-place")
	}
	for _, t := range try {
		argv = append(argv, "--try", t)
	}
	return append(append(argv, "--"), args...)
}

// hfDownloadArgs are the downloader's arguments for a Hugging Face repo;
// it downloads into HF_HOME.
func hfDownloadArgs(id modelid.Identity) []string {
	args := []string{"download", id.Ref}
	if id.Revision != "" {
		args = append(args, "--revision", id.Revision)
	}
	return args
}

// containerArgv is the command a container runs: its command and args.
func containerArgv(c *corev1.Container) []string {
	return append(append([]string{}, c.Command...), c.Args...)
}
