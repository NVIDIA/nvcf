// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// CRIU auto-restore. With CRIU as the cluster's capture method, the webhook
// admits a pod whose nvsnap.io/restore-from names a CRIU capture as its own
// restore placeholder (internal/webhook/criu_restore.go). This controller,
// on the pod's node, restores the checkpoint into it once its container
// runs. A failed restore marks the checkpoint blocked, so the webhook starts
// the pod's replacements fresh, and deletes the pod so its controller
// creates that replacement.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// criuRestoreFailedConfigMap lists checkpoints that failed to restore
	// (key: checkpoint id, value: when and why).
	criuRestoreFailedConfigMap = "nvsnap-criu-restore-failed"
	criuAutoRestoreTimeout     = 30 * time.Minute
	criuBlockedCacheTTL        = 10 * time.Second
)

// criuIsDefault reports whether CRIU is this cluster's capture method
// (chart value agent.captureMethod).
func criuIsDefault() bool { return !RootfsIsDefault() }

// criuRestoreTarget returns the checkpoint and container of a pod the
// webhook prepared for a CRIU restore, once that container runs.
func criuRestoreTarget(pod *corev1.Pod) (checkpointID, container string, ready bool) {
	checkpointID = pod.Annotations[webhook.CRIURestoreAnnotation]
	container = pod.Annotations[webhook.CRIURestoreContainerAnnotation]
	if checkpointID == "" || container == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning ||
		pod.Annotations[webhook.CRIURestoredAnnotation] != "" {
		return checkpointID, container, false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == container {
			return checkpointID, container, cs.State.Running != nil
		}
	}
	return checkpointID, container, false
}

// criuReservePIDArgs raises the next pid of the pod's pid namespace above
// the dumped range. The placeholder cannot: function pods are not
// privileged. ns_last_pid applies to the writer's own pid namespace, so
// entering only that namespace is enough.
func criuReservePIDArgs(hostPID int) []string {
	return []string{"-t", strconv.Itoa(hostPID), "-p", "--", "/bin/sh", "-c",
		fmt.Sprintf("echo %d > /proc/sys/kernel/ns_last_pid", reservedPIDFloor*2)}
}

// criuAutoRestorer restores CRIU checkpoints into the placeholder pods on
// this node.
type criuAutoRestorer struct {
	a         *Agent
	attempted sync.Map // pod UID -> struct{}

	mu        sync.Mutex
	blocked   map[string]bool
	blockedAt time.Time
}

func (a *Agent) startCRIUAutoRestore(ctx context.Context) error {
	if !criuIsDefault() {
		return nil
	}
	if a.kubeClient == nil || a.config.NodeName == "" {
		return fmt.Errorf("CRIU auto-restore needs the kube client and the node name")
	}
	c := a.criuRestorer()
	factory := informers.NewSharedInformerFactoryWithOptions(a.kubeClient, 30*time.Second,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.FieldSelector = "spec.nodeName=" + a.config.NodeName }))
	informer := factory.Core().V1().Pods().Informer()
	handle := func(obj any) {
		if pod, ok := obj.(*corev1.Pod); ok {
			c.consider(ctx, pod)
		}
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    handle,
		UpdateFunc: func(_, obj any) { handle(obj) },
	}); err != nil {
		return fmt.Errorf("CRIU auto-restore: %w", err)
	}
	go factory.Start(ctx.Done())
	a.log.WithField("node", a.config.NodeName).Info("CRIU auto-restore: watching this node's pods")
	return nil
}

func (a *Agent) criuRestorer() *criuAutoRestorer {
	a.criuRestoreOnce.Do(func() { a.criuRestore = &criuAutoRestorer{a: a} })
	return a.criuRestore
}

func (c *criuAutoRestorer) consider(ctx context.Context, pod *corev1.Pod) {
	c.considerGroupCapture(ctx, pod)
	c.considerRestored(ctx, pod)
	if isGroupPlaceholder(pod) {
		c.considerGroupRestore(ctx, pod)
		return
	}
	id, container, ready := criuRestoreTarget(pod)
	if !ready {
		return
	}
	if _, done := c.attempted.LoadOrStore(pod.UID, struct{}{}); done {
		return
	}
	go c.restore(ctx, pod.Namespace, pod.Name, id, container)
}

func (c *criuAutoRestorer) restore(ctx context.Context, ns, pod, id, container string) {
	a := c.a
	log := a.log.WithFields(logrus.Fields{"pod": ns + "/" + pod, "checkpoint": id, "container": container})
	rctx, cancel := context.WithTimeout(ctx, criuAutoRestoreTimeout)
	defer cancel()
	t0 := time.Now()
	err := func() error {
		_, err := a.Restore(rctx, RestoreRequest{
			CheckpointID:             id,
			PlaceholderNamespace:     ns,
			PlaceholderPodName:       pod,
			PlaceholderContainerName: container,
			ReservePIDs:              true,
		})
		return err
	}()
	if err == nil {
		log.WithField("duration", time.Since(t0).Round(time.Millisecond).String()).Info("CRIU auto-restore: restored")
		a.markRestored(context.WithoutCancel(ctx), ns, pod, log)
		return
	}
	if errors.Is(err, errNotPlaceholder) {
		// Restored already (by this agent before a restart): leave it be.
		log.WithError(err).Warn("CRIU auto-restore: the pod already runs its workload; not restoring, not deleting")
		a.markRestored(context.WithoutCancel(ctx), ns, pod, log)
		return
	}
	log.WithError(err).Error("CRIU auto-restore failed; blocking the checkpoint and deleting the pod so its replacement starts fresh")
	if merr := c.markFailed(context.WithoutCancel(ctx), id, err); merr != nil {
		log.WithError(merr).Error("CRIU auto-restore: could not record the failed checkpoint; its next pods will try it again")
	}
	if derr := a.kubeClient.CoreV1().Pods(ns).Delete(context.WithoutCancel(ctx), pod, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
		log.WithError(derr).Error("CRIU auto-restore: could not delete the failed pod")
	}
}

func (a *Agent) criuFailedNamespace() string {
	if ns := a.config.RootfsCapture.CMNamespace; ns != "" {
		return ns
	}
	return "nvsnap-system"
}

// markFailed records id in the blocked list.
func (c *criuAutoRestorer) markFailed(ctx context.Context, id string, cause error) error {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	reason := time.Now().UTC().Format(time.RFC3339) + " " + cause.Error()
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := cms.Get(ctx, criuRestoreFailedConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = cms.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: criuRestoreFailedConfigMap, Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
				Data:       map[string]string{id: reason},
			}, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[id] = reason
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
	c.mu.Lock()
	c.blocked = nil // reread on the next check
	c.mu.Unlock()
	return err
}

// Blocked reports whether id failed to restore before. The list is read at
// most every criuBlockedCacheTTL; an unreadable list blocks nothing.
func (c *criuAutoRestorer) Blocked(ctx context.Context, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocked == nil || time.Since(c.blockedAt) > criuBlockedCacheTTL {
		m := map[string]bool{}
		cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuRestoreFailedConfigMap, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			c.a.log.WithError(err).Warn("CRIU restore: cannot read the failed-checkpoint list")
			return false
		}
		if cm != nil {
			for k := range cm.Data {
				m[k] = true
			}
		}
		c.blocked, c.blockedAt = m, time.Now()
	}
	return c.blocked[id]
}

// recordCRIUCapture makes a CRIU checkpoint findable by its hash: the
// webhook reads capture records to decide how a pod with
// nvsnap.io/restore-from restores. The checkpoint stays where it is; only
// the record is written.
func (a *Agent) recordCRIUCapture(ctx context.Context, hash, checkpointID string, req CheckpointRequest, image string, gpushare bool, log *logrus.Entry) {
	r, ok := a.captureBackend.(checkpointstore.ManifestRecorder)
	if !ok || hash == "" {
		log.Debug("CRIU capture not recorded: no capture record store")
		return
	}
	// The catalog is content-addressed: a later capture of the same hash
	// keeps the first checkpoint's id. Keep the record on that checkpoint
	// too, or a restore on another node asks the catalog for an id it
	// never registered.
	if prev, err := a.captureBackend.Stat(ctx, hash); err == nil {
		if id := prev.SourcePodMeta["checkpoint_id"]; id != "" && id != checkpointID && prev.CaptureMethod == "criu" {
			log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "recorded": id, "new": checkpointID}).
				Info("CRIU capture of an already recorded hash; restores keep using the recorded checkpoint")
			return
		}
	}
	m := checkpointstore.Manifest{
		Hash:          hash,
		CaptureMethod: "criu",
		CapturedAt:    time.Now().UTC(),
		SourcePodMeta: map[string]string{
			"engine": "criu", "checkpoint_id": checkpointID,
			"namespace": req.Namespace, "pod": req.PodName, "image": image, "node": a.config.NodeName,
		},
	}
	if gpushare {
		// The restore mounts the checkpoint's chunk store and the library.
		m.SourcePodMeta["gpushare"] = "true"
	}
	if req.CaptureID != "" {
		m.SourcePodMeta[checkpointstore.InstanceCaptureMetaKey] = "true"
	}
	if a.config.NodeName != "" {
		m.CapturedOnNodes = []string{a.config.NodeName}
	}
	if err := r.RecordManifest(ctx, hash, m); err != nil {
		log.WithError(err).Warn("CRIU capture not recorded; pods restoring from it will start fresh")
		return
	}
	log.WithFields(logrus.Fields{"hash": checkpointstore.ShortHash(hash), "checkpoint": checkpointID}).Info("CRIU capture recorded for restore")
}

// markRestored annotates a restored placeholder so a restarted agent does
// not restore into it again (and, refused, delete it).
func (a *Agent) markRestored(ctx context.Context, ns, pod string, log *logrus.Entry) {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, webhook.CRIURestoredAnnotation, time.Now().UTC().Format(time.RFC3339))
	if _, err := a.kubeClient.CoreV1().Pods(ns).Patch(ctx, pod, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Warn("CRIU restore: could not mark the pod restored; an agent restart may try it again")
	}
}
