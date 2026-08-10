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

package rootfsonly

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// CaptureRequest describes a pod the agent should capture.
type CaptureRequest struct {
	// PodUID is metadata.uid — used by the PIDResolver and to compute
	// the kubelet host paths for emptyDir volumes.
	PodUID string

	// Namespace and Name are recorded in the manifest for audit/debug
	// only; they don't drive any capture behavior.
	Namespace string
	Name      string

	// Spec is pod.spec — drives volume classification + main image lookup.
	Spec *corev1.PodSpec

	// MainContainer is the index in spec.Containers whose volumeMounts
	// and image we use. Most nvsnap workloads are single-container at 0.
	MainContainer int

	// HashInput is the canonical content-addressed identity (image
	// digest + model id + engine compat flags + driver major). The agent
	// composes this from the pod spec; the orchestrator hashes it.
	HashInput checkpointstore.HashInput

	// GPUType and DriverVersion are display-only metadata resolved from
	// the capture node's labels by the watcher (gpu product name, full
	// driver version string). Recorded in the manifest for the UI; they
	// do NOT feed the content hash (only the driver MAJOR does, via
	// HashInput.CUDADriverMajor). Empty when the node lookup failed.
	GPUType       string
	DriverVersion string
}

// Capturer ties together the pure pieces (classify, PID resolution,
// mountinfo) and writes the resulting tree to a checkpointstore.Backend.
//
// Concurrency: a single Capturer is safe for concurrent Capture calls
// only if the underlying Backend is. The Local backend is safe; future
// PVC-based backends will need their own coordination (tracked in #99).
type Capturer struct {
	// Backend is where captures land. Phase 1 ships Local; #99 adds GPD-ROX.
	Backend checkpointstore.Backend

	// PIDResolver finds the pod's main process. Required.
	PIDResolver *PIDResolver

	// NodeName is the K8s node name the agent runs on. Recorded in
	// every capture's Manifest.CapturedOnNodes so the webhook can
	// inject nodeAffinity for FRESH pods (only meaningful for
	// per-node-storage backends like Local). Empty = don't record.
	NodeName string

	// HostFSRoot is the directory the agent should prefix to host paths
	// when reading them. In production the agent runs in a container
	// with hostPID=true; "/proc/1/root" lets it walk the host's
	// filesystem through PID 1's root view. In tests, this is a temp dir
	// that mocks the host FS.
	HostFSRoot string

	// CacheDir, when non-empty (e.g. "/nvsnap"), switches the capture to
	// "cachedir" mode: instead of the container rootfs upperdir + user
	// volumes, capture ONLY the volume mounted at this path as the whole
	// PVC root. The webhook injects that volume (an emptyDir) and the
	// HF_HOME / TORCHINDUCTOR / HOME env vars that funnel all the
	// expensive-to-rebuild state into it. Restore mounts the rox
	// read-only here directly — no overlayfs, no pivot_root.
	CacheDir string

	// KubeletPodsDir is the host-side path to per-pod kubelet state,
	// where emptyDir volumes are materialized. Default
	// "/var/lib/kubelet/pods". Joined with HostFSRoot at read time.
	KubeletPodsDir string

	// MountinfoProcRoot is the proc root the orchestrator reads
	// mountinfo from. Default DefaultProcRoot() (/host/proc or /proc).
	// Tests override.
	MountinfoProcRoot string

	// Log is the structured logger; nil disables logging.
	Log logrus.FieldLogger

	// PostCommit is invoked after a successful Backend.Put. Used by the
	// agent to push the capture into nvsnap-blobstore for the tier-3
	// fallback in EnsureCaptureLocal. nil = no-op. Errors are logged
	// but do not roll back the capture — tier-2 peer fetch keeps
	// working regardless.
	PostCommit func(ctx context.Context, hash string) error
}

// Layout under a committed capture's tree directory:
//
//	tree/
//	  rootfs/                  (overlay upperdir; absent for NIM)
//	  volumes/
//	    <volname>/             (host-side contents of one user-data volume)
const (
	stagingRootfsDir  = "rootfs"
	stagingVolumesDir = "volumes"
)

// Capture-method stamps written into the manifest. The restore side
// dispatches on these deterministically (webhook mutate.go), so they are
// the contract between capture and restore — do not infer method from
// manifest shape (the "crossing paths" bug, GCP-H100-a 2026-06-10).
const (
	captureMethodRootfs   = "rootfs"
	captureMethodCacheDir = "cachedir"
)

// captureMethodFor returns the authoritative capture-method stamp and the
// cache root for the manifest. "cachedir" (capture ONLY the configured
// pod cache mount + prewarm on restore) when a pod cache dir is set;
// otherwise "rootfs" (whole container rootfs + user-data volumes). The
// agent wires cacheDir from --pod-cache-dir (Capturer.CacheDir), so a
// cluster with cachedir/ember mode on always stamps "cachedir".
func captureMethodFor(cacheDir string) (method, dir string) {
	if cacheDir != "" {
		return captureMethodCacheDir, cacheDir
	}
	return captureMethodRootfs, ""
}

// Capture executes the full flow: hash → check existing → resolve PID →
// read mountinfo → classify → assemble sources → Backend.Put. Idempotent:
// if the hash already exists in the backend, returns the existing manifest
// with no I/O work done.
//
// The orchestrator does NOT copy data; it hands sources (host paths +
// excludes + destination subpaths) to the Backend, which owns the actual
// I/O. This keeps the path from source bytes to committed bytes a single
// read+write, regardless of whether the Backend writes locally (Local) or
// via a one-shot Job into a per-capture PVC (GPDRox).
//
// Errors:
//   - ErrPodNotRunning if no process is found for req.PodUID
//   - the Backend.Put error, except ErrExists (idempotent path)
//   - I/O errors during source resolution (wrapped with context)
func (c *Capturer) Capture(ctx context.Context, req CaptureRequest) (checkpointstore.Manifest, error) {
	ctx, span := tracing.Tracer().Start(ctx, "rootfs.capture")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.pod", req.Namespace+"/"+req.Name),
		attribute.String("nvsnap.pod_uid", req.PodUID),
	)
	if c.Backend == nil {
		return checkpointstore.Manifest{}, errors.New("rootfsonly: Capturer.Backend is nil")
	}
	if c.PIDResolver == nil {
		return checkpointstore.Manifest{}, errors.New("rootfsonly: Capturer.PIDResolver is nil")
	}
	if req.Spec == nil {
		return checkpointstore.Manifest{}, errors.New("rootfsonly: CaptureRequest.Spec is nil")
	}

	hash := checkpointstore.ComputeHash(req.HashInput)
	log := c.logger().WithFields(logrus.Fields{
		"hash":    checkpointstore.ShortHash(hash),
		"pod":     req.Namespace + "/" + req.Name,
		"pod_uid": req.PodUID,
	})

	// Idempotent: if the backend already has this hash, we're done.
	if existing, err := c.Backend.Stat(ctx, hash); err == nil {
		log.Info("capture skipped: hash already exists")
		return existing, nil
	} else if !errors.Is(err, checkpointstore.ErrNotFound) {
		return checkpointstore.Manifest{}, fmt.Errorf("backend stat: %w", err)
	}

	pid, err := c.PIDResolver.ResolvePodPID(req.PodUID)
	if err != nil {
		return checkpointstore.Manifest{}, fmt.Errorf("resolve pod pid: %w", err)
	}
	log = log.WithField("pid", pid)

	_, buildSpan := tracing.Tracer().Start(ctx, "rootfs.build_sources")
	sources, volumes, extractPaths, err := c.buildSources(pid, req)
	if err != nil {
		buildSpan.RecordError(err)
		buildSpan.End()
		return checkpointstore.Manifest{}, err
	}
	buildSpan.SetAttributes(
		attribute.Int("nvsnap.source_count", len(sources)),
		attribute.Int("nvsnap.volume_count", len(volumes)),
		attribute.Int("nvsnap.extract_path_count", len(extractPaths)),
	)
	buildSpan.End()
	for _, p := range extractPaths {
		log.WithFields(logrus.Fields{
			"category": p.Category,
			"path":     p.Path,
			"size_mib": p.SizeBytes / 1024 / 1024,
			"files":    p.FileCount,
		}).Info("rootfs extract path recorded")
	}

	totalSize, totalFiles := int64(0), int64(0)
	for _, v := range volumes {
		totalSize += v.SizeBytes
		totalFiles += v.FileCount
	}

	mainImage, mainContainerName := "", ""
	gpuCount := 0
	if req.MainContainer >= 0 && req.MainContainer < len(req.Spec.Containers) {
		mc := req.Spec.Containers[req.MainContainer]
		mainImage = mc.Image
		mainContainerName = mc.Name
		// nvidia.com/gpu is an integer extended resource; prefer limits
		// (always set for GPUs) and fall back to requests.
		if q, ok := mc.Resources.Limits["nvidia.com/gpu"]; ok {
			gpuCount = int(q.Value())
		} else if q, ok := mc.Resources.Requests["nvidia.com/gpu"]; ok {
			gpuCount = int(q.Value())
		}
	}

	// Record the source PID-1 argv so the whole-rootfs restore shim
	// (cmd/nvsnap-rootfs-restore) can exec it after pivot_root. Critical
	// for ENTRYPOINT-only images (NIM, whisper) whose command is absent
	// from the restored Pod spec. Best-effort: nil on read error → the
	// restore webhook falls back to the pod's explicit command/args.
	procRoot := c.MountinfoProcRoot
	if procRoot == "" {
		procRoot = mountinfo.DefaultProcRoot()
	}
	entryArgv := readEntryArgv(procRoot, pid)
	if len(entryArgv) > 0 {
		log.WithField("entry_argv", entryArgv).Info("recorded source entrypoint for whole-rootfs restore")
	} else {
		log.Warn("could not read source /proc/<pid>/cmdline; restore will rely on the pod's command/args")
	}

	// Authoritative capture-method stamp so the restore side dispatches
	// deterministically (no inferring from manifest shape — the
	// "crossing paths" bug, GCP-H100-a 2026-06-10). "cachedir" when we
	// captured only the configured cache mount; "rootfs" otherwise.
	captureMethod, cacheDir := captureMethodFor(c.CacheDir)
	var cacheEnv map[string]string
	if captureMethod == captureMethodCacheDir {
		// Stamp the cache/model env the pod actually ran with — the
		// per-checkpoint single source of truth. Restore replays this
		// verbatim, so path-consistency (cache reuse) holds regardless
		// of any later edit to the cachedir-env ConfigMap.
		cacheEnv = collectCacheEnv(req.Spec, req.MainContainer, c.CacheDir)
	}

	manifest := checkpointstore.Manifest{
		CaptureMethod: captureMethod,
		CacheDir:      cacheDir,
		CacheEnv:      cacheEnv,
		SourcePodMeta: map[string]string{
			"namespace":              req.Namespace,
			"name":                   req.Name,
			"uid":                    req.PodUID,
			"image":                  mainImage,
			"engine":                 classifyEngine(mainImage),
			"container":              mainContainerName,
			"model_id":               req.HashInput.ModelID,
			"gpu_count":              strconv.Itoa(gpuCount),
			"driver_major":           strconv.Itoa(req.HashInput.CUDADriverMajor),
			"gpu_type":               req.GPUType,
			"driver_version":         req.DriverVersion,
			"gpu_compute_capability": req.HashInput.GPUComputeCapability,
		},
		Volumes:            volumes,
		RootfsExtractPaths: extractPaths,
		TotalSizeBytes:     totalSize,
		FileCount:          totalFiles,
		EntryArgv:          entryArgv,
	}
	if c.NodeName != "" {
		manifest.CapturedOnNodes = []string{c.NodeName}
	}

	putCtx, putSpan := tracing.Tracer().Start(ctx, "rootfs.backend_put")
	putSpan.SetAttributes(
		attribute.Int64("nvsnap.scanned_size_bytes", totalSize),
		attribute.Int64("nvsnap.scanned_file_count", totalFiles),
	)
	stored, err := c.Backend.Put(putCtx, hash, sources, manifest)
	putSpan.SetAttributes(
		attribute.Int64("nvsnap.stored_size_bytes", stored.TotalSizeBytes),
		attribute.Int64("nvsnap.stored_file_count", stored.FileCount),
	)
	if err != nil {
		putSpan.RecordError(err)
	}
	putSpan.End()
	if errors.Is(err, checkpointstore.ErrExists) {
		// Race with another capture (e.g. two warm-pod watchers): treat as
		// idempotent success. Re-read manifest.
		log.Info("backend.Put returned ErrExists; another capture won the race")
		return c.Backend.Stat(ctx, hash)
	}
	if err != nil {
		return checkpointstore.Manifest{}, fmt.Errorf("backend put: %w", err)
	}
	log.WithFields(logrus.Fields{
		"size_mib": stored.TotalSizeBytes / 1024 / 1024,
		"files":    stored.FileCount,
	}).Info("capture committed")

	// Best-effort post-commit hook (typically nvsnap-blobstore upload).
	// Done async so the capture appears available immediately; tier-3
	// readiness lags by the upload duration, which is fine because
	// tier-1 (local) and tier-2 (peer) are already serving.
	if c.PostCommit != nil {
		go func() {
			if err := c.PostCommit(context.Background(), hash); err != nil {
				log.WithError(err).Warn("post-commit hook failed (tier-3 unavailable for this capture)")
			}
		}()
	}
	return stored, nil
}

// buildSources walks classified volumes and returns the source list the
// Backend will copy from, plus the matching VolumeMeta entries (with
// pre-scanned sizes) and any rootfs extract paths discovered.
//
// Sizes are populated via a metadata-only walk of the source — accurate
// to within the bytes filtered by mountinfo excludes (a few KB of
// /etc/hostname et al.), which is acceptable for the manifest counters
// shown to users. Authoritative byte counts live in TotalSizeBytes /
// FileCount, which Backend.Put fills in from the actual copy.
func (c *Capturer) buildSources(pid int, req CaptureRequest) (
	[]checkpointstore.CaptureSource,
	[]checkpointstore.VolumeMeta,
	[]checkpointstore.ExtractPath,
	error,
) {
	var sources []checkpointstore.CaptureSource
	var volumes []checkpointstore.VolumeMeta
	var extractPaths []checkpointstore.ExtractPath

	classified := ClassifyVolumes(req.Spec, req.MainContainer)

	// cachedir mode: capture ONLY the volume mounted at c.CacheDir
	// (the webhook-injected emptyDir, e.g. /nvsnap, into which HF_HOME /
	// TORCHINDUCTOR / HOME were redirected) as the entire PVC root.
	// No rootfs upperdir, no other volumes — restore mounts the rox
	// read-only at c.CacheDir directly (no overlayfs).
	if c.CacheDir != "" {
		for _, cls := range classified {
			if cls.Kind != VolumeUserData || cls.MountPath != c.CacheDir {
				continue
			}
			src, err := c.resolveUserDataSrc(req.PodUID, cls)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resolve cache dir %s: %w", c.CacheDir, err)
			}
			size, files, _ := dirContentStats(src)
			sources = append(sources, checkpointstore.CaptureSource{
				SrcPath:    src,
				DstSubpath: "", // contents land at the PVC root
			})
			volumes = append(volumes, checkpointstore.VolumeMeta{
				Name:      "cachedir",
				MountPath: c.CacheDir,
				Type:      cls.VolumeType,
				SizeBytes: size,
				FileCount: files,
			})
			return sources, volumes, extractPaths, nil
		}
		return nil, nil, nil, fmt.Errorf("cachedir mode: no volume mounted at %s on the main container (webhook must inject it)", c.CacheDir)
	}
	for _, cls := range classified {
		switch cls.Kind {
		case VolumeRootfs:
			src, excludes, err := c.resolveRootfs(pid)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resolve rootfs: %w", err)
			}
			size, files, _ := dirContentStats(src)
			// nvsnap#88: generic enumeration replaces the curated catalog.
			// Walk the captured upperdir and emit one mount per
			// directory that has direct file children (defense-in-depth
			// skip set drops /tmp, /run, /proc, /sys, /dev, /var/run,
			// /var/log). MinSubdirBytes filters noise (.gitkeep, empty
			// per-pod control files).
			extractPaths = enumerateMountPoints(src, mountPointMinBytes)
			sources = append(sources, checkpointstore.CaptureSource{
				SrcPath:    src,
				DstSubpath: stagingRootfsDir,
				Excludes:   excludes,
			})
			volumes = append(volumes, checkpointstore.VolumeMeta{
				Name:      "rootfs",
				MountPath: "/",
				Type:      "rootfs",
				SizeBytes: size,
				FileCount: files,
			})
		case VolumeUserData:
			src, err := c.resolveUserDataSrc(req.PodUID, cls)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resolve volume %s: %w", cls.Name, err)
			}
			size, files, _ := dirContentStats(src)
			sources = append(sources, checkpointstore.CaptureSource{
				SrcPath:    src,
				DstSubpath: filepath.Join(stagingVolumesDir, cls.Name),
			})
			volumes = append(volumes, checkpointstore.VolumeMeta{
				Name:      cls.Name,
				MountPath: cls.MountPath,
				Type:      cls.VolumeType,
				SizeBytes: size,
				FileCount: files,
			})
		}
	}
	return sources, volumes, extractPaths, nil
}

// resolveRootfs returns the absolute host path of the source pod's
// overlay upperdir (already prefixed with HostFSRoot) and the
// excludes to apply during the capture-copy. Excludes are the union
// of:
//
//   - dynamic: every non-"/" mountpoint in the source pod's mountinfo
//     (e.g. /dev, /sys, /proc, /etc/hostname — kubelet bind-mounts).
//   - static (nvsnap#88): paths that are part of the overlay upperdir
//     in some container runtimes but are never safe to replay
//     cross-pod (e.g. /tmp, /run, /var/log, /var/cache). Keeps the
//     captured tree small and protects against the enumerator
//     surfacing per-pod state even if its defensive skip-set ever
//     drifts.
func (c *Capturer) resolveRootfs(pid int) (rootfsPath string, excludePaths []string, err error) {
	procRoot := c.MountinfoProcRoot
	if procRoot == "" {
		procRoot = mountinfo.DefaultProcRoot()
	}
	upperdir, err := mountinfo.ResolveOverlayUpperdirAtRoot(procRoot, pid)
	if err != nil {
		return "", nil, fmt.Errorf("resolve upperdir: %w", err)
	}
	if upperdir == "" {
		return "", nil, errors.New("source pod has no overlay rootfs (non-overlay storage driver?)")
	}
	excludes, err := mountinfo.NonRootMountPointsAtRoot(procRoot, pid)
	if err != nil {
		return "", nil, fmt.Errorf("read non-root mountpoints: %w", err)
	}
	excludes = append(excludes, alwaysExcludeRootfsPaths...)
	return filepath.Join(c.HostFSRoot, upperdir), excludes, nil
}

// alwaysExcludeRootfsPaths are absolute container paths that are
// per-pod, transient, or kubelet-managed — never safe to replay onto
// a restored pod. Excluded at capture time so they never enter the
// tree/rootfs/ artifact. Belt-and-braces with the enumerator's
// restore-side skip set in enumerate.go (mountPointSkipPrefixes).
//
// Match semantics in treecopy.Copier: exact-path OR ancestor-of-path.
// "/tmp" excludes both /tmp itself and everything under it.
var alwaysExcludeRootfsPaths = []string{
	"/tmp",             // transient scratch (per-process tempfiles, torchinductor JIT)
	"/run",             // tmpfs in most distros (kubelet SA token, secrets, runtime sockets)
	"/var/run",         // legacy alias of /run on some images
	"/var/log",         // per-pod stdout/stderr capture + service logs
	"/var/cache/apt",   // package-manager state — meaningless cross-pod
	"/var/cache/yum",   // same for RHEL family
	"/var/lib/dhcp",    // per-pod DHCP lease cache
	"/var/lib/systemd", // per-pod systemd state if image uses it
	"/etc/hostname",    // kubelet-set per pod
	"/etc/hosts",       // kubelet-set per pod
	"/etc/resolv.conf", // kubelet-set per pod
	"/etc/mtab",        // mount table — stale on restore
	"/etc/ld.so.cache", // linker cache — regenerated on demand and arch-specific
}

// resolveUserDataSrc returns the absolute host path that holds the
// contents of one user-data volume (hostPath: as-is; emptyDir:
// kubelet's per-pod materialization path).
func (c *Capturer) resolveUserDataSrc(podUID string, cls Classified) (string, error) {
	switch cls.VolumeType {
	case "hostPath":
		return filepath.Join(c.HostFSRoot, cls.HostPath), nil
	case "emptyDir":
		return filepath.Join(c.HostFSRoot, c.kubeletPodsDir(),
			podUID, "volumes", "kubernetes.io~empty-dir", cls.Name), nil
	default:
		return "", fmt.Errorf("unsupported volume type: %s", cls.VolumeType)
	}
}

func (c *Capturer) kubeletPodsDir() string {
	if c.KubeletPodsDir != "" {
		return c.KubeletPodsDir
	}
	return "/var/lib/kubelet/pods"
}

// classifyEngine infers the inference-engine family from the container
// image. Used as a label on capture artifacts (kubectl get pvc -l
// nvsnap.io/source-engine=vllm). Returns "" if no known marker is present.
//
// Markers are intentionally loose substring matches — operators retag
// images and we want the label to follow the family even when the
// registry path differs.
func classifyEngine(image string) string {
	if image == "" {
		return ""
	}
	lc := image
	for i := 0; i < len(lc); i++ {
		c := lc[i]
		if c >= 'A' && c <= 'Z' {
			lc = lc[:i] + string(c+'a'-'A') + lc[i+1:]
		}
	}
	switch {
	case containsAny(lc, "vllm"):
		return "vllm"
	case containsAny(lc, "sglang"):
		return "sglang"
	case containsAny(lc, "trtllm", "tensorrt-llm", "tensorrt_llm"):
		return "trtllm"
	case containsAny(lc, "/nim/", "nim-"):
		return "nim"
	default:
		return ""
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func (c *Capturer) logger() logrus.FieldLogger {
	if c.Log != nil {
		return c.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "rootfsonly")
}

// readEntryArgv reads the source process's argv from
// <procRoot>/<pid>/cmdline, which the kernel stores as NUL-separated,
// NUL-terminated args. Best-effort: returns nil on any read error or
// empty cmdline (kernel threads, races), in which case the restore
// webhook falls back to the restored pod's explicit command/args.
func readEntryArgv(procRoot string, pid int) []string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}
	var argv []string
	for _, p := range strings.Split(string(data), "\x00") {
		if p != "" {
			argv = append(argv, p)
		}
	}
	return argv
}

// collectCacheEnv returns the main container's env vars whose value lives
// under cacheDir — i.e. the cache/model paths the engine actually ran with.
// Stamped into the manifest (cachedir mode) as the per-checkpoint single
// source of truth so the restore webhook replays them verbatim; this keeps
// capture↔restore path consistency (and thus JIT/compile cache reuse)
// regardless of any later edit to the cachedir-env ConfigMap. Returns nil
// when nothing matches (restore then recomputes from CacheDir).
func collectCacheEnv(spec *corev1.PodSpec, mainContainer int, cacheDir string) map[string]string {
	if spec == nil || cacheDir == "" || mainContainer < 0 || mainContainer >= len(spec.Containers) {
		return nil
	}
	prefix := strings.TrimRight(cacheDir, "/") + "/"
	out := map[string]string{}
	for _, e := range spec.Containers[mainContainer].Env {
		if e.Value == cacheDir || strings.HasPrefix(e.Value, prefix) {
			out[e.Name] = e.Value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
