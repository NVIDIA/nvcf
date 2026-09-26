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

// Package modelid answers, for any GPU pod at admission, the first two of
// the four questions in docs/proposals/helm-shared-model-volume.md:
//
//   - Identity: which model artifact will this pod download, as a URI
//     (hf://org/repo[@rev], ngc://org/team/model:ver, s3://bucket/path,
//     nim://image[@profile]) so every chart, version and namespace that
//     names the same artifact shares one volume.
//   - Landing: where inside the pod the download writes and what volume
//     backs that path today, so the webhook knows what to substitute and
//     when to leave the pod alone.
//
// Sources, in the order the field uses them: the engine's own arguments
// (--model, --model-path, positional `vllm serve <x>`), engine env
// (HF_MODEL_ID, MODEL_ID, MODEL_PATH), an init container that downloads
// (NGC CLI, huggingface-cli, aws s3, KServe storage-initializer), a NIM
// image, and for group members with no identity of their own (LWS
// workers) the group's leader template.
package modelid

import (
	"path"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Identity is the normalized artifact reference.
type Identity struct {
	Scheme   string // hf, ngc, s3, nim, path
	Ref      string // org/repo, org/team/model:ver, bucket/key, image, /abs/path
	Revision string // hf revision when given
	Profile  string // NIM_MODEL_PROFILE when given
}

// URI renders the identity as the cache key everything else hangs off.
func (id Identity) URI() string {
	if id.Scheme == "" || id.Ref == "" {
		return ""
	}
	s := id.Scheme + "://" + id.Ref
	if id.Revision != "" {
		s += "@" + id.Revision
	}
	if id.Profile != "" {
		s += "@" + id.Profile
	}
	return s
}

// Downloader says which container performs the download.
type Downloader string

// Downloaders: the engine fetches at start (vllm serve --model X), an init
// container fetches before the engine, or the bytes are already present
// (PVC, hostPath, image).
const (
	DownloaderEngine Downloader = "engine"
	DownloaderInit   Downloader = "init"
	DownloaderNone   Downloader = "none"
)

// VolumeKind classifies what backs the landing path.
type VolumeKind string

// Volume kinds behind the landing path. emptyDir and rootfs are ours to
// replace; a PVC, hostPath or other volume is the customer's own storage.
const (
	VolumeEmptyDir VolumeKind = "emptyDir" // substitutable
	VolumeRootfs   VolumeKind = "rootfs"   // no volume: container filesystem, substitutable
	VolumePVC      VolumeKind = "pvc"      // customer's own shared storage: skip
	VolumeHostPath VolumeKind = "hostPath" // customer's node cache: skip
	VolumeOther    VolumeKind = "other"    // image volume, CSI ephemeral, etc.: skip
)

// Landing is where the download writes and what is under it.
type Landing struct {
	// Path is the directory the download populates inside the container.
	Path string
	// VolumeName is the pod volume mounted at (or above) Path; empty for rootfs.
	VolumeName string
	// MountPath is that volume's mount path; empty for rootfs.
	MountPath string
	Kind      VolumeKind
	// InitContainer is the name of the downloading init, when Downloader is init.
	InitContainer string
	Downloader    Downloader
}

// Substitutable reports whether the webhook may replace the landing volume
// with the shared model volume. A PVC, hostPath or image volume means the
// customer already solved sharing; leave it alone.
func (l Landing) Substitutable() bool {
	return l.Kind == VolumeEmptyDir || l.Kind == VolumeRootfs
}

// Result is what Resolve returns for a pod.
type Result struct {
	Identity Identity
	Landing  Landing
	// Source names where the identity came from, for logs.
	Source string
}

const (
	defaultHFHome = "/root/.cache/huggingface"
	kserveDest    = "/mnt/models"
)

var (
	modelFlagRe   = regexp.MustCompile(`(?:^|\s)--model(?:-path)?(?:=|\s+)(?:'([^']+)'|"([^"]+)"|([^\s\\'"]+))`)
	revisionRe    = regexp.MustCompile(`(?:^|\s)--revision(?:=|\s+)([^\s\\'"]+)`)
	servePosRe    = regexp.MustCompile(`\bvllm\s+serve\s+(?:'([^']+)'|"([^"]+)"|([^\s\\'"-][^\s\\'"]*))`)
	ngcDownloadRe = regexp.MustCompile(`ngc\s+registry\s+model\s+download-version(?:\s+--\S+(?:\s+[^\s-]\S*)?)*\s+"?([A-Za-z0-9_./-]+:[A-Za-z0-9_.-]+)"?`)
	ngcDestRe     = regexp.MustCompile(`--dest(?:=|\s+)"?([^\s"]+)"?`)
	hfCLIRe       = regexp.MustCompile(`(?:huggingface-cli|hf)\s+download\s+(?:--\S+\s+\S+\s+)*([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)`)
	hfLocalDirRe  = regexp.MustCompile(`--local-dir(?:=|\s+)"?([^\s"]+)"?`)
	s3Re          = regexp.MustCompile(`s3://([A-Za-z0-9_./-]+)`)
	shellVarRe    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

// Resolve derives identity and landing for the pod's main container.
// ok is false when the pod names no model: it is not a downloader and the
// webhook leaves it alone.
func Resolve(pod *corev1.Pod, mainContainer int) (Result, bool) {
	if pod == nil || mainContainer < 0 || mainContainer >= len(pod.Spec.Containers) {
		return Result{}, false
	}
	main := pod.Spec.Containers[mainContainer]
	// 1. An init container that downloads is the strongest signal: it
	//    names the artifact and the destination explicitly.
	for i := range pod.Spec.InitContainers {
		if r, ok := fromInit(pod, &pod.Spec.InitContainers[i], &main); ok {
			return r, true
		}
	}
	// 2. The engine downloads itself.
	if id, src, ok := fromEngine(&main); ok {
		land := landingFor(pod, &main, engineCachePath(&main), DownloaderEngine, "")
		return Result{Identity: id, Landing: land, Source: src}, true
	}
	return Result{}, false
}

// fromInit recognizes the download init containers seen in the field.
func fromInit(pod *corev1.Pod, init, main *corev1.Container) (Result, bool) {
	env := envMap(init)
	text := joinArgv(init, env)
	// NVCF Helm functions: NGC CLI download with the artifact in env.
	if name := env["NGC_MODEL_NAME"]; name != "" {
		dest := firstNonEmpty(env["NGC_MODEL_MOUNT"], ngcDest(text), mountRoot(init))
		return Result{Identity: Identity{Scheme: "ngc", Ref: name}, Landing: landingFor(pod, main, dest, DownloaderInit, init.Name), Source: "init env NGC_MODEL_NAME"}, true
	}
	if m := ngcDownloadRe.FindStringSubmatch(text); m != nil {
		dest := firstNonEmpty(ngcDest(text), env["NGC_MODEL_MOUNT"], mountRoot(init))
		return Result{Identity: Identity{Scheme: "ngc", Ref: m[1]}, Landing: landingFor(pod, main, dest, DownloaderInit, init.Name), Source: "init ngc registry model download-version"}, true
	}
	if m := hfCLIRe.FindStringSubmatch(text); m != nil {
		dest := firstNonEmpty(hfLocalDir(text), env["HF_HOME"], mountRoot(init))
		id := Identity{Scheme: "hf", Ref: m[1]}
		if r := revisionRe.FindStringSubmatch(text); r != nil {
			id.Revision = r[1]
		}
		return Result{Identity: id, Landing: landingFor(pod, main, dest, DownloaderInit, init.Name), Source: "init huggingface-cli download"}, true
	}
	if m := s3Re.FindStringSubmatch(text); m != nil && (strings.Contains(text, "s3 sync") || strings.Contains(text, "s3 cp") || strings.Contains(text, "s5cmd")) {
		return Result{Identity: Identity{Scheme: "s3", Ref: strings.TrimSuffix(m[1], "/")}, Landing: landingFor(pod, main, mountRoot(init), DownloaderInit, init.Name), Source: "init s3"}, true
	}
	// KServe storage-initializer: args [storageUri, destDir].
	if strings.Contains(init.Image, "storage-initializer") && len(init.Args) >= 2 {
		if id, ok := parseURI(init.Args[0]); ok {
			return Result{Identity: id, Landing: landingFor(pod, main, init.Args[1], DownloaderInit, init.Name), Source: "kserve storage-initializer"}, true
		}
		if strings.HasPrefix(init.Args[0], "pvc://") {
			return Result{Identity: Identity{}, Landing: Landing{Path: init.Args[1], Kind: VolumePVC, Downloader: DownloaderNone}, Source: "kserve pvc"}, false
		}
	}
	return Result{}, false
}

// fromEngine reads the engine's own arguments and env.
func fromEngine(main *corev1.Container) (Identity, string, bool) {
	env := envMap(main)
	text := joinArgv(main, env)
	rev := ""
	if r := revisionRe.FindStringSubmatch(text); r != nil {
		rev = r[1]
	}
	if m := modelFlagRe.FindStringSubmatch(text); m != nil {
		return classifyRef(first(m[1:]), rev, env), "--model", true
	}
	if m := servePosRe.FindStringSubmatch(text); m != nil {
		return classifyRef(first(m[1:]), rev, env), "vllm serve <model>", true
	}
	for _, k := range []string{"HF_MODEL_ID", "MODEL_ID"} {
		if v := env[k]; v != "" {
			return classifyRef(v, rev, env), "env " + k, true
		}
	}
	if v := env["MODEL_PATH"]; v != "" {
		return classifyRef(v, rev, env), "env MODEL_PATH", true
	}
	if isNIMImage(main.Image) {
		return Identity{Scheme: "nim", Ref: main.Image, Profile: env["NIM_MODEL_PROFILE"]}, "nim image", true
	}
	return Identity{}, "", false
}

// classifyRef turns a --model value into an identity: an absolute path is
// a local path (an init filled it, or it is baked into the image); a
// URI keeps its scheme; anything else is a Hugging Face repo id.
func classifyRef(ref, rev string, env map[string]string) Identity {
	ref = expandEnv(ref, env)
	if id, ok := parseURI(ref); ok {
		return id
	}
	if strings.HasPrefix(ref, "/") {
		return Identity{Scheme: "path", Ref: path.Clean(ref)}
	}
	return Identity{Scheme: "hf", Ref: ref, Revision: rev}
}

func parseURI(s string) (Identity, bool) {
	for _, scheme := range []string{"hf", "ngc", "s3", "gs", "oci"} {
		if strings.HasPrefix(s, scheme+"://") {
			ref := strings.TrimPrefix(s, scheme+"://")
			id := Identity{Scheme: scheme, Ref: strings.TrimSuffix(ref, "/")}
			if scheme == "hf" {
				if i := strings.LastIndex(id.Ref, "@"); i > 0 {
					id.Revision, id.Ref = id.Ref[i+1:], id.Ref[:i]
				}
			}
			return id, true
		}
	}
	return Identity{}, false
}

// engineCachePath is where an engine-internal download lands.
func engineCachePath(main *corev1.Container) string {
	env := envMap(main)
	if isNIMImage(main.Image) && env["NIM_CACHE_PATH"] != "" {
		return env["NIM_CACHE_PATH"]
	}
	if v := env["HF_HOME"]; v != "" {
		return v
	}
	if v := env["HF_HUB_CACHE"]; v != "" {
		return v
	}
	return defaultHFHome
}

// landingFor finds the volume mounted at or above dest in the main
// container and classifies it. The download init may mount the volume
// under a different path; the main container's view is what the engine
// reads, so it is the reference.
func landingFor(pod *corev1.Pod, main *corev1.Container, dest string, dl Downloader, initName string) Landing {
	dest = path.Clean(dest)
	l := Landing{Path: dest, Kind: VolumeRootfs, Downloader: dl, InitContainer: initName}
	best := ""
	for _, vm := range main.VolumeMounts {
		mp := path.Clean(vm.MountPath)
		if (dest == mp || strings.HasPrefix(dest, mp+"/")) && len(mp) > len(best) {
			best = mp
			l.VolumeName, l.MountPath = vm.Name, mp
		}
	}
	if l.VolumeName == "" {
		return l
	}
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name != l.VolumeName {
			continue
		}
		switch {
		case v.EmptyDir != nil:
			l.Kind = VolumeEmptyDir
		case v.PersistentVolumeClaim != nil:
			l.Kind = VolumePVC
		case v.HostPath != nil:
			l.Kind = VolumeHostPath
		default:
			l.Kind = VolumeOther
		}
	}
	return l
}

// mountRoot is the first non-system mount of a container, the usual
// destination of a download init that does not say where it writes.
func mountRoot(c *corev1.Container) string {
	paths := []string{}
	for _, vm := range c.VolumeMounts {
		if strings.HasPrefix(vm.MountPath, "/var/run/secrets") || vm.MountPath == "/dev/shm" {
			continue
		}
		paths = append(paths, vm.MountPath)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return kserveDest
	}
	return paths[0]
}

func ngcDest(text string) string {
	if m := ngcDestRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func hfLocalDir(text string) string {
	if m := hfLocalDirRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func envMap(c *corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		if e.Value != "" {
			m[e.Name] = e.Value
		}
	}
	return m
}

// joinArgv flattens command+args into one string and expands $VAR from
// the container's own literal env, so `vllm serve ${MODEL_PATH}` resolves.
func joinArgv(c *corev1.Container, env map[string]string) string {
	parts := append(append([]string{}, c.Command...), c.Args...)
	return expandEnv(strings.Join(parts, " "), env)
}

func expandEnv(s string, env map[string]string) string {
	return shellVarRe.ReplaceAllStringFunc(s, func(tok string) string {
		name := shellVarRe.FindStringSubmatch(tok)[1]
		if v, ok := env[name]; ok {
			return v
		}
		return tok
	})
}

func isNIMImage(image string) bool {
	return strings.Contains(image, "nvcr.io/nim/") || strings.Contains(image, "/nim/")
}

func first(groups []string) string {
	for _, g := range groups {
		if g != "" {
			return g
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
