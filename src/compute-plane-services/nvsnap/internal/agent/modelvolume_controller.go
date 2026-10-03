// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tarstream"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
	"github.com/gorilla/mux"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

// ModelVolumeController is the agent half of
// docs/proposals/helm-shared-model-volume.md.
//
//   - Download Jobs, RWX (any node): when the Job for an identity
//     succeeds, the shared claim is labelled complete.
//   - Staging pods, block storage (the pod's node): the Job's download
//     init wrote into a pod-local emptyDir and a hold container keeps the
//     pod Running (kubelet removes the emptyDir the moment a pod
//     terminates). This agent measures the bytes on disk, creates a claim
//     of that size in the nvsnap namespace, attaches it through a
//     mount-holder, copies the tree in, labels the retained PV complete,
//     releases the claim so the volume detaches, and deletes the Job. The
//     same download-then-copy shape as the cachedir capture: no size is
//     guessed, no expansion is assumed, any downloader works. After
//     StagingAttempts failures it gives up: records the failure so new
//     admissions keep their own download and releases the readers pending
//     on the claim, so nothing ever waits forever.
//   - Pending readers on this node (block storage): once the identity is
//     complete, the read-only claim is minted in the reader's namespace,
//     attached to this node through a mount-holder, bind-mounted read-only
//     onto the reader's hostPath landing (under the Bidirectional overlays
//     root, so kubelet's mount sees it), and the pod is un-pended. The Job
//     touched the marker inside the volume, so the reader's wait init sees
//     it as soon as the bind lands.
//
// Every agent watches Jobs; marking and minting are idempotent, so the
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
	// Its own Bidirectional hostPath (not the overlays root, whose sweeper
	// removes entries it does not own).
	HostRoot string
	// HolderNamespaceImage is the image for mount-holder pods (the agent image).
	HolderImage       string
	HolderPullSecrets []string
	Log               logrus.FieldLogger
	// Copier writes a staged download into the attached claim; the
	// agent's in-process tree copier. Required in block mode.
	Copier checkpointstore.Copier
	// HostFSRoot is the agent's view of the node filesystem (/host);
	// KubeletPodsDir is the host path of per-pod kubelet state, where
	// emptyDirs are materialised. Defaults: /host, /var/lib/kubelet/pods.
	HostFSRoot     string
	KubeletPodsDir string
	// fetchRankFn is the seam tests use for remote ranks; nil means HTTP.
	fetchRankFn func(ctx context.Context, uri string, r rankSource, dst string) error
	// rankSizeFn measures a remote rank's tree on its node (seam for
	// tests); nil uses the peer agent's size query.
	rankSizeFn func(ctx context.Context, uri string, r rankSource) (int64, error)
	// collectAttach attaches the set claim and returns its mount path and
	// a release; nil means a mount-holder on this node.
	collectAttach func(ctx context.Context, uri, sysNS, claim string) (dst string, release func(context.Context) error, err error)

	// StagingAttempts bounds the copy retries per identity before the
	// staged Job is dropped and readers are left to their fallback.
	StagingAttempts int
	// Cache is the KindCache provisioner for compile-cache volumes
	// captured from Ready pods; nil disables.
	Cache *modelvolume.Provisioner
	// CacheWarmup is the wait after a pod is Ready before its cachedir is
	// looked at. Zero means 30 seconds.
	CacheWarmup time.Duration
	// RefreshDisabled turns set refresh off; RefreshCooldown is the least
	// time between a generation's creation and the next refresh of the
	// same set (zero: 6 hours). docs/proposals/helm-chart-cache-refresh.md.
	RefreshDisabled bool
	RefreshCooldown time.Duration
	// deltaScan is the warm-rank scan (seam for tests): new files not in
	// the seed index, their bytes, a fingerprint, and whether an index was
	// found at all.
	// Files written after `before` (the pod's Ready transition) are what
	// serving compiled, not what the next start pays for; they are left out.
	deltaScan func(src string, before time.Time) (files int, bytes int64, fingerprint string, indexed bool, err error)
	// CacheSettle is how long the cache tree must stay unchanged (bytes
	// and file count) before it is captured. Readiness is not "compiled":
	// a tensor-parallel worker reports Ready before its torch.compile
	// finishes (748-byte capture on dev1 2026-09-29) and some engines
	// compile lazily. Zero means 20 seconds.
	CacheSettle time.Duration

	// Seams for tests: attach returns the host path a claim is mounted at
	// on this node; bind bind-mounts src onto dst read-only; unbind undoes
	// it; mountedDevice reports the block device mounted at a path ("" when
	// nothing is mounted there).
	attach        func(ctx context.Context, ns, claim string) (string, error)
	bind          func(src, dst string) error
	unbind        func(dst string) error
	mountedDevice func(dst string) string
	// stageSize measures a staged tree (agent-visible path); copyStaging
	// copies host path src into claim ns/name and returns when the
	// volume is detached again.
	stageSize   func(path string) (int64, error)
	treeStat    func(path string) (bytes, files int64, err error)
	copyStaging func(ctx context.Context, ns, claim, src string) error

	mu       sync.Mutex
	holders  map[string]*checkpointstore.MountHolder
	inflight map[string]bool
	attempts map[string]int
}

// stagingBindTimeout bounds the wait for a freshly created primary
// claim to bind; stagingDetachTimeout the wait for the holder's
// attachment to go away after the copy.
const (
	stagingBindTimeout   = 3 * time.Minute
	stagingDetachTimeout = 2 * time.Minute
)

// Run starts the informer and blocks until ctx is done.
func (c *ModelVolumeController) Run(ctx context.Context) error {
	c.init()
	factory := informers.NewSharedInformerFactoryWithOptions(c.Kube, 30*time.Second,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = modelvolume.IdentityLabel }))
	pods := factory.Core().V1().Pods().Informer()
	if _, err := pods.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.handle(ctx, obj) },
		UpdateFunc: func(_, obj any) { c.handle(ctx, obj) },
	}); err != nil {
		return fmt.Errorf("AddEventHandler pods: %w", err)
	}
	jobs := factory.Batch().V1().Jobs().Informer()
	if _, err := jobs.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.handleJob(ctx, obj) },
		UpdateFunc: func(_, obj any) { c.handleJob(ctx, obj) },
	}); err != nil {
		return fmt.Errorf("AddEventHandler jobs: %w", err)
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), pods.HasSynced, jobs.HasSynced) {
		return fmt.Errorf("model volume informers did not sync")
	}
	c.log().WithFields(logrus.Fields{"node": c.NodeName, "mode": c.Provisioner.Cfg.Mode, "host_root": c.HostRoot}).Info("model volume controller started")
	<-ctx.Done()
	return nil
}

func (c *ModelVolumeController) init() {
	if c.holders == nil {
		c.holders = map[string]*checkpointstore.MountHolder{}
	}
	if c.attach == nil {
		c.attach = c.attachWithHolder
	}
	if c.bind == nil {
		c.bind = bindReadOnly
	}
	if c.unbind == nil {
		c.unbind = func(dst string) error { return syscall.Unmount(dst, syscall.MNT_DETACH) }
	}
	if c.mountedDevice == nil {
		c.mountedDevice = mountedDeviceAt
	}
	if c.stageSize == nil {
		c.stageSize = stagedSize
	}
	if c.treeStat == nil {
		c.treeStat = treeStatFS
	}
	if c.copyStaging == nil {
		c.copyStaging = c.copyThroughHolder
	}
	if c.inflight == nil {
		c.inflight = map[string]bool{}
	}
	if c.attempts == nil {
		c.attempts = map[string]int{}
	}
	if c.HostFSRoot == "" {
		c.HostFSRoot = "/host"
	}
	if c.KubeletPodsDir == "" {
		c.KubeletPodsDir = "/var/lib/kubelet/pods"
	}
	if c.StagingAttempts == 0 {
		c.StagingAttempts = 3
	}
}

func (c *ModelVolumeController) log() logrus.FieldLogger {
	if c.Log != nil {
		return c.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "modelvolume")
}

// handle dispatches one pod event: pending readers on this node.
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
	if staging := pod.Annotations[modelvolume.StagingAnnotation]; staging != "" {
		// A Block-mode staging pod: its download init finished and the
		// hold container keeps the emptyDir alive for this node's agent.
		if pod.Spec.NodeName != c.NodeName {
			return
		}
		log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri, "node": c.NodeName})
		if reason, failed := modelvolume.StagingFailed(pod, c.StagingAttempts); failed {
			// The download itself cannot start or finish here (for
			// example an amd64-only engine image on an arm64 node). Do
			// not sit out the Job's deadline: give up now so the readers
			// are released and the pod's owner sees the real error.
			c.giveUpOnce(ctx, uri, pod.Namespace, reason, log.WithField("reason", reason))
			return
		}
		if modelvolume.StagingReady(pod) {
			c.promoteStaging(ctx, pod, uri, staging, log)
		}
		return
	}
	if pod.Labels[modelvolume.CaptureLabel] == "true" {
		// The engine downloaded the model itself into a pod volume; once
		// the pod is Ready the tree is final and this node's agent copies
		// it into the primary. Not a reader: it never waits on a claim.
		if pod.Spec.NodeName == c.NodeName && rootfsonly.IsPodReady(pod) && pod.DeletionTimestamp == nil {
			c.captureModel(ctx, pod, uri)
		}
		return
	}
	if pod.Labels[cacheCaptureLabel] != "true" && pod.Labels[modelvolume.CacheKeyLabel] != "" && pod.Annotations[cacheURIAnnotation] != "" {
		// A warm rank seeded from a complete set. Once Ready and settled
		// its cachedir is compared with the seed index; a rank that gained
		// files proposes a refresh and every agent runs that election.
		curi := pod.Annotations[cacheURIAnnotation]
		switch {
		case pod.Annotations[modelvolume.CacheRankDeltaAnnotation] == "true":
			c.tryRefresh(ctx, curi, c.log().WithFields(logrus.Fields{"cache": curi, "node": c.NodeName}))
		case pod.Spec.NodeName == c.NodeName && rootfsonly.IsPodReady(pod) && pod.DeletionTimestamp == nil && pod.Annotations[modelvolume.CacheRankReadyAnnotation] != "true":
			c.scanWarmRank(ctx, pod)
		}
	}
	if pod.Labels[cacheCaptureLabel] == "true" && pod.Annotations[cacheURIAnnotation] != "" {
		switch {
		case pod.Annotations[modelvolume.CacheRankReadyAnnotation] == "true":
			// A rank became ready somewhere in the cluster: every agent
			// re-runs the election; atomic claim creation picks one
			// collector, the rest see AlreadyExists.
			c.tryCollect(ctx, pod.Annotations[cacheURIAnnotation], c.log().WithFields(logrus.Fields{"cache": pod.Annotations[cacheURIAnnotation], "node": c.NodeName}))
		case pod.Spec.NodeName == c.NodeName && rootfsonly.IsPodReady(pod) && pod.DeletionTimestamp == nil:
			c.captureCache(ctx, pod)
		}
	}
	if pod.Labels[modelvolume.RoleLabel] == "writer" {
		// A download Job's pod on storage that is shared while written: its
		// landing claim is the writer view, minted here once the primary is
		// bound (the webhook mints it at admission when the primary already
		// was; a brand-new filesystem can take longer than admission allows).
		if c.Provisioner.Cfg.SharedWhileWriting() && pod.DeletionTimestamp == nil {
			c.ensureWriterView(ctx, pod, uri)
		}
		return
	}
	if pod.Labels[modelvolume.RoleLabel] != "reader" {
		return
	}
	if pod.Labels[modelvolume.PendingLabel] != "true" {
		return
	}
	// hostPath readers are served by the agent on their node (it does the
	// bind); PVC readers may be unscheduled, so every agent serves them and
	// the idempotent mint converges.
	if c.Provisioner.Cfg.ReaderMode() == modelvolume.ReaderPVC || pod.Spec.NodeName == c.NodeName {
		c.handlePendingReader(ctx, pod, uri)
	}
}

// Handle is the test entry point for one pod event.
func (c *ModelVolumeController) Handle(ctx context.Context, pod *corev1.Pod) { c.handle(ctx, pod) }

// HandleJob is the test entry point for one Job event.
func (c *ModelVolumeController) HandleJob(ctx context.Context, job *batchv1.Job) {
	c.handleJob(ctx, job)
}

// handleJob completes the identity when its download Job succeeded.
func (c *ModelVolumeController) handleJob(ctx context.Context, obj any) {
	job, ok := obj.(*batchv1.Job)
	if !ok || job == nil {
		return
	}
	c.init()
	uri := job.Annotations[modelvolume.IdentityAnnotation]
	if uri == "" || job.Status.Succeeded == 0 {
		return
	}
	log := c.log().WithFields(logrus.Fields{"job": job.Namespace + "/" + job.Name, "model": uri})
	if job.Annotations[modelvolume.StagingAnnotation] != "" {
		return // block storage: the staging pod event drives the copy
	}
	// The Job wrote through its namespace's read-write view into the
	// primary in the nvsnap namespace. The agent on the Job's node reads
	// the byte count the writer left in the marker and records it on the
	// primary, so views carry the real size; then every agent completes
	// the primary (idempotent) and retires the writer view.
	sysNS := c.Provisioner.Cfg.SystemNamespace()
	c.recordJobBytes(ctx, job, uri, log)
	if err := c.Provisioner.MarkComplete(ctx, uri, sysNS); err != nil {
		log.WithError(err).Warn("model volume: mark complete failed")
		return
	}
	if err := c.Provisioner.ReleaseWriterView(ctx, uri, job.Namespace); err != nil {
		log.WithError(err).Warn("model volume: release writer view failed; the reaper retires it with the namespace")
	}
	log.Info("model volume: download complete; readers read the primary through their views")
}

// recordJobBytes stamps the tree size a download Job measured onto the
// primary PV. The writer prints the count into its termination message
// (kubelet unmounts a finished pod's volumes at once, so the volume itself
// cannot be read afterwards), which any agent reads from the pod status.
// A Job whose pod is already gone leaves the views at the primary's
// capacity.
func (c *ModelVolumeController) recordJobBytes(ctx context.Context, job *batchv1.Job, uri string, log logrus.FieldLogger) {
	pods, err := c.Kube.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + job.Name})
	if err != nil || len(pods.Items) == 0 {
		return
	}
	var bytes int64
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name != modelvolume.DownloadContainer || cs.State.Terminated == nil || cs.State.Terminated.ExitCode != 0 {
				continue
			}
			if n := modelvolume.MarkerBytes([]byte(cs.State.Terminated.Message)); n > 0 {
				bytes = n
			}
		}
	}
	if bytes == 0 {
		log.Info("model volume: the download left no byte count; views keep the primary's capacity")
		return
	}
	pv, err := c.Provisioner.PrimaryPVName(ctx, uri)
	if err != nil || pv == "" {
		log.WithError(err).Warn("model volume: primary volume unknown; bytes not recorded")
		return
	}
	c.recordPrimaryBytes(ctx, pv, bytes)
	log.WithFields(logrus.Fields{"bytes": bytes, "pv": pv}).Info("model volume: tree size recorded on the primary")
}

// promoteStaging turns a staging pod on this node into the completed
// primary PV. Idempotent and retried on every pod event until the
// identity is complete or StagingAttempts is spent.
func (c *ModelVolumeController) promoteStaging(ctx context.Context, pod *corev1.Pod, uri, staging string, log logrus.FieldLogger) {
	st, err := c.Provisioner.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("model volume: lookup failed")
		return
	}
	if st.Complete {
		// Another attempt finished it; the staged bytes are surplus.
		if derr := c.Provisioner.DeleteJob(ctx, uri, pod.Namespace); derr != nil {
			log.WithError(derr).Warn("model volume: delete surplus staging job failed")
		}
		return
	}
	c.mu.Lock()
	if c.inflight[uri] || c.attempts[uri] >= c.StagingAttempts {
		c.mu.Unlock()
		return
	}
	c.inflight[uri] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, uri)
			c.mu.Unlock()
		}()
		src := filepath.Join(c.KubeletPodsDir, string(pod.UID), "volumes", "kubernetes.io~empty-dir", staging)
		if cerr := c.copyStaged(ctx, uri, src, log); cerr != nil {
			c.mu.Lock()
			c.attempts[uri]++
			n := c.attempts[uri]
			c.mu.Unlock()
			log.WithError(cerr).WithField("attempt", n).Warn("model volume: staging copy failed")
			if n >= c.StagingAttempts {
				c.giveUp(ctx, uri, pod.Namespace, cerr.Error(), log)
			}
			return
		}
		c.mu.Lock()
		delete(c.attempts, uri)
		c.mu.Unlock()
		if err := c.Provisioner.DeleteJob(ctx, uri, pod.Namespace); err != nil {
			log.WithError(err).Warn("model volume: delete staging job after copy failed")
		}
	}()
}

// giveUpOnce runs giveUp for a failing staging pod once per identity;
// the pod keeps producing events while its Job is being deleted.
func (c *ModelVolumeController) giveUpOnce(ctx context.Context, uri, jobNS, reason string, log logrus.FieldLogger) {
	c.mu.Lock()
	if c.inflight[uri] {
		c.mu.Unlock()
		return
	}
	c.inflight[uri] = true
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); delete(c.inflight, uri); c.mu.Unlock() }()
		log.Error("model volume: download cannot start on this node; giving up early")
		c.giveUp(ctx, uri, jobNS, reason, log)
	}()
}

// giveUp is the fallback when the copy cannot be made to work: record the
// failure so new admissions keep their own download, drop the staging
// Job and the sized claim, and release the readers pending on a claim
// that will not come, so their controllers recreate them and they are
// admitted on their own path. Never a deadlock.
func (c *ModelVolumeController) giveUp(ctx context.Context, uri, jobNS, reason string, log logrus.FieldLogger) {
	log.Error("model volume: staging copy gave up; readers are released to their own download")
	if err := c.Provisioner.RecordFailure(ctx, uri, reason); err != nil {
		log.WithError(err).Warn("model volume: record failure failed")
	}
	sysNS := c.Provisioner.Cfg.SystemNamespace()
	if err := c.Kube.CoreV1().PersistentVolumeClaims(sysNS).Delete(ctx, modelvolume.ClaimName(uri), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Warn("model volume: delete failed primary claim failed")
	}
	if err := c.Provisioner.DeleteJob(ctx, uri, jobNS); err != nil {
		log.WithError(err).Warn("model volume: delete staging job failed")
	}
	sel := modelvolume.IdentityLabel + "=" + modelvolume.Key(uri) + "," + modelvolume.RoleLabel + "=reader," + modelvolume.PendingLabel + "=true"
	pods, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		log.WithError(err).Warn("model volume: list pending readers failed")
		return
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if err := c.Kube.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("reader", p.Namespace+"/"+p.Name).Warn("model volume: release pending reader failed")
			continue
		}
		log.WithField("reader", p.Namespace+"/"+p.Name).Info("model volume: released pending reader to its own download")
	}
	c.mu.Lock()
	delete(c.attempts, uri)
	c.mu.Unlock()
}

// captureModel turns a Ready pod's engine-downloaded landing volume into
// the completed primary. The pod keeps serving from its own copy; the
// volume outlives the copy because the pod does. Atomic claim creation
// decides which pod, of all those downloading the same model across the
// cluster, is the source; the rest keep their downloads and nothing more
// happens to them.
func (c *ModelVolumeController) captureModel(ctx context.Context, pod *corev1.Pod, uri string) {
	vol := pod.Annotations[modelvolume.CaptureVolumeAnnotation]
	if vol == "" {
		return
	}
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri, "node": c.NodeName, "volume": vol})
	key := "capture:" + uri
	c.mu.Lock()
	if c.inflight[key] || c.attempts[key] >= c.StagingAttempts {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		st, err := c.Provisioner.Lookup(ctx, uri)
		if err != nil {
			log.WithError(err).Warn("model volume: lookup failed")
			return
		}
		if st.Complete || st.Failed {
			return
		}
		src, err := c.podVolumeHostPath(ctx, pod, vol)
		if err != nil {
			log.WithError(err).Warn("model volume: cannot locate the engine's landing volume on this node")
			return
		}
		bytes, err := c.stageSize(filepath.Join(c.HostFSRoot, src))
		if err != nil || bytes <= 0 {
			log.WithError(err).WithField("bytes", bytes).Warn("model volume: engine landing volume is empty; nothing to capture")
			return
		}
		sysNS := c.Provisioner.Cfg.SystemNamespace()
		size := c.Provisioner.Cfg.VolumeSize(bytes)
		claim, created, err := c.Provisioner.ClaimSizedClaim(ctx, uri, sysNS, size)
		if err != nil {
			log.WithError(err).Warn("model volume: claim failed")
			return
		}
		if !created {
			return // another pod is the source for this model
		}
		log = log.WithFields(logrus.Fields{"bytes": bytes, "claim_size": size.String(), "claim": sysNS + "/" + claim})
		log.Info("model volume: primary claim sized from the engine's download; copying")
		start := time.Now()
		if err := c.copyCaptured(ctx, uri, sysNS, claim, src, bytes); err != nil {
			c.mu.Lock()
			c.attempts[key]++
			n := c.attempts[key]
			c.mu.Unlock()
			log.WithError(err).WithField("attempt", n).Warn("model volume: capture failed")
			// Release the claim so another pod can become the source; on
			// the last attempt record the failure so admissions stop
			// stamping pods for this model for a while.
			if derr := c.Kube.CoreV1().PersistentVolumeClaims(sysNS).Delete(ctx, claim, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				log.WithError(derr).Warn("model volume: delete claim after failure failed")
			}
			if n >= c.StagingAttempts {
				if rerr := c.Provisioner.RecordFailure(ctx, uri, err.Error()); rerr != nil {
					log.WithError(rerr).Warn("model volume: record failure failed")
				}
			}
			return
		}
		log.WithField("elapsed", time.Since(start).Round(time.Second).String()).Info("model volume: captured from the engine's download; later pods read it")
	}()
}

// copyCaptured waits for the claim, copies the tree in, marks it complete.
func (c *ModelVolumeController) copyCaptured(ctx context.Context, uri, sysNS, claim, src string, bytes int64) error {
	pv, err := c.Provisioner.WaitBound(ctx, sysNS, claim, stagingBindTimeout)
	if err != nil {
		return err
	}
	if err := c.copyStaging(ctx, sysNS, claim, src); err != nil {
		return err
	}
	if err := c.Provisioner.MarkComplete(ctx, uri, sysNS); err != nil {
		return err
	}
	c.recordPrimaryBytes(ctx, pv, bytes)
	return c.Provisioner.ClearFailure(ctx, uri)
}

// recordPrimaryBytes stamps the measured tree size on the primary so the
// views are sized from it whichever path filled the volume (download Job,
// staged copy, or capture from an engine download). Best effort: without
// it the views keep the primary's capacity.
func (c *ModelVolumeController) recordPrimaryBytes(ctx context.Context, pv string, bytes int64) {
	if pv == "" || bytes <= 0 {
		return
	}
	if err := c.Provisioner.RecordBytes(ctx, pv, bytes); err != nil {
		c.log().WithError(err).WithFields(logrus.Fields{"pv": pv, "bytes": bytes}).Warn("model volume: record bytes failed")
	}
}

// podVolumeHostPath is the kubelet path of a pod volume on this node: an
// emptyDir under kubernetes.io~empty-dir by volume name, a CSI-backed
// claim under kubernetes.io~csi by its PV name.
func (c *ModelVolumeController) podVolumeHostPath(ctx context.Context, pod *corev1.Pod, vol string) (string, error) {
	base := filepath.Join(c.KubeletPodsDir, string(pod.UID), "volumes")
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name != vol {
			continue
		}
		switch {
		case v.EmptyDir != nil:
			return filepath.Join(base, "kubernetes.io~empty-dir", vol), nil
		case v.PersistentVolumeClaim != nil:
			pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, v.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			if err != nil {
				return "", fmt.Errorf("get claim %s/%s: %w", pod.Namespace, v.PersistentVolumeClaim.ClaimName, err)
			}
			if pvc.Spec.VolumeName == "" {
				return "", fmt.Errorf("claim %s/%s is not bound", pod.Namespace, pvc.Name)
			}
			return filepath.Join(base, "kubernetes.io~csi", pvc.Spec.VolumeName, "mount"), nil
		default:
			return "", fmt.Errorf("volume %s is neither an emptyDir nor a claim", vol)
		}
	}
	return "", fmt.Errorf("volume %s not in pod", vol)
}

// Labels and annotations the webhook stamps on a pod whose cachedir is to
// be captured (mirrors internal/webhook/cache_volume.go).
const (
	cacheCaptureLabel      = "nvsnap.io/cache-capture"
	cacheURIAnnotation     = "nvsnap.io/cache-uri"
	cacheVolumeAnnotation  = "nvsnap.io/cache-volume"
	cacheSubpathAnnotation = "nvsnap.io/cache-subpath"
)

// captureCache is the cache-set flow for a Ready pod stamped for capture.
// The pod's rank is not copied anywhere on its own: once its tree has
// settled the agent records rank-ready and the byte count on the pod, and
// whichever agent sees the whole group ready wins the election (atomic
// claim creation) and collects every rank into one volume, its own from
// the local emptyDir and the others streamed from their agents. One
// attach, one volume per configuration.
func (c *ModelVolumeController) captureCache(ctx context.Context, pod *corev1.Pod) {
	if c.Cache == nil {
		return
	}
	uri := pod.Annotations[cacheURIAnnotation]
	vol := pod.Annotations[cacheVolumeAnnotation]
	sub := pod.Annotations[cacheSubpathAnnotation]
	if vol == "" {
		return
	}
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "cache": uri, "ordinal": pod.Annotations[modelvolume.CacheOrdinalAnnotation], "node": c.NodeName})
	key := "rank:" + string(pod.UID)
	c.mu.Lock()
	if c.inflight[key] {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		warm := c.CacheWarmup
		if warm == 0 {
			warm = 30 * time.Second
		}
		select {
		case <-time.After(warm):
		case <-ctx.Done():
			return
		}
		st, err := c.Cache.Lookup(ctx, uri)
		if err != nil {
			log.WithError(err).Warn("cache volume: lookup failed")
			return
		}
		if st.Complete || st.Failed {
			return
		}
		src := filepath.Join(c.KubeletPodsDir, string(pod.UID), "volumes", "kubernetes.io~empty-dir", vol, sub)
		bytes, files, err := c.treeStat(filepath.Join(c.HostFSRoot, src))
		if err != nil || bytes <= 0 {
			log.WithError(err).WithField("bytes", bytes).Info("cache volume: nothing to capture yet")
			return
		}
		settle := c.CacheSettle
		if settle == 0 {
			settle = 20 * time.Second
		}
		select {
		case <-time.After(settle):
		case <-ctx.Done():
			return
		}
		again, filesAgain, err := c.treeStat(filepath.Join(c.HostFSRoot, src))
		if err != nil || again != bytes || filesAgain != files {
			log.WithFields(logrus.Fields{"bytes": bytes, "bytes_after": again, "files": files, "files_after": filesAgain}).Info("cache volume: tree still changing; retrying on the next event")
			return
		}
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"true",%q:%q}}}`, modelvolume.CacheRankReadyAnnotation, modelvolume.CacheRankBytesAnnotation, strconv.FormatInt(bytes, 10))
		if _, err := c.Kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			log.WithError(err).Warn("cache volume: mark rank ready failed")
			return
		}
		log.WithFields(logrus.Fields{"bytes": bytes, "files": files}).Info("cache volume: rank ready for collection")
		c.tryCollect(ctx, uri, log)
	}()
}

// rankSource is one ready rank of a group: where its tree lives.
type rankSource struct {
	ordinal int
	pod     *corev1.Pod
	bytes   int64
	src     string // host path of the cache subtree
}

// groupRanks lists the group's ready ranks, how many are ready, and the
// group size. ok is false until every ordinal 0..size-1 is ready.
func (c *ModelVolumeController) groupRanks(ctx context.Context, uri string) ([]rankSource, int, int, bool, error) {
	return c.groupRanksWhere(ctx, uri, nil)
}

// groupRanksWhere is groupRanks over the pods keep accepts (nil: all).
func (c *ModelVolumeController) groupRanksWhere(ctx context.Context, uri string, keep func(*corev1.Pod) bool) ([]rankSource, int, int, bool, error) {
	pods, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: modelvolume.CacheKeyLabel + "=" + modelvolume.Key(uri)})
	if err != nil {
		return nil, 0, 0, false, err
	}
	size := 0
	byOrdinal := map[int]rankSource{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Annotations[cacheURIAnnotation] != uri || (keep != nil && !keep(p)) {
			continue
		}
		if n, err := strconv.Atoi(p.Annotations[modelvolume.CacheGroupSizeAnnotation]); err == nil && n > size {
			size = n
		}
		if p.Annotations[modelvolume.CacheRankReadyAnnotation] != "true" {
			continue
		}
		ord, err := strconv.Atoi(p.Annotations[modelvolume.CacheOrdinalAnnotation])
		if err != nil {
			continue
		}
		bytes, _ := strconv.ParseInt(p.Annotations[modelvolume.CacheRankBytesAnnotation], 10, 64)
		if _, dup := byOrdinal[ord]; dup {
			continue // two pods of one ordinal (a rolling group); first wins
		}
		byOrdinal[ord] = rankSource{ordinal: ord, pod: p, bytes: bytes,
			src: filepath.Join(c.KubeletPodsDir, string(p.UID), "volumes", "kubernetes.io~empty-dir", p.Annotations[cacheVolumeAnnotation], p.Annotations[cacheSubpathAnnotation])}
	}
	if size == 0 {
		size = 1
	}
	ranks := make([]rankSource, 0, size)
	for o := 0; o < size; o++ {
		r, ok := byOrdinal[o]
		if !ok {
			return nil, len(byOrdinal), size, false, nil
		}
		ranks = append(ranks, r)
	}
	return ranks, len(byOrdinal), size, true, nil
}

// tryCollect runs the election for the set and, when this agent wins,
// collects every rank into the new volume.
func (c *ModelVolumeController) tryCollect(ctx context.Context, uri string, log logrus.FieldLogger) {
	key := "set:" + uri
	c.mu.Lock()
	if c.inflight[key] || c.attempts[key] >= c.StagingAttempts {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		st, err := c.Cache.Lookup(ctx, uri)
		if err != nil || st.Complete || st.Failed {
			return
		}
		ranks, ready, size, ok, err := c.groupRanks(ctx, uri)
		if err != nil {
			log.WithError(err).Warn("cache volume: list group failed")
			return
		}
		if !ok {
			log.WithFields(logrus.Fields{"ready": ready, "group_size": size}).Info("cache volume: waiting for the rest of the group")
			return
		}
		total := c.measureRanks(ctx, uri, ranks, log)
		sysNS := c.Cache.Cfg.SystemNamespace()
		claim, created, err := c.Cache.ClaimSizedClaim(ctx, uri, sysNS, c.Cache.Cfg.VolumeSize(total))
		if err != nil {
			log.WithError(err).Warn("cache volume: claim failed")
			return
		}
		if !created {
			return // another agent collects this set
		}
		start := time.Now()
		if err := c.collectSet(ctx, uri, sysNS, claim, ranks, nil, log); err != nil {
			c.mu.Lock()
			c.attempts[key]++
			n := c.attempts[key]
			c.mu.Unlock()
			log.WithError(err).WithField("attempt", n).Warn("cache volume: collection failed")
			if derr := c.Kube.CoreV1().PersistentVolumeClaims(sysNS).Delete(ctx, claim, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				log.WithError(derr).Warn("cache volume: delete claim after failure failed")
			}
			if n >= c.StagingAttempts {
				if rerr := c.Cache.RecordFailure(ctx, uri, err.Error()); rerr != nil {
					log.WithError(rerr).Warn("cache volume: record failure failed")
				}
			}
			return
		}
		log.WithFields(logrus.Fields{"ranks": len(ranks), "bytes": total, "claim": sysNS + "/" + claim, "elapsed": time.Since(start).Round(time.Second).String()}).Info("cache volume: set collected; later pods of this configuration seed from it")
	}()
}

// collectSet attaches the set claim once and fills /<ordinal>/ for every
// rank: local ranks through the copier, remote ranks streamed from the
// agent on their node. Then complete and release.
func (c *ModelVolumeController) collectSet(ctx context.Context, uri, sysNS, claim string, ranks []rankSource, meta map[string]string, log logrus.FieldLogger) error {
	if _, err := c.Cache.WaitBound(ctx, sysNS, claim, stagingBindTimeout); err != nil {
		return err
	}
	attach := c.collectAttach
	if attach == nil {
		attach = c.holderAttach
	}
	dst, release, err := attach(ctx, uri, sysNS, claim)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			cleanup, cancel := context.WithTimeout(context.Background(), checkpointstore.MountHolderDeleteTimeout+5*time.Second)
			defer cancel()
			_ = release(cleanup)
		}
	}()
	for _, r := range ranks {
		rankDst := filepath.Join(dst, strconv.Itoa(r.ordinal))
		if r.pod.Spec.NodeName == c.NodeName {
			if _, _, err := c.Copier.Copy(ctx, rankDst, []checkpointstore.CaptureSource{{Kind: checkpointstore.SourceKindRootfs, SrcPath: r.src}}); err != nil {
				return fmt.Errorf("copy rank %d: %w", r.ordinal, err)
			}
			continue
		}
		if err := c.fetchRank(ctx, uri, r, rankDst); err != nil {
			return fmt.Errorf("fetch rank %d from %s: %w", r.ordinal, r.pod.Spec.NodeName, err)
		}
	}
	if err := release(ctx); err != nil {
		return fmt.Errorf("release collect holder: %w", err)
	}
	released = true
	if err := c.Cache.MarkCompleteClaim(ctx, uri, sysNS, claim, meta); err != nil {
		return err
	}
	return c.Cache.ClearFailure(ctx, uri)
}

// holderAttach attaches the set claim through a mount-holder on this node
// and waits for the detach on release, so the volume can be attached
// read-only elsewhere afterwards.
func (c *ModelVolumeController) holderAttach(ctx context.Context, uri, sysNS, claim string) (string, func(context.Context) error, error) {
	pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(sysNS).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("get claim %s/%s: %w", sysNS, claim, err)
	}
	entry, _ := c.log().(*logrus.Entry)
	if entry == nil {
		entry = logrus.NewEntry(logrus.New())
	}
	name := "nvsnap-cache-collect-" + modelvolume.Key(uri) + strings.TrimPrefix(claim, c.Cache.Cfg.ClaimName(uri)) + "-" + shortNode(c.NodeName)
	h := checkpointstore.NewMountHolder(c.Kube, entry, sysNS, name, c.NodeName, claim, pvc.UID, c.HolderImage, c.HostFSRoot, c.HolderPullSecrets)
	if err := h.Create(ctx); err != nil {
		return "", nil, fmt.Errorf("create collect holder: %w", err)
	}
	release := func(rctx context.Context) error {
		if err := h.Delete(rctx); err != nil {
			return err
		}
		deadline := time.Now().Add(stagingDetachTimeout)
		for {
			detached, err := c.Cache.Detached(rctx, pvc.Spec.VolumeName)
			if err != nil || detached || time.Now().After(deadline) {
				return err
			}
			time.Sleep(2 * time.Second)
		}
	}
	if err := h.WaitRunning(ctx); err != nil {
		_ = release(ctx)
		return "", nil, fmt.Errorf("collect holder not running: %w", err)
	}
	dst, err := h.PVMountPath()
	if err != nil {
		_ = release(ctx)
		return "", nil, err
	}
	return dst, release, nil
}

// fetchRank streams one remote rank from the agent on its node into dst,
// with bounded retries. The peer address is that node's agent pod; the
// request carries the agent token through the shared peer transport.
func (c *ModelVolumeController) fetchRank(ctx context.Context, uri string, r rankSource, dst string) error {
	fetch := c.fetchRankFn
	if fetch == nil {
		fetch = c.fetchRankHTTP
	}
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = fetch(ctx, uri, r, dst); err == nil {
			return nil
		}
		select {
		case <-time.After(time.Duration(attempt) * 5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (c *ModelVolumeController) fetchRankHTTP(ctx context.Context, uri string, r rankSource, dst string) error {
	agents, err := c.Kube.CoreV1().Pods(c.Cache.Cfg.SystemNamespace()).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=nvsnap-agent", FieldSelector: "spec.nodeName=" + r.pod.Spec.NodeName})
	if err != nil || len(agents.Items) == 0 || agents.Items[0].Status.PodIP == "" {
		return fmt.Errorf("no agent found on node %s: %v", r.pod.Spec.NodeName, err)
	}
	u := fmt.Sprintf("http://%s:%d/v1/cache-rank/%s/%d", agents.Items[0].Status.PodIP, agentAPIPort, modelvolume.Key(uri), r.ordinal)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("peer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _, err = tarstream.Extract(resp.Body, dst, false)
	return err
}

// ServeRank answers GET /v1/cache-rank/{key}/{ordinal}: the tar of a
// ready rank whose pod runs on this node, for the collecting agent.
func (c *ModelVolumeController) ServeRank(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key, ordinal := vars["key"], vars["ordinal"]
	pods, err := c.Kube.CoreV1().Pods("").List(r.Context(), metav1.ListOptions{LabelSelector: modelvolume.CacheKeyLabel + "=" + key, FieldSelector: "spec.nodeName=" + c.NodeName})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Annotations[modelvolume.CacheOrdinalAnnotation] != ordinal || p.Annotations[modelvolume.CacheRankReadyAnnotation] != "true" {
			continue
		}
		src := filepath.Join(c.HostFSRoot, c.KubeletPodsDir, string(p.UID), "volumes", "kubernetes.io~empty-dir", p.Annotations[cacheVolumeAnnotation], p.Annotations[cacheSubpathAnnotation])
		if r.URL.Query().Get("stat") == "1" {
			// The collector sizes the set from what the ranks hold now,
			// not from the stamp made when the rank first reported Ready.
			bytes, files, err := c.treeStat(src)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int64{"bytes": bytes, "files": files})
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		if err := tarstream.Write(w, src, nil); err != nil {
			c.log().WithError(err).WithField("pod", p.Namespace+"/"+p.Name).Warn("cache volume: rank stream failed")
		}
		return
	}
	http.Error(w, "no ready rank "+ordinal+" for key "+key+" on this node", http.StatusNotFound)
}

const agentAPIPort = 8081

// copyStaged is one attempt: measure, claim, copy, complete.
func (c *ModelVolumeController) copyStaged(ctx context.Context, uri, src string, log logrus.FieldLogger) error {
	start := time.Now()
	bytes, err := c.stageSize(filepath.Join(c.HostFSRoot, src))
	if err != nil {
		return fmt.Errorf("measure staged tree: %w", err)
	}
	if bytes <= 0 {
		return fmt.Errorf("staged tree %s is empty", src)
	}
	size := c.Provisioner.Cfg.VolumeSize(bytes)
	sysNS := c.Provisioner.Cfg.SystemNamespace()
	claim, err := c.Provisioner.EnsureSizedClaim(ctx, uri, sysNS, size)
	if err != nil {
		return err
	}
	pv, err := c.Provisioner.WaitBound(ctx, sysNS, claim, stagingBindTimeout)
	if err != nil {
		return err
	}
	log = log.WithFields(logrus.Fields{"staged_bytes": bytes, "claim_size": size.String(), "claim": sysNS + "/" + claim, "pv": pv})
	log.Info("model volume: primary claim sized from the staged download; copying")
	if err := c.copyStaging(ctx, sysNS, claim, src); err != nil {
		return err
	}
	if err := c.Provisioner.MarkComplete(ctx, uri, sysNS); err != nil {
		return err
	}
	c.recordPrimaryBytes(ctx, pv, bytes)
	if err := c.Provisioner.ClearFailure(ctx, uri); err != nil {
		log.WithError(err).Warn("model volume: clear failure record failed")
	}
	log.WithField("elapsed", time.Since(start).Round(time.Second).String()).Info("model volume: download complete; readers may attach once the volume detaches")
	return nil
}

// stagedSize measures a staged tree; a missing tree is an error, not zero.
func stagedSize(path string) (int64, error) {
	b, _, err := treeStatFS(path)
	return b, err
}

// treeStatFS returns the regular-file bytes and file count under path.
func treeStatFS(path string) (bytes, files int64, err error) {
	if _, err = os.Stat(path); err != nil {
		return 0, 0, err
	}
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return bytes, files, nil
}

// copyThroughHolder attaches the claim on this node with a one-shot
// mount-holder, copies the staged tree into it, removes the holder and
// waits for the volume to detach so read-only attaches elsewhere succeed.
func (c *ModelVolumeController) copyThroughHolder(ctx context.Context, ns, claim, src string) error {
	if c.Copier == nil {
		return fmt.Errorf("no copier configured")
	}
	pvc, err := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get claim %s/%s: %w", ns, claim, err)
	}
	entry, _ := c.log().(*logrus.Entry)
	if entry == nil {
		entry = logrus.NewEntry(logrus.New())
	}
	name := "nvsnap-model-copy-" + strings.TrimPrefix(claim, "nvsnap-model-") + "-" + shortNode(c.NodeName)
	h := checkpointstore.NewMountHolder(c.Kube, entry, ns, name, c.NodeName, claim, pvc.UID, c.HolderImage, c.HostFSRoot, c.HolderPullSecrets)
	if err = h.Create(ctx); err != nil {
		return fmt.Errorf("create copy holder: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), checkpointstore.MountHolderDeleteTimeout+5*time.Second)
		defer cancel()
		if derr := h.Delete(cleanup); derr != nil {
			entry.WithError(derr).Warn("model volume: copy holder delete failed")
		}
	}()
	if err = h.WaitRunning(ctx); err != nil {
		return fmt.Errorf("copy holder not running: %w", err)
	}
	dst, err := h.PVMountPath()
	if err != nil {
		return err
	}
	if _, _, err = c.Copier.Copy(ctx, dst, []checkpointstore.CaptureSource{{Kind: checkpointstore.SourceKindRootfs, SrcPath: src, DstSubpath: ""}}); err != nil {
		return fmt.Errorf("copy staged tree: %w", err)
	}
	if err = h.Delete(ctx); err != nil {
		return fmt.Errorf("release copy holder: %w", err)
	}
	deadline := time.Now().Add(stagingDetachTimeout)
	for {
		detached, err := c.Provisioner.Detached(ctx, pvc.Spec.VolumeName)
		if err != nil {
			return err
		}
		if detached {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("primary %s still attached %s after the copy holder left", pvc.Spec.VolumeName, stagingDetachTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *ModelVolumeController) handlePendingReader(ctx context.Context, pod *corev1.Pod, uri string) {
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri, "node": c.NodeName})
	st, err := c.Provisioner.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("model volume: lookup failed")
		return
	}
	if !st.Complete {
		// On storage that is shared while written a bound primary is
		// already readable: give the reader its view now and let its wait
		// init watch for the marker. Block storage waits for completion.
		if c.Provisioner.Cfg.SharedWhileWriting() && st.InFlightPV != "" && c.Minter != nil {
			st.PrimaryPV = st.InFlightPV
			c.servePVCReader(ctx, pod, uri, st, log)
		}
		return
	}
	if c.Provisioner.Cfg.ReaderMode() == modelvolume.ReaderPVC {
		c.servePVCReader(ctx, pod, uri, st, log)
		return
	}
	dst := filepath.Join(c.HostRoot, modelvolume.Key(uri))
	// The mount table is the truth, not memory: the agent may have
	// restarted, the volume may have been replaced by a re-download of the
	// same identity, or an operator may have unmounted by hand. A bind is
	// current only if the device under dst belongs to this primary PV.
	primary, err := c.Kube.CoreV1().PersistentVolumes().Get(ctx, st.PrimaryPV, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Warn("model volume: get primary PV failed")
		return
	}
	handle := ""
	if primary.Spec.CSI != nil {
		handle = primary.Spec.CSI.VolumeHandle
	}
	if dev := c.mountedDevice(dst); dev != "" && !deviceMatchesHandle(dev, handle) {
		log.WithFields(logrus.Fields{"dst": dst, "device": dev}).Info("model volume: stale bind for a replaced volume; unbinding")
		if err := c.unbind(dst); err != nil {
			log.WithError(err).Warn("model volume: unbind stale bind failed")
			return
		}
	}
	if dev := c.mountedDevice(dst); dev == "" {
		if c.Minter != nil {
			if ready, err := c.primaryReadable(ctx, st.PrimaryPV, log); err != nil || !ready {
				return
			}
			if err := c.Minter.MintReadOnlyFromPV(ctx, st.PrimaryPV, modelvolume.ReadOnlyPVName(uri, pod.Namespace), modelvolume.ReadOnlyClaimName(uri), pod.Namespace, modelvolume.Key(uri)); err != nil {
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
		if dev := c.mountedDevice(dst); dev == "" {
			log.WithField("dst", dst).Warn("model volume: bind reported success but nothing is mounted at the target; not un-pending")
			return
		}
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

// mountedDeviceAt returns the source device of the mount at dst, or ""
// when dst is not a mount point. Reads the agent's own mount table; dst
// lives under the Bidirectional model root, so agent and host agree.
func mountedDeviceAt(dst string) string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	dst = filepath.Clean(dst)
	best := ""
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		if f[4] != dst {
			continue
		}
		// fields after the "-" separator: fstype, source, options
		for i := 6; i < len(f)-2; i++ {
			if f[i] == "-" {
				best = f[i+2]
				break
			}
		}
	}
	return best
}

// deviceMatchesHandle reports whether a mounted device belongs to the CSI
// volume behind handle. NVMesh devices are /dev/nvmesh/<csi-id> and the
// handle carries the same csi-id segment; other drivers fall back to
// "something is mounted, trust it".
func deviceMatchesHandle(device, handle string) bool {
	base := filepath.Base(device)
	if !strings.HasPrefix(base, "csi-") || handle == "" {
		return true
	}
	return strings.Contains(handle, base)
}

// servePVCReader mints the read-only claim the pending reader already
// references, once the primary is detached, and un-pends it. Kubelet
// binds the claim and starts the pod; no hostPath and no agent bind.
// ensureWriterView mints the read-write view a download Job's pod mounts,
// in the pod's namespace, once the primary claim is bound. Idempotent and
// retried on every pod event until the view exists.
func (c *ModelVolumeController) ensureWriterView(ctx context.Context, pod *corev1.Pod, uri string) {
	if c.Minter == nil {
		return
	}
	cfg := c.Provisioner.Cfg
	view := cfg.WriterViewClaimName(uri)
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, view, metav1.GetOptions{}); err == nil {
		return
	}
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "model": uri, "node": c.NodeName, "view": view})
	st, err := c.Provisioner.Lookup(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("model volume: lookup failed")
		return
	}
	pv := st.InFlightPV
	if pv == "" {
		pv = st.PrimaryPV
	}
	if pv == "" {
		return // the primary has not bound yet; the next pod event retries
	}
	if err := c.Minter.MintViewFromPVLabels(ctx, pv, cfg.WriterViewPVName(uri, pod.Namespace), view, pod.Namespace, cfg.ReadOnlyLabels(uri), false); err != nil {
		log.WithError(err).Warn("model volume: mint writer view failed")
		return
	}
	log.WithField("pv", pv).Info("model volume: writer view minted for the download job")
}

// primaryReadable reports whether a complete primary may be attached
// read-only now. Block storage refuses the read-only attach while the
// writer's read-write attachment still exists, so it waits for the detach;
// a filesystem shared while written has nothing to wait for.
func (c *ModelVolumeController) primaryReadable(ctx context.Context, pv string, log logrus.FieldLogger) (bool, error) {
	if c.Provisioner.Cfg.SharedWhileWriting() {
		return true, nil
	}
	detached, err := c.Provisioner.Detached(ctx, pv)
	if err != nil {
		log.WithError(err).Warn("model volume: detach check failed")
		return false, err
	}
	if !detached {
		log.WithField("pv", pv).Info("model volume: primary still attached; retrying after detach")
	}
	return detached, nil
}

func (c *ModelVolumeController) servePVCReader(ctx context.Context, pod *corev1.Pod, uri string, st modelvolume.State, log logrus.FieldLogger) {
	if c.Minter == nil || st.PrimaryPV == "" {
		return
	}
	if ready, err := c.primaryReadable(ctx, st.PrimaryPV, log); err != nil || !ready {
		return
	}
	if err := c.Minter.MintReadOnlyFromPV(ctx, st.PrimaryPV, modelvolume.ReadOnlyPVName(uri, pod.Namespace), modelvolume.ReadOnlyClaimName(uri), pod.Namespace, modelvolume.Key(uri)); err != nil {
		log.WithError(err).Warn("model volume: mint read-only claim in reader namespace failed")
		return
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]string{modelvolume.PendingLabel: "false"}}})
	if _, err := c.Kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		log.WithError(err).Warn("model volume: un-pend reader failed")
		return
	}
	log.WithField("claim", modelvolume.ReadOnlyClaimName(uri)).Info("model volume: read-only claim minted for reader")
}

// scanWarmRank measures a warm rank after Ready and the settle period,
// marks it rank-ready so it can be listed and streamed, and when its
// cachedir holds files the seed index did not, marks the delta and
// proposes a refresh (docs/proposals/helm-chart-cache-refresh.md).
func (c *ModelVolumeController) scanWarmRank(ctx context.Context, pod *corev1.Pod) {
	if c.RefreshDisabled {
		return
	}
	uri, vol, sub := pod.Annotations[cacheURIAnnotation], pod.Annotations[cacheVolumeAnnotation], pod.Annotations[cacheSubpathAnnotation]
	if vol == "" {
		return
	}
	log := c.log().WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "cache": uri, "ordinal": pod.Annotations[modelvolume.CacheOrdinalAnnotation], "node": c.NodeName})
	key := "warm:" + string(pod.UID)
	c.mu.Lock()
	if c.inflight[key] {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		warm := c.CacheWarmup
		if warm == 0 {
			warm = 30 * time.Second
		}
		select {
		case <-time.After(warm):
		case <-ctx.Done():
			return
		}
		src := filepath.Join(c.KubeletPodsDir, string(pod.UID), "volumes", "kubernetes.io~empty-dir", vol, sub)
		bytes, files, err := c.treeStat(filepath.Join(c.HostFSRoot, src))
		if err != nil || bytes <= 0 {
			log.WithError(err).Info("cache volume: warm rank not measurable yet")
			return
		}
		settle := c.CacheSettle
		if settle == 0 {
			settle = 20 * time.Second
		}
		select {
		case <-time.After(settle):
		case <-ctx.Done():
			return
		}
		if again, filesAgain, err := c.treeStat(filepath.Join(c.HostFSRoot, src)); err != nil || again != bytes || filesAgain != files {
			log.Info("cache volume: warm rank still changing; retrying on the next event")
			return
		}
		scan := c.deltaScan
		if scan == nil {
			scan = scanSeedDelta
		}
		// Only what the engine compiled before it reported Ready costs the
		// next start; kernels compiled while serving (new request shapes)
		// change on every rank with every traffic mix and would refresh
		// the set forever (GB300, 2026-10-01: 1 to 38 files per rank in
		// the first minutes of traffic). A short grace covers writes that
		// land as readiness flips.
		before := readyAt(pod).Add(readyGrace)
		newFiles, newBytes, fp, indexed, err := scan(filepath.Join(c.HostFSRoot, src), before)
		if err != nil {
			log.WithError(err).Warn("cache volume: warm rank delta scan failed")
			return
		}
		if !indexed {
			log.Info("cache volume: warm rank has no seed index; nothing to compare")
			return
		}
		ann := map[string]string{modelvolume.CacheRankReadyAnnotation: "true", modelvolume.CacheRankBytesAnnotation: strconv.FormatInt(bytes, 10)}
		if newFiles > 0 {
			ann[modelvolume.CacheRankDeltaAnnotation] = "true"
			ann[modelvolume.CacheRankDeltaBytesAnnotation] = strconv.FormatInt(newBytes, 10)
			ann[modelvolume.CacheRankDeltaFingerprintAnnotation] = fp
		}
		body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
		if _, err := c.Kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
			log.WithError(err).Warn("cache volume: mark warm rank failed")
			return
		}
		if newFiles == 0 {
			log.WithField("files", files).Info("cache volume: warm rank matches the set")
			return
		}
		log.WithFields(logrus.Fields{"new_files": newFiles, "new_bytes": newBytes, "fingerprint": fp}).Info("cache volume: warm rank gained files the set lacks; proposing a refresh")
		c.tryRefresh(ctx, uri, log)
	}()
}

// readyGrace is added to the Ready transition when deciding which new
// files belong to startup.
const readyGrace = 5 * time.Second

// readyAt is the pod's Ready transition time, zero when unknown.
func readyAt(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// scanSeedDelta lists the files under root that the seed index does not
// name and that were written before `before` (zero: any time), ignoring
// bookkeeping the engine rewrites on every start: lock files, logs and
// tmp directories. indexed is false when there is no index.
func scanSeedDelta(root string, before time.Time) (int, int64, string, bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, webhook.SeedIndexFile))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, "", false, nil
		}
		return 0, 0, "", false, err
	}
	seeded := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			seeded[line] = true
		}
	}
	var added []string
	var bytes int64
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || seeded[rel] || deltaIgnored(rel) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if !before.IsZero() && info.ModTime().After(before) {
			return nil // compiled while serving
		}
		bytes += info.Size()
		added = append(added, rel)
		return nil
	})
	if err != nil {
		return 0, 0, "", true, err
	}
	sort.Strings(added)
	sum := sha256.Sum256([]byte(strings.Join(added, "\n")))
	return len(added), bytes, hex.EncodeToString(sum[:8]), true, nil
}

// deltaIgnored names the per-start bookkeeping a delta scan skips.
func deltaIgnored(rel string) bool {
	base := filepath.Base(rel)
	if base == webhook.SeedIndexFile || strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".log") {
		return true
	}
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if part == "locks" || part == "tmp" {
			return true
		}
	}
	return false
}

// tryRefresh runs the refresh election for a complete set: when a rank
// reports a delta, every rank of the group is listed and ready, the
// cooldown since the serving generation has passed and the same delta
// has not been collected before, one agent collects a new generation from
// the warm ranks. Readers of the old generation are unaffected.
func (c *ModelVolumeController) tryRefresh(ctx context.Context, uri string, log logrus.FieldLogger) {
	if c.RefreshDisabled {
		return
	}
	key := "refresh:" + uri
	c.mu.Lock()
	if c.inflight[key] || c.attempts[key] >= c.StagingAttempts {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		st, err := c.Cache.Lookup(ctx, uri)
		if err != nil || !st.Complete || st.Failed {
			return
		}
		if st.RefreshStable {
			return
		}
		cooldown := c.RefreshCooldown
		if cooldown == 0 {
			cooldown = 6 * time.Hour
		}
		if since := time.Since(st.PrimaryCreated); since < cooldown {
			log.WithFields(logrus.Fields{"generation": st.Generation, "since": since.Round(time.Second).String(), "cooldown": cooldown.String()}).Info("cache volume: refresh proposed inside the cooldown; waiting")
			return
		}
		// Only ranks seeded from the serving generation take part: a rank
		// seeded from an older one already gave its delta to this
		// generation, or reports against a tree that no longer serves.
		serving := strconv.Itoa(st.Generation)
		ranks, ready, size, ok, err := c.groupRanksWhere(ctx, uri, func(p *corev1.Pod) bool {
			g := p.Annotations[modelvolume.CacheSeedGenerationAnnotation]
			if g == "" {
				g = "1"
			}
			return g == serving
		})
		if err != nil {
			log.WithError(err).Warn("cache volume: list group failed")
			return
		}
		if !ok {
			if ready > 0 {
				log.WithFields(logrus.Fields{"ready": ready, "group_size": size, "generation": st.Generation}).Info("cache volume: refresh waits for the rest of the group")
			}
			return
		}
		deltas := map[int]string{}
		total := c.measureRanks(ctx, uri, ranks, log)
		for _, r := range ranks {
			if r.pod.Annotations[modelvolume.CacheRankDeltaAnnotation] == "true" {
				deltas[r.ordinal] = r.pod.Annotations[modelvolume.CacheRankDeltaFingerprintAnnotation]
			}
		}
		if len(deltas) == 0 {
			return
		}
		fp := refreshFingerprint(deltas)
		if fp == st.DeltaFingerprint {
			if err := c.Cache.MarkRefreshStable(ctx, st.PrimaryPV); err != nil {
				log.WithError(err).Warn("cache volume: mark set stable failed")
			}
			log.WithField("fingerprint", fp).Info("cache volume: the same delta came back after a refresh; the engine rewrites these files every start, set marked stable")
			return
		}
		gen := st.Generation + 1
		sysNS := c.Cache.Cfg.SystemNamespace()
		claim := c.Cache.Cfg.GenerationClaimName(uri, gen)
		_, created, err := c.Cache.ClaimSizedClaimNamed(ctx, claim, uri, sysNS, c.Cache.Cfg.VolumeSize(total))
		if err != nil {
			log.WithError(err).Warn("cache volume: refresh claim failed")
			return
		}
		if !created {
			return // another agent refreshes this set
		}
		meta := map[string]string{modelvolume.GenerationAnnotation: strconv.Itoa(gen), modelvolume.RefreshedFromAnnotation: st.PrimaryPV, modelvolume.DeltaFingerprintAnnotation: fp}
		start := time.Now()
		if err := c.collectSet(ctx, uri, sysNS, claim, ranks, meta, log); err != nil {
			c.mu.Lock()
			c.attempts[key]++
			c.mu.Unlock()
			log.WithError(err).Warn("cache volume: refresh collection failed")
			if derr := c.Kube.CoreV1().PersistentVolumeClaims(sysNS).Delete(ctx, claim, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				log.WithError(derr).Warn("cache volume: delete refresh claim after failure failed")
			}
			return
		}
		log.WithFields(logrus.Fields{"generation": gen, "ranks": len(ranks), "bytes": total, "claim": sysNS + "/" + claim, "fingerprint": fp, "elapsed": time.Since(start).Round(time.Second).String()}).Info("cache volume: set refreshed; later pods of this configuration seed from the new generation")
	}()
}

// refreshFingerprint identifies a set-wide delta: the per-rank
// fingerprints of the ranks that reported one, by ordinal.
func refreshFingerprint(deltas map[int]string) string {
	parts := make([]string, 0, len(deltas))
	for o, f := range deltas {
		parts = append(parts, strconv.Itoa(o)+":"+f)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(sum[:8])
}

// measureRanks sizes a set from what each rank holds at collection time.
// The rank-ready stamp records the tree when the pod first reported Ready,
// and a pod whose readiness comes from a stub (a multi-node follower's
// health responder) reports Ready before its engine has compiled anything
// (Kimi K3, GB300 2026-10-01: stamp 1.2 MB, tree 550 MB at collection; the
// set claim was sized at 1Gi for 1.27 GB of content). Local ranks are
// measured directly, remote ranks through the peer agent; a measurement
// that fails or comes back smaller than the stamp keeps the stamp.
func (c *ModelVolumeController) measureRanks(ctx context.Context, uri string, ranks []rankSource, log logrus.FieldLogger) int64 {
	var total int64
	for _, r := range ranks {
		size := r.bytes
		var fresh int64
		var err error
		if r.pod.Spec.NodeName == c.NodeName {
			fresh, _, err = c.treeStat(filepath.Join(c.HostFSRoot, r.src))
		} else {
			measure := c.rankSizeFn
			if measure == nil {
				measure = c.rankSizeHTTP
			}
			fresh, err = measure(ctx, uri, r)
		}
		switch {
		case err != nil:
			log.WithError(err).WithField("ordinal", r.ordinal).Warn("cache volume: rank measurement failed; sizing from its stamp")
		case fresh > size:
			size = fresh
		}
		total += size
	}
	return total
}

func (c *ModelVolumeController) rankSizeHTTP(ctx context.Context, uri string, r rankSource) (int64, error) {
	agents, err := c.Kube.CoreV1().Pods(c.Cache.Cfg.SystemNamespace()).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=nvsnap-agent", FieldSelector: "spec.nodeName=" + r.pod.Spec.NodeName})
	if err != nil || len(agents.Items) == 0 || agents.Items[0].Status.PodIP == "" {
		return 0, fmt.Errorf("no agent found on node %s: %v", r.pod.Spec.NodeName, err)
	}
	u := fmt.Sprintf("http://%s:%d/v1/cache-rank/%s/%d?stat=1", agents.Items[0].Status.PodIP, agentAPIPort, modelvolume.Key(uri), r.ordinal)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("peer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Bytes, nil
}
