// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// An engine may declare its own snapshot point: Dynamo's workers do when
// DYN_SNAPSHOT_CONTROL_DIR is set. The worker loads its engine, pauses it,
// writes ready-for-snapshot in that directory and waits, before it has
// connected to anything outside the pod. On restore-complete it reads
// restore-context.json, applies the pod identity it names (namespace, pod
// name and uid, discovery and request planes), and only then creates its
// runtime and registers.
//
// A process captured after it registered cannot serve in another
// instance: its registrations live in services of the old one. So for
// such a pod the agent captures at the snapshot point instead of after
// Ready, then releases the engine with this pod's own identity; a
// restored engine is released with the identity of the pod it was
// restored into. The engine registers either way, exactly as at a cold
// start.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// The names of Dynamo's snapshot protocol (dynamo.common.snapshot).
	snapshotControlDirEnv   = webhook.SnapshotControlDirEnv
	snapshotReadyFile       = "ready-for-snapshot"
	snapshotRestoreFile     = "restore-complete"
	snapshotContextFile     = "restore-context.json"
	snapshotPointPoll       = 2 * time.Second
	snapshotPointPodRecheck = 30 * time.Second
)

// snapshotContextEnv are the variables a released engine applies: the
// ones Dynamo reads after the snapshot point. None is a secret.
var snapshotContextEnv = []string{
	"CONTAINER_NAME",
	"DYN_COMPONENT",
	"DYN_DISCOVERY_BACKEND",
	"DYN_EVENT_PLANE",
	"DYN_HEALTH_CHECK_ENABLED",
	"DYN_KUBE_DISCOVERY_MODE",
	"DYN_NAMESPACE",
	"DYN_NAMESPACE_WORKER_SUFFIX",
	"DYN_PARENT_DGD_K8S_NAME",
	"DYN_PARENT_DGD_K8S_NAMESPACE",
	"DYN_REQUEST_PLANE",
	"DYN_SYSTEM_HEALTH_PATH",
	"DYN_SYSTEM_HOST",
	"DYN_SYSTEM_LIVE_PATH",
	"DYN_SYSTEM_PORT",
	"DYN_SYSTEM_STARTING_HEALTH_STATUS",
	"DYN_SYSTEM_USE_ENDPOINT_HEALTH_STATUS",
	"ETCD_ENDPOINTS",
	"MODEL_EXPRESS_URL",
	"NATS_SERVER",
	"POD_NAME",
	"POD_NAMESPACE",
	"POD_UID",
	"PROMETHEUS_ENDPOINT",
}

// snapshotPointDir returns the GPU container and the snapshot-control
// directory it declares, or "" when it declares none.
func snapshotPointDir(pod *corev1.Pod) (container, dir string) {
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; !ok {
			continue
		}
		for _, e := range c.Env {
			if e.Name == snapshotControlDirEnv && e.ValueFrom == nil && filepath.IsAbs(e.Value) {
				return c.Name, filepath.Clean(e.Value)
			}
		}
		return "", ""
	}
	return "", ""
}

// considerSnapshotPoint drives the snapshot point of a pod on this node.
func (c *criuAutoRestorer) considerSnapshotPoint(ctx context.Context, pod *corev1.Pod) {
	container, dir := snapshotPointDir(pod)
	if dir == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || rootfsonly.IsPodReady(pod) {
		return
	}
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "container": container, "controlDir": dir})
	switch {
	case pod.Annotations[webhook.CRIURestoredAnnotation] != "":
		if _, done := c.attempted.LoadOrStore("point-release/"+string(pod.UID), struct{}{}); done {
			return
		}
		go func() {
			if err := c.releaseSnapshotPoint(ctx, pod, container, dir); err != nil {
				log.WithError(err).Error("snapshot point: cannot release the restored engine; it stays paused")
				c.attempted.Delete("point-release/" + string(pod.UID))
				return
			}
			log.Info("snapshot point: restored engine released with this pod's identity")
		}()
	case pod.Annotations[webhook.CRIURestoreAnnotation] != "":
		// A placeholder: released once its restore has run.
	default:
		if _, busy := c.attempted.LoadOrStore("point/"+string(pod.UID), struct{}{}); busy {
			return
		}
		go func() {
			defer c.attempted.Delete("point/" + string(pod.UID))
			c.atSnapshotPoint(ctx, pod, container, dir, log)
		}()
	}
}

// atSnapshotPoint waits for the engine to reach its snapshot point,
// captures the instance there when it is to be captured, and releases the
// engine whatever the capture did.
func (c *criuAutoRestorer) atSnapshotPoint(ctx context.Context, pod *corev1.Pod, container, dir string, log *logrus.Entry) {
	if err := c.waitSnapshotPoint(ctx, pod, container, dir); err != nil {
		log.WithError(err).Info("snapshot point: no longer waiting")
		return
	}
	log.Info("snapshot point: engine paused at its snapshot point")
	c.captureAtSnapshotPoint(ctx, pod, log)
	if err := c.releaseSnapshotPoint(context.WithoutCancel(ctx), pod, container, dir); err != nil {
		log.WithError(err).Error("snapshot point: cannot release the engine; it stays paused")
		return
	}
	log.Info("snapshot point: engine released")
}

var errSnapshotPointGone = errors.New("the pod is gone or past its snapshot point")

// waitSnapshotPoint returns once a process of the container shows the
// ready file.
func (c *criuAutoRestorer) waitSnapshotPoint(ctx context.Context, pod *corev1.Pod, container, dir string) error {
	procBase := hostProcBase()
	recheck := time.Now().Add(snapshotPointPodRecheck)
	for {
		if info, err := c.a.runtime.FindContainerByPod(ctx, pod.Namespace, pod.Name, container); err == nil && info != nil && info.PID != 0 {
			if _, ok := snapshotPointRoot(procBase, int(info.PID), dir, true); ok {
				return nil
			}
		}
		if time.Now().After(recheck) {
			cur, err := c.a.kubeClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) || (err == nil && (cur.UID != pod.UID || cur.DeletionTimestamp != nil || rootfsonly.IsPodReady(cur))) {
				return errSnapshotPointGone
			}
			recheck = time.Now().Add(snapshotPointPodRecheck)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(snapshotPointPoll):
		}
	}
}

// captureAtSnapshotPoint captures the instance whose rank 0 is pod, the
// engine paused at its snapshot point. Other ranks hold no runtime of
// their own and are captured when Ready.
func (c *criuAutoRestorer) captureAtSnapshotPoint(ctx context.Context, pod *corev1.Pod, log *logrus.Entry) {
	uri := pod.Annotations[cacheURIAnnotation]
	o, size, isRank := podRank(pod, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation)
	if uri == "" || !isRank || o != 0 || !hasInstance(pod) {
		return
	}
	if size > 1 && !c.a.captureOptedIn(pod) {
		return
	}
	scope, err := c.a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		log.WithError(err).Warn("snapshot point: cannot read the namespace's scope; not capturing")
		return
	}
	if size == 1 {
		if prev, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(criuGroupKey(uri, scope)), metav1.GetOptions{}); err == nil && !criuGroupRecaptures(prev.Data, time.Now()) {
			return
		}
	}
	deadline := time.Now().Add(criuGroupPlaceholderWait)
	for c.captureGroup(ctx, pod, scope, size, true) {
		if time.Now().After(deadline) {
			log.Warn("snapshot point: the instance's other ranks are not Ready; releasing the engine uncaptured")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// releaseSnapshotPoint writes the pod's identity and restore-complete into
// the engine's control directory.
func (c *criuAutoRestorer) releaseSnapshotPoint(ctx context.Context, pod *corev1.Pod, container, dir string) error {
	info, err := c.a.runtime.FindContainerByPod(ctx, pod.Namespace, pod.Name, container)
	if err != nil || info == nil || info.PID == 0 {
		return fmt.Errorf("find container %s: %v", container, err)
	}
	procBase := hostProcBase()
	// The container's init carries the pod's env: the engine itself on a
	// cold start, the placeholder after a restore.
	environ, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(int(info.PID)), "environ"))
	if err != nil {
		return fmt.Errorf("read the container's env: %w", err)
	}
	root, ok := snapshotPointRoot(procBase, int(info.PID), dir, false)
	if !ok {
		return fmt.Errorf("no process of the container sees %s", dir)
	}
	return writeSnapshotRelease(filepath.Join(root, dir), snapshotContext(environ))
}

// snapshotPointRoot returns the root (procBase/<pid>/root) of a process
// of the container in which dir exists, and holds the ready file when
// ready.
func snapshotPointRoot(procBase string, containerPID int, dir string, ready bool) (string, bool) {
	// The engine's view first: a restored engine may run in a mount
	// namespace of its own under the placeholder.
	tree := processTree(procBase, containerPID)
	sort.SliceStable(tree, func(i, j int) bool {
		return !isPlaceholderProc(procBase, tree[i]) && isPlaceholderProc(procBase, tree[j])
	})
	for _, p := range tree {
		root := filepath.Join(procBase, strconv.Itoa(p), "root")
		want := filepath.Join(root, dir)
		if ready {
			want = filepath.Join(want, snapshotReadyFile)
		}
		st, err := os.Stat(want)
		if err == nil && (ready || st.IsDir()) {
			return root, true
		}
	}
	return "", false
}

// snapshotContext is the restore-context document for an environ
// (NUL-separated): every name Dynamo reads, null when unset.
func snapshotContext(environ []byte) []byte {
	have := map[string]string{}
	for _, kv := range strings.Split(string(environ), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			have[k] = v
		}
	}
	env := make(map[string]*string, len(snapshotContextEnv))
	names := append([]string(nil), snapshotContextEnv...)
	sort.Strings(names)
	for _, n := range names {
		if v, ok := have[n]; ok {
			env[n] = &v
		} else {
			env[n] = nil
		}
	}
	out, _ := json.Marshal(map[string]any{"env": env})
	return append(out, '\n')
}

// writeSnapshotRelease writes the context, then the sentinel the engine
// waits on, each atomically and owned like the directory, so the engine
// can remove them.
func writeSnapshotRelease(dir string, context []byte) error {
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	uid, gid := -1, -1
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		uid, gid = int(s.Uid), int(s.Gid)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{snapshotContextFile, context}, {snapshotRestoreFile, []byte("restore")}} {
		tmp := filepath.Join(dir, "."+f.name+".nvsnap")
		if err := os.WriteFile(tmp, f.data, 0o644); err != nil {
			return err
		}
		_ = os.Lchown(tmp, uid, gid)
		if err := os.Rename(tmp, filepath.Join(dir, f.name)); err != nil {
			return err
		}
	}
	return nil
}

func isPlaceholderProc(procBase string, pid int) bool {
	comm, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "comm"))
	return err == nil && placeholderComms[strings.TrimSpace(string(comm))]
}

func hostProcBase() string {
	if _, err := os.Stat("/host/proc"); err == nil {
		return "/host/proc"
	}
	return "/proc"
}
