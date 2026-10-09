// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// CRIU capture and restore of whole instances: the pods of one Helm
// StatefulSet or LeaderWorkerSet group, ranked by the webhook's
// nvsnap.io/cache-ordinal. The agent on rank 0's node drives both.
//
// Capture: once every rank is Ready and has warmed up, it checkpoints the
// instance (a group checkpoint when it has more than one pod, so the
// ranks suspend together and keep their connections) and records one
// checkpoint per rank under the instance's configuration key, in a
// ConfigMap that doubles as the lock: the agent that creates it captures.
//
// Restore: the webhook admits each pod of a later instance of that
// configuration and size as its rank's placeholder
// (internal/webhook/criu_group.go). Once every placeholder runs, the
// driver restores them as one group. A failure blocks the checkpoints and
// deletes the pods, so their replacements start fresh.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
	criuGroupConfigMapPrefix = "nvsnap-criu-group-"
	// criuGroupWarmup is how long an instance serves before it is
	// captured: the engine's first requests finish its lazy setup.
	criuGroupWarmup = 60 * time.Second
	// criuGroupPlaceholderWait bounds the wait for every placeholder of an
	// instance to run.
	criuGroupPlaceholderWait = 10 * time.Minute
	criuGroupCaptureTimeout  = 60 * time.Minute

	criuGroupStateCapturing = "capturing"
	criuGroupStateComplete  = "complete"
	criuGroupStateFailed    = "failed"

	lwsGroupKeyLabel = "leaderworkerset.sigs.k8s.io/group-key"

	// criuCaptureAnnotation opts a multi-pod instance into the capture.
	// Multi-pod capture is opt-in: a failed multi-node GPU suspend has
	// taken down the engine it suspended (kimi-k3, 2026-10-08).
	criuCaptureAnnotation = "nvsnap.io/criu-capture"
)

func criuGroupConfigMapName(key string) string { return criuGroupConfigMapPrefix + key }

// parseCRIUGroup reads a complete group record.
func parseCRIUGroup(key string, data map[string]string) (webhook.CRIUGroup, bool) {
	if data["state"] != criuGroupStateComplete {
		return webhook.CRIUGroup{}, false
	}
	size, err := strconv.Atoi(data["size"])
	if err != nil || size < 1 {
		return webhook.CRIUGroup{}, false
	}
	g := webhook.CRIUGroup{Key: key, GPUShare: data["gpushare"] == "true",
		Checkpoints: make([]string, size), Nodes: make([]string, size)}
	for i := range size {
		g.Checkpoints[i] = data[fmt.Sprintf("checkpoint.%d", i)]
		if g.Checkpoints[i] == "" {
			return webhook.CRIUGroup{}, false
		}
		g.Nodes[i] = data[fmt.Sprintf("node.%d", i)]
	}
	return g, true
}

// lookupCRIUGroup is the webhook's view of the group records.
func (a *Agent) lookupCRIUGroup(ctx context.Context, key string) (webhook.CRIUGroup, bool) {
	cm, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			a.log.WithError(err).WithField("criuGroup", key).Warn("CRIU group restore: cannot read the group record")
		}
		return webhook.CRIUGroup{}, false
	}
	return parseCRIUGroup(key, cm.Data)
}

// hasInstance reports whether the pod belongs to a workload instance: a
// Grove scaling group replica, a LeaderWorkerSet group or a controller. A
// bare pod is no instance.
func hasInstance(pod *corev1.Pod) bool {
	return groveInstance(pod) != "" || pod.Labels[lwsGroupKeyLabel] != "" || metav1.GetControllerOf(pod) != nil
}

// groveInstance identifies the pod's Grove scaling group replica, or "".
// Its pods have different controllers (one PodClique per role), so the
// controller does not identify the instance.
func groveInstance(pod *corev1.Pod) string {
	g, r := pod.Labels[webhook.GroveScalingGroupLabel], pod.Labels[webhook.GroveScalingGroupReplicaLabel]
	if g == "" || r == "" {
		return ""
	}
	return g + "/" + r
}

// sameInstance reports whether p belongs to leader's instance: the same
// Grove scaling group replica, else the same LeaderWorkerSet group, else
// the same controller.
func sameInstance(leader, p *corev1.Pod) bool {
	if g := groveInstance(leader); g != "" {
		return groveInstance(p) == g
	}
	if g := leader.Labels[lwsGroupKeyLabel]; g != "" {
		return p.Labels[lwsGroupKeyLabel] == g
	}
	lc, pc := metav1.GetControllerOf(leader), metav1.GetControllerOf(p)
	return lc != nil && pc != nil && lc.UID == pc.UID
}

// podRank returns the pod's rank and its instance's size as the webhook
// stamped them.
func podRank(pod *corev1.Pod, ordinalKey, sizeKey string) (ordinal, size int, ok bool) {
	o, err1 := strconv.Atoi(pod.Annotations[ordinalKey])
	s, err2 := strconv.Atoi(pod.Annotations[sizeKey])
	if err1 != nil || err2 != nil || s < 1 || o < 0 || o >= s {
		return 0, 0, false
	}
	return o, s, true
}

// instanceRanks returns the pods of leader's instance that pass keep, in
// rank order, or an error naming the first rank missing.
func instanceRanks(pods []corev1.Pod, leader *corev1.Pod, size int, ordinalKey, sizeKey string, keep func(*corev1.Pod) bool) ([]*corev1.Pod, error) {
	ranks := make([]*corev1.Pod, size)
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || !sameInstance(leader, p) || !keep(p) {
			continue
		}
		o, s, ok := podRank(p, ordinalKey, sizeKey)
		if !ok || s != size || ranks[o] != nil {
			continue
		}
		ranks[o] = p
	}
	for i, p := range ranks {
		if p == nil {
			return nil, fmt.Errorf("rank %d of %d is not ready", i, size)
		}
	}
	return ranks, nil
}

// gpuContainer is the first container that requests GPUs.
func gpuContainer(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if _, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
			return c.Name
		}
	}
	return ""
}

// criuGroupCaptureLeader reports whether pod is rank 0 of a Ready instance
// that has not been captured from, and returns its configuration key.
func criuGroupCaptureLeader(pod *corev1.Pod) (key string, size int, ok bool) {
	uri := pod.Annotations[cacheURIAnnotation]
	if uri == "" || pod.DeletionTimestamp != nil || pod.Annotations[webhook.CRIURestoreAnnotation] != "" {
		return "", 0, false
	}
	o, s, ok := podRank(pod, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation)
	if !ok || o != 0 || !rootfsonly.IsPodReady(pod) || gpuContainer(pod) == "" || !hasInstance(pod) {
		return "", 0, false
	}
	return webhook.CRIUGroupKey(uri), s, true
}

// CaptureOptIn opts the pods whose Label has one of Values into the
// multi-pod instance capture, for workloads whose pod spec nvsnap does not
// own (an NVCF function's pods are built by NVCA).
type CaptureOptIn struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// captureOptedIn reports whether a multi-pod instance's rank 0 may be
// captured: annotated nvsnap.io/criu-capture, or matched by the agent's
// configured opt-ins.
func (a *Agent) captureOptedIn(pod *corev1.Pod) bool {
	if pod.Annotations[criuCaptureAnnotation] == "true" {
		return true
	}
	for _, o := range a.config.CaptureOptIn {
		v, ok := pod.Labels[o.Label]
		if !ok {
			continue
		}
		for _, want := range o.Values {
			if v == want {
				return true
			}
		}
	}
	return false
}

func (c *criuAutoRestorer) considerGroupCapture(ctx context.Context, pod *corev1.Pod) {
	key, size, ok := criuGroupCaptureLeader(pod)
	if !ok || (size > 1 && !c.a.captureOptedIn(pod)) {
		return
	}
	if _, busy := c.attempted.LoadOrStore("capture/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		if retry := c.captureGroup(ctx, pod, key, size); retry {
			// Not ready yet: a later event of the pod tries again.
			c.attempted.Delete("capture/" + string(pod.UID))
		}
	}()
}

// captureGroup captures leader's instance. retry is true when the
// instance was not ready for it yet.
func (c *criuAutoRestorer) captureGroup(ctx context.Context, leader *corev1.Pod, key string, size int) (retry bool) {
	a := c.a
	log := a.log.WithFields(logrus.Fields{"criuGroup": key, "pod": leader.Namespace + "/" + leader.Name, "size": size})
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
	if _, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{}); err == nil {
		return false // captured, being captured, or failed before
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(criuGroupWarmup):
	}
	pods, err := a.kubeClient.CoreV1().Pods(leader.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Warn("CRIU group capture: cannot list the instance's pods")
		return true
	}
	uri := leader.Annotations[cacheURIAnnotation]
	ranks, err := instanceRanks(pods.Items, leader, size, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation, func(p *corev1.Pod) bool {
		return p.Annotations[cacheURIAnnotation] == uri && p.Annotations[webhook.CRIURestoreAnnotation] == "" && rootfsonly.IsPodReady(p)
	})
	if err != nil {
		log.WithError(err).Info("CRIU group capture: waiting for the whole instance")
		return true
	}
	for _, r := range ranks {
		if !a.gpushareEnabledFor(r) {
			// Without the library a GPU checkpoint goes through the CUDA
			// plugin: the whole GPU memory into the image, the engine paused
			// for minutes. Instances are captured under gpushare only.
			log.WithField("rank", r.Name).Info("CRIU group capture: skipped; the instance does not run under gpushare")
			return false
		}
	}
	if err := instanceCoversEngine(ranks); err != nil {
		// Part of an engine: pausing it would stall the rest.
		log.WithError(err).Warn("CRIU group capture: skipped; the instance is not the whole engine")
		return false
	}
	record := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
		Data: map[string]string{"state": criuGroupStateCapturing, "size": strconv.Itoa(size),
			"source": leader.Namespace + "/" + leader.Name, "startedAt": time.Now().UTC().Format(time.RFC3339)},
	}
	if _, err := cms.Create(ctx, record, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.WithError(err).Warn("CRIU group capture: cannot create the group record")
			return true
		}
		return false // another agent captures it
	}

	log.Info("CRIU group capture: instance ready; checkpointing it")
	cctx, cancel := context.WithTimeout(ctx, criuGroupCaptureTimeout)
	defer cancel()
	t0 := time.Now()
	results, err := c.checkpointInstance(cctx, ranks, log)
	data := record.Data
	if err != nil {
		log.WithError(err).Error("CRIU group capture failed; this configuration starts fresh")
		data["state"], data["reason"] = criuGroupStateFailed, truncate(err.Error(), 1024)
	} else {
		data["state"] = criuGroupStateComplete
		for i, r := range results {
			data[fmt.Sprintf("checkpoint.%d", i)] = r.CheckpointID
			data[fmt.Sprintf("node.%d", i)] = ranks[i].Spec.NodeName
		}
		if c.a.gpushareEnabledFor(ranks[0]) {
			data["gpushare"] = "true"
		}
		log.WithField("duration", time.Since(t0).Round(time.Second).String()).Info("CRIU group capture: recorded")
	}
	record.Data = data
	if _, err := cms.Update(context.WithoutCancel(ctx), record, metav1.UpdateOptions{}); err != nil {
		log.WithError(err).Error("CRIU group capture: cannot update the group record")
	}
	return false
}

// checkpointInstance checkpoints the ranks in place and returns their
// results in rank order.
func (c *criuAutoRestorer) checkpointInstance(ctx context.Context, ranks []*corev1.Pod, log *logrus.Entry) ([]*CheckpointResult, error) {
	reqs := make([]CheckpointRequest, len(ranks))
	for i, p := range ranks {
		reqs[i] = CheckpointRequest{Namespace: p.Namespace, PodName: p.Name, ContainerName: gpuContainer(p), LeaveRunning: true}
	}
	if len(reqs) == 1 {
		r, err := c.a.Checkpoint(ctx, reqs[0])
		if err != nil {
			return nil, err
		}
		return []*CheckpointResult{r}, nil
	}
	res, err := c.a.groupCheckpoint(ctx, GroupCheckpointRequest{Members: reqs, LeaveRunning: true}, log)
	if err != nil {
		return nil, err
	}
	out := make([]*CheckpointResult, len(res.Members))
	for i, m := range res.Members {
		if m.Result == nil || m.Result.CheckpointID == "" {
			return nil, fmt.Errorf("member %s/%s returned no checkpoint", m.Namespace, m.PodName)
		}
		out[i] = m.Result
	}
	return out, nil
}

// gpushareEnabledFor reports whether the pod runs under the gpushare
// library, so its restore mounts the checkpoint's chunk store.
func (a *Agent) gpushareEnabledFor(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "LD_PRELOAD" && strings.Contains(e.Value, webhook.GPUShareLibPath) {
				return true
			}
		}
	}
	return false
}

// criuGroupRestoreLeader reports whether pod is the running rank-0
// placeholder of a multi-pod group restore.
func criuGroupRestoreLeader(pod *corev1.Pod) (key string, size int, ok bool) {
	key = pod.Annotations[webhook.CRIUGroupAnnotation]
	if key == "" {
		return "", 0, false
	}
	o, s, ok := podRank(pod, webhook.CRIUGroupOrdinalAnnotation, webhook.CRIUGroupSizeAnnotation)
	if !ok || o != 0 || s < 2 || !hasInstance(pod) {
		return "", 0, false
	}
	if _, _, running := criuRestoreTarget(pod); !running {
		return "", 0, false
	}
	return key, s, true
}

// isGroupPlaceholder reports a placeholder restored by its group's driver
// rather than on its own.
func isGroupPlaceholder(pod *corev1.Pod) bool {
	_, s, ok := podRank(pod, webhook.CRIUGroupOrdinalAnnotation, webhook.CRIUGroupSizeAnnotation)
	return pod.Annotations[webhook.CRIUGroupAnnotation] != "" && ok && s >= 2
}

func (c *criuAutoRestorer) considerGroupRestore(ctx context.Context, pod *corev1.Pod) {
	key, size, ok := criuGroupRestoreLeader(pod)
	if !ok {
		return
	}
	if _, done := c.attempted.LoadOrStore("restore/"+string(pod.UID), struct{}{}); done {
		return
	}
	go c.restoreGroup(ctx, pod, key, size)
}

func (c *criuAutoRestorer) restoreGroup(ctx context.Context, leader *corev1.Pod, key string, size int) {
	a := c.a
	log := a.log.WithFields(logrus.Fields{"criuGroup": key, "pod": leader.Namespace + "/" + leader.Name, "size": size})
	t0 := time.Now()
	var ranks []*corev1.Pod
	var waitErr error
	for {
		pods, err := a.kubeClient.CoreV1().Pods(leader.Namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			ranks, waitErr = instanceRanks(pods.Items, leader, size, webhook.CRIUGroupOrdinalAnnotation, webhook.CRIUGroupSizeAnnotation, func(p *corev1.Pod) bool {
				_, _, running := criuRestoreTarget(p)
				return p.Annotations[webhook.CRIUGroupAnnotation] == key && running
			})
			if waitErr == nil {
				break
			}
		} else {
			waitErr = err
		}
		if time.Since(t0) > criuGroupPlaceholderWait || ctx.Err() != nil {
			c.failGroup(ctx, leader, ranks, fmt.Errorf("placeholders not running after %s: %w", criuGroupPlaceholderWait, waitErr), log)
			return
		}
		time.Sleep(5 * time.Second)
	}

	req := GroupRestoreRequest{Members: make([]GroupRestoreMember, size)}
	for i, p := range ranks {
		req.Members[i] = GroupRestoreMember{
			CheckpointID:             p.Annotations[webhook.CRIURestoreAnnotation],
			PlaceholderNamespace:     p.Namespace,
			PlaceholderPodName:       p.Name,
			PlaceholderContainerName: p.Annotations[webhook.CRIURestoreContainerAnnotation],
			ReservePIDs:              true,
		}
	}
	log.Info("CRIU group restore: every placeholder runs; restoring the instance")
	rctx, cancel := context.WithTimeout(ctx, criuAutoRestoreTimeout)
	defer cancel()
	res, err := a.groupRestore(rctx, req, log)
	if err == nil {
		log.WithField("duration", time.Since(t0).Round(time.Millisecond).String()).Info("CRIU group restore: restored")
		return
	}
	if res != nil && len(res.DeletedPods) > 0 {
		ranks = nil // groupRestore deleted them
	}
	c.failGroup(ctx, leader, ranks, err, log)
}

// failGroup blocks the group's checkpoints and deletes its placeholders,
// so their replacements start fresh.
func (c *criuAutoRestorer) failGroup(ctx context.Context, leader *corev1.Pod, ranks []*corev1.Pod, cause error, log *logrus.Entry) {
	log.WithError(cause).Error("CRIU group restore failed; blocking its checkpoints and deleting its pods so their replacements start fresh")
	ctx = context.WithoutCancel(ctx)
	ids := map[string]bool{leader.Annotations[webhook.CRIURestoreAnnotation]: true}
	if g, ok := c.a.lookupCRIUGroup(ctx, leader.Annotations[webhook.CRIUGroupAnnotation]); ok {
		for _, id := range g.Checkpoints {
			ids[id] = true
		}
	}
	for id := range ids {
		if id == "" {
			continue
		}
		if err := c.markFailed(ctx, id, cause); err != nil {
			log.WithError(err).WithField("checkpoint", id).Error("CRIU group restore: could not block the checkpoint")
		}
	}
	victims := ranks
	if victims == nil {
		victims = []*corev1.Pod{leader}
	}
	for _, p := range victims {
		if p == nil {
			continue
		}
		if err := c.a.kubeClient.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("placeholder", p.Name).Error("CRIU group restore: could not delete the placeholder")
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
