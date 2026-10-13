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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	criuGroupConfigMapPrefix = "nvsnap-criu-group-"
	// criuGroupStateAlias marks a record that only names the record of the
	// instance a rank belongs to (data "alias").
	criuGroupStateAlias = "alias"
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
	// criuGroupStateRestoreFailed is a capture whose restore failed: its
	// checkpoints are blocked and the next Ready instance captures anew.
	criuGroupStateRestoreFailed = "restore-failed"

	// criuGroupMaxCaptures bounds the captures of one configuration, so a
	// workload that cannot be captured or restored stops paying for it.
	criuGroupMaxCaptures = 3
	// criuGroupMaxStalls bounds the restores of one capture that did not
	// get to run (a rank never scheduled, a checkpoint never fetched).
	// Past it the configuration starts cold, keeping its checkpoints.
	criuGroupMaxStalls = 3

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
	if n, _ := strconv.Atoi(data["stalls"]); n >= criuGroupMaxStalls {
		return webhook.CRIUGroup{}, false
	}
	size, err := strconv.Atoi(data["size"])
	if err != nil || size < 1 {
		return webhook.CRIUGroup{}, false
	}
	g := webhook.CRIUGroup{Key: key, GPUShare: data["gpushare"] == "true", Slot: -1,
		Checkpoints: make([]string, size), Nodes: make([]string, size), Companion: make([]bool, size)}
	for i := range size {
		g.Checkpoints[i] = data[fmt.Sprintf("checkpoint.%d", i)]
		if g.Checkpoints[i] == "" {
			return webhook.CRIUGroup{}, false
		}
		g.Nodes[i] = data[fmt.Sprintf("node.%d", i)]
		g.Companion[i] = data[fmt.Sprintf("companion.%d", i)] == "true"
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
	if cm.Data["state"] == criuGroupStateAlias {
		// A rank of an instance whose ranks differ in configuration finds
		// the instance's record through the alias of its own.
		// A companion's alias also names its slot.
		target, slot := cm.Data["alias"], cm.Data["slot"]
		if cm, err = a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(target), metav1.GetOptions{}); err != nil {
			return webhook.CRIUGroup{}, false
		}
		g, ok := parseCRIUGroup(target, cm.Data)
		if n, err := strconv.Atoi(slot); ok && slot != "" && err == nil {
			g.Slot = n
		}
		return g, ok
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
		if p.DeletionTimestamp != nil || !groupMember(leader, p) || !keep(p) {
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
// that has not been captured from, and returns its cache URI.
func criuGroupCaptureLeader(pod *corev1.Pod) (uri string, size int, ok bool) {
	uri = pod.Annotations[cacheURIAnnotation]
	if uri == "" || pod.DeletionTimestamp != nil || pod.Annotations[webhook.CRIURestoreAnnotation] != "" {
		return "", 0, false
	}
	o, s, isRank := podRank(pod, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation)
	if !isRank || o != 0 || !rootfsonly.IsPodReady(pod) || gpuContainer(pod) == "" || !hasInstance(pod) {
		return "", 0, false
	}
	return uri, s, true
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
	if name := c.a.identityBlocksCapture(pod); name != "" && rootfsonly.IsPodReady(pod) && pod.Annotations[cacheURIAnnotation] != "" {
		if _, seen := c.attempted.LoadOrStore("identity/"+string(pod.UID), struct{}{}); !seen {
			c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "env": name}).
				Info("CRIU group capture: skipped; the pod names its own instance in its configuration, so a restore would run as the wrong instance (it starts from the caches)")
		}
		return
	}
	uri, size, ok := criuGroupCaptureLeader(pod)
	if !ok || (size > 1 && !c.a.captureOptedIn(pod)) {
		return
	}
	scope, err := c.a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		c.a.log.WithError(err).WithField("namespace", pod.Namespace).Warn("CRIU group capture: cannot read the namespace's scope")
		return
	}
	if size == 1 {
		// One pod: its record is known now, so a captured configuration
		// is not waited on again.
		if prev, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(criuGroupKey(uri, scope)), metav1.GetOptions{}); err == nil && !criuGroupRecaptures(prev.Data, time.Now()) {
			return
		}
	}
	if _, busy := c.attempted.LoadOrStore("capture/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		if retry := c.captureGroup(ctx, pod, scope, size); retry {
			// Not ready yet: a later event of the pod tries again.
			c.attempted.Delete("capture/" + string(pod.UID))
		}
	}()
}

// captureGroup captures leader's instance. retry is true when the
// instance was not ready for it yet.
func (c *criuAutoRestorer) captureGroup(ctx context.Context, leader *corev1.Pod, scope string, size int) (retry bool) {
	a := c.a
	log := a.log.WithFields(logrus.Fields{"pod": leader.Namespace + "/" + leader.Name, "size": size})
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
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
	// The ranks need not share one configuration: a Grove leader and its
	// workers come from different templates. Pods of one template must.
	ranks, err := instanceRanks(pods.Items, leader, size, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation, func(p *corev1.Pod) bool {
		return p.Annotations[cacheURIAnnotation] != "" && p.Annotations[webhook.CRIURestoreAnnotation] == "" && rootfsonly.IsPodReady(p)
	})
	if err == nil {
		err = ranksAgreePerTemplate(ranks)
	}
	if err != nil {
		log.WithError(err).Info("CRIU group capture: waiting for the whole instance")
		return true
	}
	for _, r := range ranks {
		if name := a.identityBlocksCapture(r); name != "" {
			log.WithFields(logrus.Fields{"rank": r.Name, "env": name}).Info("CRIU group capture: skipped; a rank names its own instance in its configuration")
			return false
		}
	}
	companions, err := a.discoverCompanions(ctx, ranks, log)
	if err != nil {
		log.WithError(err).Warn("CRIU group capture: skipped; a companion of the instance cannot be captured")
		return false
	}
	for _, r := range ranks {
		if webhook.DynamoComponent(r) && len(companions) == 0 {
			log.WithField("rank", r.Name).Warn("CRIU group capture: skipped; a Dynamo rank is connected to no etcd or NATS of its namespace to capture with it")
			return false
		}
	}
	uris := make([]string, 0, len(ranks)+len(companions))
	for _, r := range ranks {
		uris = append(uris, r.Annotations[cacheURIAnnotation])
	}
	for _, p := range companions {
		u, ok := a.specCacheURI(p)
		if !ok {
			log.WithField("companion", p.Name).Warn("CRIU group capture: skipped; no configuration for a companion")
			return false
		}
		uris = append(uris, u)
	}
	if dup := duplicateURI(uris[len(ranks):]); dup != "" {
		log.WithField("configuration", dup).Warn("CRIU group capture: skipped; two companions share a configuration, so a replacement could not tell which it restores")
		return false
	}
	key := criuGroupKey(instanceConfigURIOf(uris), scope)
	log = log.WithField("criuGroup", key)
	if prev, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{}); err == nil && !criuGroupRecaptures(prev.Data, time.Now()) {
		c.writeGroupAliases(ctx, key, scope, uris, len(ranks), log)
		return false // captured, being captured, or out of attempts
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
	record, err := c.claimGroupRecord(ctx, key, leader, len(uris))
	if err != nil {
		log.WithError(err).Warn("CRIU group capture: cannot claim the group record")
		return true
	}
	if record == nil {
		return false // another agent captures it
	}

	log.Info("CRIU group capture: instance ready; checkpointing it")
	cctx, cancel := context.WithTimeout(ctx, criuGroupCaptureTimeout)
	defer cancel()
	t0 := time.Now()
	results, err := c.checkpointInstance(cctx, ranks, companions, log)
	data := record.Data
	if err != nil {
		log.WithError(err).Error("CRIU group capture failed; this configuration starts fresh")
		data["state"], data["reason"] = criuGroupStateFailed, truncate(err.Error(), 1024)
		if strings.Contains(err.Error(), errSourceLeftSuspended.Error()) {
			// A rank could not be resumed: the instance serves nothing.
			// Its controllers replace every pod of it.
			log.Error("CRIU group capture left a rank suspended; deleting the instance's pods")
			for _, r := range ranks {
				if derr := a.kubeClient.CoreV1().Pods(r.Namespace).Delete(context.WithoutCancel(ctx), r.Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
					log.WithError(derr).WithField("pod", r.Name).Error("cannot delete a pod of the suspended instance")
				}
			}
		}
	} else {
		data["state"] = criuGroupStateComplete
		members := append(append([]*corev1.Pod{}, ranks...), companions...)
		for i, r := range results {
			data[fmt.Sprintf("checkpoint.%d", i)] = r.CheckpointID
			data[fmt.Sprintf("node.%d", i)] = members[i].Spec.NodeName
			if i >= len(ranks) {
				data[fmt.Sprintf("companion.%d", i)] = "true"
			}
		}
		if c.a.gpushareEnabledFor(ranks[0]) {
			data["gpushare"] = "true"
		}
		log.WithField("duration", time.Since(t0).Round(time.Second).String()).Info("CRIU group capture: recorded")
	}
	started := record.Data["startedAt"]
	if err := c.updateGroupRecord(context.WithoutCancel(ctx), key, func(d map[string]string) bool {
		if d["state"] != criuGroupStateCapturing || d["startedAt"] != started {
			return false // another capture took the record over
		}
		for k := range d {
			delete(d, k)
		}
		for k, v := range data {
			d[k] = v
		}
		return true
	}); err != nil {
		log.WithError(err).Error("CRIU group capture: cannot update the group record")
	} else if data["state"] == criuGroupStateComplete {
		c.writeGroupAliases(context.WithoutCancel(ctx), key, scope, uris, len(ranks), log)
	}
	return false
}

// instanceConfigURI is the configuration of an instance: its ranks' shared
// configuration, or, when they differ, one derived from all of them in rank
// order.
func instanceConfigURI(ranks []*corev1.Pod) string {
	uris := make([]string, len(ranks))
	for i, r := range ranks {
		uris[i] = r.Annotations[cacheURIAnnotation]
	}
	return instanceConfigURIOf(uris)
}

// instanceConfigURIOf is instanceConfigURI of the members' configurations
// in slot order.
func instanceConfigURIOf(uris []string) string {
	if instanceUniform(uris) {
		return uris[0]
	}
	sum := sha256.Sum256([]byte(strings.Join(uris, "\n")))
	return "cache://group-" + hex.EncodeToString(sum[:16])
}

func instanceUniform(uris []string) bool {
	for _, u := range uris {
		if u != uris[0] {
			return false
		}
	}
	return true
}

// ranksAgreePerTemplate refuses an instance whose pods of one template
// (one controller) differ in configuration: a rollout in progress, which a
// capture must not mix.
func ranksAgreePerTemplate(ranks []*corev1.Pod) error {
	seen := map[types.UID]*corev1.Pod{}
	for _, r := range ranks {
		ctrl := metav1.GetControllerOf(r)
		if ctrl == nil {
			continue
		}
		if prev, ok := seen[ctrl.UID]; ok && prev.Annotations[cacheURIAnnotation] != r.Annotations[cacheURIAnnotation] {
			return fmt.Errorf("pods %s and %s of %s %s differ in configuration (a rollout in progress)", prev.Name, r.Name, ctrl.Kind, ctrl.Name)
		}
		seen[ctrl.UID] = r
	}
	return nil
}

// writeGroupAliases lets each rank of an instance whose ranks differ in
// configuration find the instance's record from its own configuration, as
// admission looks it up. A real record already under a rank's key (a
// uniform instance of that configuration) is left alone.
func (c *criuAutoRestorer) writeGroupAliases(ctx context.Context, key, scope string, uris []string, ranks int, log *logrus.Entry) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	done := map[string]bool{key: true}
	for i, uri := range uris {
		akey := criuGroupKey(uri, scope)
		if done[akey] {
			continue
		}
		done[akey] = true
		data := map[string]string{"state": criuGroupStateAlias, "alias": key}
		if i >= ranks {
			// A companion is admitted by its own configuration alone: the
			// alias names its slot.
			data["slot"] = strconv.Itoa(i)
		}
		alias := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(akey), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
			Data:       data,
		}
		_, err := cms.Create(ctx, alias, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			prev, gerr := cms.Get(ctx, alias.Name, metav1.GetOptions{})
			if gerr != nil || prev.Data["state"] != criuGroupStateAlias || (prev.Data["alias"] == key && prev.Data["slot"] == data["slot"]) {
				continue
			}
			prev.Data = data
			_, err = cms.Update(ctx, prev, metav1.UpdateOptions{})
		}
		if err != nil {
			log.WithError(err).WithField("slot", i).Warn("CRIU group capture: cannot record a member's alias; it will start cold")
		}
	}
}

// criuGroupRecaptures reports whether a group record lets its
// configuration be captured again: its capture failed or its restore did,
// and attempts remain, or its capture stopped without finishing.
func criuGroupRecaptures(data map[string]string, now time.Time) bool {
	n, _ := strconv.Atoi(data["captures"])
	switch data["state"] {
	case criuGroupStateFailed, criuGroupStateRestoreFailed:
		return n < criuGroupMaxCaptures
	case criuGroupStateCapturing:
		started, err := time.Parse(time.RFC3339, data["startedAt"])
		return n < criuGroupMaxCaptures && (err != nil || now.Sub(started) > criuGroupCaptureTimeout+10*time.Minute)
	}
	return false
}

// claimGroupRecord makes the agent the capturer of key: it creates the
// record, or takes over one that may be captured again. It returns nil
// when another agent holds it.
func (c *criuAutoRestorer) claimGroupRecord(ctx context.Context, key string, leader *corev1.Pod, size int) (*corev1.ConfigMap, error) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	data := map[string]string{"state": criuGroupStateCapturing, "size": strconv.Itoa(size), "captures": "1",
		"source": leader.Namespace + "/" + leader.Name, "startedAt": time.Now().UTC().Format(time.RFC3339)}
	record := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
		Data:       data,
	}
	created, err := cms.Create(ctx, record, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	prev, err := cms.Get(ctx, record.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !criuGroupRecaptures(prev.Data, time.Now()) {
		return nil, nil
	}
	n, _ := strconv.Atoi(prev.Data["captures"])
	data["captures"] = strconv.Itoa(n + 1)
	if prev.Data["reason"] != "" {
		data["previousReason"] = truncate(prev.Data["reason"], 1024)
	}
	prev.Data = data
	// The resource version makes the takeover a compare-and-swap: of two
	// agents taking over, one gets a conflict.
	updated, err := cms.Update(ctx, prev, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil, nil
	}
	return updated, err
}

// updateGroupRecord applies change to the group record of key; change
// returns false to leave the record as it is.
func (c *criuAutoRestorer) updateGroupRecord(ctx context.Context, key string, change func(map[string]string) bool) error {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		if !change(cm.Data) {
			return nil
		}
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}

// recordOf reports whether the group record data is of the capture
// checkpoint id belongs to.
func recordOf(d map[string]string, id string) bool {
	if id == "" {
		return false
	}
	for k, v := range d {
		if strings.HasPrefix(k, "checkpoint.") && v == id {
			return true
		}
	}
	return false
}

// checkpointInstance checkpoints the ranks in place and returns their
// results in rank order.
func (c *criuAutoRestorer) checkpointInstance(ctx context.Context, ranks, companions []*corev1.Pod, log *logrus.Entry) ([]*CheckpointResult, error) {
	capture := time.Now().UTC().Format("20060102T150405.000000000")
	reqs := make([]CheckpointRequest, 0, len(ranks)+len(companions))
	for _, p := range ranks {
		reqs = append(reqs, CheckpointRequest{Namespace: p.Namespace, PodName: p.Name, ContainerName: gpuContainer(p), LeaveRunning: true, CaptureID: capture})
	}
	for _, p := range companions {
		reqs = append(reqs, CheckpointRequest{Namespace: p.Namespace, PodName: p.Name, ContainerName: p.Spec.Containers[0].Name,
			LeaveRunning: true, CaptureID: capture, Companion: true})
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

// duplicateURI returns a configuration that occurs twice, or "".
func duplicateURI(uris []string) string {
	seen := map[string]bool{}
	for _, u := range uris {
		if seen[u] {
			return u
		}
		seen[u] = true
	}
	return ""
}

// identityBlocksCapture returns the env variable through which a GPU pod
// names its own instance, unless a group restore keeps that identity
// consistent: a Dynamo component that runs under the fixed Dynamo
// namespace and registers in its instance's etcd (webhook.DynamoRestorable),
// whose etcd is captured with it as a companion.
func (a *Agent) identityBlocksCapture(pod *corev1.Pod) string {
	name := webhook.NamesOwnInstance(pod)
	if name == "" {
		return ""
	}
	fixed := a.config.Webhook.DynamoNamespace
	if webhook.DynamoRestorable(pod, fixed) && webhook.DynamoNamespaceIs(pod, fixed) {
		return ""
	}
	return name
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
		if ctx.Err() != nil {
			return // the agent stops; the next one drives the restore
		}
		if time.Since(t0) > criuGroupPlaceholderWait {
			c.failGroup(ctx, leader, key, false, fmt.Errorf("placeholders not running after %s: %w", criuGroupPlaceholderWait, waitErr), log)
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
	_, err := a.groupRestore(rctx, req, log)
	if err == nil {
		log.WithField("duration", time.Since(t0).Round(time.Millisecond).String()).Info("CRIU group restore: restored")
		for _, p := range ranks {
			a.markRestored(context.WithoutCancel(ctx), p.Namespace, p.Name, log)
		}
		id := leader.Annotations[webhook.CRIURestoreAnnotation]
		_ = c.updateGroupRecord(context.WithoutCancel(ctx), key, func(d map[string]string) bool { return recordRestored(d, id) })
		return
	}
	if ctx.Err() != nil && !errors.As(err, new(*groupRestoreFailure)) {
		return // the agent stops before the restore ran
	}
	c.failGroup(ctx, leader, key, errors.As(err, new(*groupRestoreFailure)), err, log)
}

// failGroup ends a group restore that did not succeed: it deletes every
// pod of the instance admitted for it, so their replacements start again.
// A restore that ran and failed (restoreFailed) also blocks the
// checkpoints and lets the configuration be captured again; any other
// failure keeps them, and counts toward criuGroupMaxStalls.
func (c *criuAutoRestorer) failGroup(ctx context.Context, leader *corev1.Pod, key string, restoreFailed bool, cause error, log *logrus.Entry) {
	c.endGroup(ctx, leader, key, restoreFailed, false, cause, log)
}

// endGroup is failGroup, also deleting the instance's restored pods when
// includeRestored (their engine died).
func (c *criuAutoRestorer) endGroup(ctx context.Context, leader *corev1.Pod, key string, restoreFailed, includeRestored bool, cause error, log *logrus.Entry) {
	ctx = context.WithoutCancel(ctx)
	log = log.WithField("restoreRan", restoreFailed)
	pods, err := c.a.kubeClient.CoreV1().Pods(leader.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Error("CRIU group restore: cannot list the instance's pods")
		return
	}
	members := groupPlaceholders(pods.Items, leader, key, includeRestored)
	// The checkpoints these pods restore from: the record may since name
	// another capture of the configuration, which this failure says
	// nothing about.
	leaderID := leader.Annotations[webhook.CRIURestoreAnnotation]
	ids := map[string]bool{leaderID: true}
	for _, p := range members {
		ids[p.Annotations[webhook.CRIURestoreAnnotation]] = true
	}
	if restoreFailed {
		log.WithError(cause).Error("CRIU group restore failed; blocking its checkpoints and deleting its pods so their replacements start fresh")
		for id := range ids {
			if id == "" {
				continue
			}
			if err := c.markFailed(ctx, id, cause); err != nil {
				log.WithError(err).WithField("checkpoint", id).Error("CRIU group restore: could not block the checkpoint")
			}
		}
	} else {
		log.WithError(cause).Warn("CRIU group restore did not run; deleting its pods so their replacements try again")
	}
	if err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
		return recordFailure(d, leaderID, restoreFailed, cause)
	}); err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Warn("CRIU group restore: cannot update the group record")
	}
	for _, p := range members {
		if err := c.a.kubeClient.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("placeholder", p.Name).Error("CRIU group restore: could not delete the placeholder")
		}
	}
}

// recordFailure records on group record d a restore of checkpoint id that
// failed (restoreFailed) or did not get to run. A record of another
// capture is left as it is.
func recordFailure(d map[string]string, id string, restoreFailed bool, cause error) bool {
	if d["state"] != criuGroupStateComplete || !recordOf(d, id) {
		return false
	}
	if restoreFailed {
		d["state"], d["reason"] = criuGroupStateRestoreFailed, truncate(cause.Error(), 1024)
		return true
	}
	n, _ := strconv.Atoi(d["stalls"])
	d["stalls"], d["lastStall"] = strconv.Itoa(n+1), truncate(cause.Error(), 1024)
	return true
}

// recordRestored clears the stalls of the capture checkpoint id belongs to:
// its restores run.
func recordRestored(d map[string]string, id string) bool {
	if d["stalls"] == "" || !recordOf(d, id) {
		return false
	}
	delete(d, "stalls")
	return true
}

// groupPlaceholders returns the pods of leader's instance admitted as
// placeholders of group key, leader included, whether running or not;
// restored ones only when includeRestored.
func groupPlaceholders(pods []corev1.Pod, leader *corev1.Pod, key string, includeRestored bool) []*corev1.Pod {
	var out []*corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp == nil && p.Annotations[webhook.CRIUGroupAnnotation] == key &&
			(includeRestored || p.Annotations[webhook.CRIURestoredAnnotation] == "") && groupMember(leader, p) {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
