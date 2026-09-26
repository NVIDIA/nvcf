// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// ModelVolumeController is the agent half of
// docs/proposals/helm-shared-model-volume.md.
//
//   - Writers (any node): when the download init named on the pod exits
//     0, the writer claim is labelled complete and, on block storage, the
//     read-only claim is minted in the writer's namespace.
//   - Pending readers on this node (block storage): once the identity is
//     complete, the read-only claim is minted in the reader's namespace,
//     attached to this node through a mount-holder, bind-mounted read-only
//     onto the reader's hostPath landing (under the Bidirectional overlays
//     root, so kubelet's mount sees it), and the pod is un-pended. The
//     writer touched the marker inside the volume, so the reader's wait
//     init sees it as soon as the bind lands.
//
// Every agent watches writers; marking and minting are idempotent, so the
// race between agents is harmless. Only the agent on the reader's node
// binds for it.
type ModelVolumeController struct {
	Kube        kubernetes.Interface
	Provisioner *modelvolume.Provisioner
	// Minter mints read-only claims on block storage; nil in RWX mode.
	Minter *checkpointstore.SharedVolumePromoter
	// NodeName is this agent's node; readers elsewhere are ignored.
	NodeName string
	// HostRoot is where model volumes are bound for readers: <root>/<key>.
	// Must be under the agent's Bidirectional overlays mount.
	HostRoot string
	// HolderNamespaceImage is the image for mount-holder pods (the agent image).
	HolderImage       string
	HolderPullSecrets []string
	Log               logrus.FieldLogger

	// Seams for tests: attach returns the host path a claim is mounted at
	// on this node; bind bind-mounts src onto dst read-only.
	attach func(ctx context.Context, ns, claim string) (string, error)
	bind   func(src, dst string) error

	mu      sync.Mutex
	bound   map[string]bool // dst paths already bound
	holders map[string]*checkpointstore.MountHolder
}

// Run starts the informer and blocks until ctx is done.
func (c *ModelVolumeController) Run(ctx context.Context) error {
	c.init()
	factory := informers.NewSharedInformerFactoryWithOptions(c.Kube, 30*time.Second,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = modelvolume.IdentityLabel }))
	informer := factory.Core().V1().Pods().Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.handle(ctx, obj) },
		UpdateFunc: func(_, obj any) { c.handle(ctx, obj) },
	}); err != nil {
		return fmt.Errorf("AddEventHandler: %w", err)
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("model volume informer did not sync")
	}
	c.log().WithFields(logrus.Fields{"node": c.NodeName, "mode": c.Provisioner.Cfg.Mode, "host_root": c.HostRoot}).Info("model volume controller started")
	<-ctx.Done()
	return nil
}

func (c *ModelVolumeController) init() {
	if c.bound == nil {
		c.bound = map[string]bool{}
	}
	if c.holders == nil {
		c.holders = map[string]*checkpointstore.MountHolder{}
	}
	if c.attach == nil {
		c.attach = c.attachWithHolder
	}
	if c.bind == nil {
		c.bind = bindReadOnly
	}
}

func (c *ModelVolumeController) log() logrus.FieldLogger {
	if c.Log != nil {
		return c.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "modelvolume")
}

// handle dispatches one pod event; exported for tests as Handle.
func (c *ModelVolumeController) handle(ctx context.Context, obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil {
		return
	}
	c.init()
	uri := pod.Annotations[modelvolume.IdentityAnnotation]
	if uri == "" {
		return
	}
	switch pod.Labels[modelvolume.RoleLabel] {
	case "writer":
		c.handleWriter(ctx, pod, uri)
	case "reader":
		if pod.Labels[modelvolume.PendingLabel] == "true" && pod.Spec.NodeName == c.NodeName {
			c.handlePendingReader(ctx, pod, uri)
		}
	}
}

// Handle is the test entry point for one pod event.
func (c *ModelVolumeController) Handle(ctx context.Context, pod *corev1.Pod) { c.handle(ctx, pod) }

func (c *ModelVolumeController) handleWriter(ctx context.Context, pod *corev1.Pod, uri string) {
	initName := pod.Annotations[modelvolume.DownloadInitAnnotation]
	if initName == "" || !initExitedZero(pod, initName) {
		return
	}
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri})
	if err := c.Provisioner.MarkComplete(ctx, uri, pod.Namespace); err != nil {
		log.WithError(err).Warn("model volume: mark complete failed")
		return
	}
	if c.Provisioner.Cfg.Mode == modelvolume.ModeBlock && c.Minter != nil {
		if err := c.Minter.MintReadOnly(ctx, pod.Namespace, modelvolume.ClaimName(uri), modelvolume.ReadOnlyPVName(uri, pod.Namespace), modelvolume.ReadOnlyClaimName(uri), pod.Namespace, modelvolume.Key(uri)); err != nil {
			log.WithError(err).Warn("model volume: mint read-only claim failed")
			return
		}
	}
	log.Info("model volume: download complete; readers may attach")
}

func initExitedZero(pod *corev1.Pod, name string) bool {
	for i := range pod.Status.InitContainerStatuses {
		s := &pod.Status.InitContainerStatuses[i]
		if s.Name == name {
			return s.State.Terminated != nil && s.State.Terminated.ExitCode == 0
		}
	}
	return false
}

func (c *ModelVolumeController) handlePendingReader(ctx context.Context, pod *corev1.Pod, uri string) {
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri, "node": c.NodeName})
	st, err := c.Provisioner.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("model volume: lookup failed")
		return
	}
	if !st.Complete {
		return // the wait init keeps waiting; the writer's completion re-triggers via its own event
	}
	dst := filepath.Join(c.HostRoot, modelvolume.Key(uri))
	c.mu.Lock()
	already := c.bound[dst]
	c.mu.Unlock()
	if !already {
		if c.Minter != nil {
			if err := c.Minter.MintReadOnly(ctx, st.ClaimNamespace, modelvolume.ClaimName(uri), modelvolume.ReadOnlyPVName(uri, pod.Namespace), modelvolume.ReadOnlyClaimName(uri), pod.Namespace, modelvolume.Key(uri)); err != nil {
				log.WithError(err).Warn("model volume: mint read-only claim in reader namespace failed")
				return
			}
		}
		src, err := c.attach(ctx, pod.Namespace, modelvolume.ReadOnlyClaimName(uri))
		if err != nil {
			log.WithError(err).Warn("model volume: attach read-only claim to node failed")
			return
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			log.WithError(err).Warn("model volume: create bind target failed")
			return
		}
		if err := c.bind(src, dst); err != nil {
			log.WithError(err).Warn("model volume: bind failed")
			return
		}
		c.mu.Lock()
		c.bound[dst] = true
		c.mu.Unlock()
		log.WithFields(logrus.Fields{"src": src, "dst": dst}).Info("model volume: bound read-only volume for reader")
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]string{modelvolume.PendingLabel: "false"}}})
	if _, err := c.Kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		log.WithError(err).Warn("model volume: un-pend reader failed")
	}
}

// attachWithHolder mounts the claim on this node through a mount-holder pod
// and returns the agent-visible path of the mounted volume. The holder
// stays for the life of the agent; the volume is read-only and shared.
func (c *ModelVolumeController) attachWithHolder(ctx context.Context, ns, claim string) (string, error) {
	key := ns + "/" + claim
	c.mu.Lock()
	h := c.holders[key]
	c.mu.Unlock()
	if h == nil {
		pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get claim %s: %w", key, err)
		}
		entry, _ := c.log().(*logrus.Entry)
		if entry == nil {
			entry = logrus.NewEntry(logrus.New())
		}
		name := "nvsnap-model-holder-" + strings.TrimPrefix(claim, "nvsnap-model-") + "-" + shortNode(c.NodeName)
		h = checkpointstore.NewMountHolder(c.Kube, entry, ns, name, c.NodeName, claim, pvc.UID, c.HolderImage, "/host", c.HolderPullSecrets)
		if err := h.Create(ctx); err != nil {
			return "", fmt.Errorf("create mount-holder: %w", err)
		}
		if err := h.WaitRunning(ctx); err != nil {
			return "", fmt.Errorf("mount-holder not running: %w", err)
		}
		c.mu.Lock()
		c.holders[key] = h
		c.mu.Unlock()
	}
	return h.PVMountPath()
}

func shortNode(n string) string {
	n = strings.SplitN(n, ".", 2)[0]
	if len(n) > 20 {
		n = n[len(n)-20:]
	}
	return n
}

// bindReadOnly bind-mounts src onto dst and remounts it read-only. dst is
// under the agent's Bidirectional overlays root, so the mount propagates
// to the host and into pods whose mounts propagate HostToContainer.
func bindReadOnly(src, dst string) error {
	if err := syscall.Mount(src, dst, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s -> %s: %w", src, dst, err)
	}
	if err := syscall.Mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("remount %s read-only: %w", dst, err)
	}
	return nil
}
