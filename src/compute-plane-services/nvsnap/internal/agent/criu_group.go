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
	"slices"
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
	// criuGroupStateVerifying is a capture whose instance was stopped for
	// it and is being restored from it. Only that instance's namespace
	// (data "sourceNamespace") restores from it. It becomes complete, for
	// every instance of the configuration, once the pods of a restore of
	// it have served (restored_verify.go); processes that restore but
	// never serve do not verify it.
	criuGroupStateVerifying = "verifying"
	criuGroupStateFailed    = "failed"
	// criuGroupStateRestoreFailed is a capture whose restore failed: its
	// checkpoints are blocked and the next Ready instance captures anew. A
	// capture being verified gets there on its first failed restore; a
	// verified one only after criuGroupMaxRestoreFailures.
	criuGroupStateRestoreFailed = "restore-failed"

	// criuGroupMaxCaptures bounds the captures of one configuration, so a
	// workload that cannot be captured or restored stops paying for it.
	criuGroupMaxCaptures = 3
	// groupAgentBundleKey is the node bundle version (the agent's release)
	// a capture ran with. A failed capture is tried again only under
	// another one: each attempt freezes the live instance, and the same
	// code fails the same workload the same way (a NATS server's MPTCP
	// socket failed three captures of one function in a row, each taking
	// its worker down, 2026-10-26).
	groupAgentBundleKey = "agentBundle"
	// criuGroupMaxStalls bounds the restores of one capture that did not
	// get to run (a rank never scheduled, a checkpoint never fetched).
	// Past it the configuration starts cold, keeping its checkpoints.
	criuGroupMaxStalls = 3
	// criuGroupMaxReplacements bounds the target pods one capture's
	// restores replace for an address collision (see addressCollision).
	// Past it the instance starts cold, keeping its checkpoints.
	criuGroupMaxReplacements = 3
	// criuGroupMaxRestoreFailures bounds the restores of a verified capture
	// that ran and failed since one last served. A verified capture has
	// served once; one failed restore may be the engine's own fault after
	// it was handed back (a crash in a library thread), so the capture is
	// kept and the instance restored again, until failures repeat.
	criuGroupMaxRestoreFailures = 3
	// groupRestoreFailuresKey counts those failures in a group record;
	// groupLastRestoreFailureKey holds the last one's cause.
	groupRestoreFailuresKey    = "restoreFailures"
	groupLastRestoreFailureKey = "lastRestoreFailure"

	lwsGroupKeyLabel = "leaderworkerset.sigs.k8s.io/group-key"

	// criuCaptureAnnotation opts a multi-pod instance into the capture.
	// Multi-pod capture is opt-in: a failed multi-node GPU suspend has
	// taken down the engine it suspended (kimi-k3, 2026-10-08).
	criuCaptureAnnotation = "nvsnap.io/criu-capture"
)

func criuGroupConfigMapName(key string) string { return criuGroupConfigMapPrefix + key }

// recordUsable reports a group record whose checkpoints may be restored:
// complete, or being verified by its own instance's restore.
func recordUsable(d map[string]string) bool {
	return d["state"] == criuGroupStateComplete || (d["state"] == criuGroupStateVerifying && d["sourceNamespace"] != "")
}

// verifyNamespace is the only namespace a record being verified restores
// into; "" for a complete one.
func verifyNamespace(d map[string]string) string {
	if d["state"] == criuGroupStateVerifying {
		return d["sourceNamespace"]
	}
	return ""
}

// parseCRIUGroup reads a complete group record.
func parseCRIUGroup(key string, data map[string]string) (webhook.CRIUGroup, bool) {
	if !recordUsable(data) {
		return webhook.CRIUGroup{}, false
	}
	if n, _ := strconv.Atoi(data["stalls"]); n >= criuGroupMaxStalls {
		return webhook.CRIUGroup{}, false
	}
	size, err := strconv.Atoi(data["size"])
	if err != nil || size < 1 {
		return webhook.CRIUGroup{}, false
	}
	g := webhook.CRIUGroup{Key: key, GPUShare: data["gpushare"] == "true", VerifyNamespace: verifyNamespace(data),
		Checkpoints: make([]string, size), Nodes: make([]string, size), Companion: make([]bool, size), Containers: make([]string, size), Bundles: make([]string, size)}
	for i := range size {
		g.Checkpoints[i] = data[fmt.Sprintf("checkpoint.%d", i)]
		if g.Checkpoints[i] == "" {
			return webhook.CRIUGroup{}, false
		}
		g.Nodes[i] = data[fmt.Sprintf("node.%d", i)]
		g.Companion[i] = data[fmt.Sprintf("companion.%d", i)] == "true"
		g.Containers[i] = data[fmt.Sprintf("container.%d", i)]
		g.Bundles[i] = data[fmt.Sprintf("bundle.%d", i)]
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
		// A companion's alias also names its slots.
		target, slots := cm.Data["alias"], parseSlots(cm.Data)
		if cm, err = a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(target), metav1.GetOptions{}); err != nil {
			return webhook.CRIUGroup{}, false
		}
		g, ok := parseCRIUGroup(target, cm.Data)
		g.Slots = slots
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
// own (an NVCF function's pods are built by NVCA). No Values opts in every
// pod carrying Label, whatever its value: every function of a cluster.
type CaptureOptIn struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// captureOptedIn reports whether a multi-pod instance's rank 0 may be
// captured: annotated nvsnap.io/criu-capture, or matched by the agent's
// configured opt-ins.
func (a *Agent) captureOptedIn(pod *corev1.Pod) bool {
	return pod.Annotations[criuCaptureAnnotation] == "true" || a.optInMatches(pod.Labels)
}

// optInMatches reports whether labels match one of the configured opt-ins.
func (a *Agent) optInMatches(labels map[string]string) bool {
	for _, o := range a.config.CaptureOptIn {
		v, ok := labels[o.Label]
		if !ok {
			continue
		}
		if len(o.Values) == 0 {
			return true
		}
		for _, want := range o.Values {
			if v == want {
				return true
			}
		}
	}
	return false
}

// captureCandidate reports whether a pod without GPUs may be frozen by an
// instance capture (webhook.Mutator.CaptureCandidate): it is opted in
// itself (captureOptedIn), or its namespace is, by the configured opt-in
// labels or the label nvsnap.io/criu-capture=true. A capture freezes the
// companions of the instance it captures, the pods without GPUs of its
// namespace; a service of any other namespace never is.
func (a *Agent) captureCandidate(ctx context.Context, pod *corev1.Pod) bool {
	if a.captureOptedIn(pod) {
		return true
	}
	labels, err := a.namespaceLabels(ctx, pod.Namespace)
	if err != nil {
		a.log.WithError(err).WithField("namespace", pod.Namespace).
			Debug("capture candidate: namespace unreadable; the pod's probes stay as they are")
		return false
	}
	return labels[criuCaptureAnnotation] == "true" || a.optInMatches(labels)
}

func (c *criuAutoRestorer) considerGroupCapture(ctx context.Context, pod *corev1.Pod) {
	if pod.Annotations[webhook.FallbackReasonAnnotation] != "" {
		// Admitted to fall back (criu_fallback.go): its cachedir is
		// captured instead, and a CRIU placeholder would not have it.
		return
	}
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
		if prev, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(criuGroupKey(uri, scope)), metav1.GetOptions{}); err == nil && !c.groupRecaptures(ctx, prev.Data) {
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
	companions, companionContainers, err := a.discoverCompanions(ctx, ranks, log)
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
		// The identity its replacement is admitted by: the stamp admission
		// left, else (pods admitted before the stamp) its spec re-hashed.
		u, ok := p.Annotations[webhook.AdmittedConfigAnnotation], p.Annotations[webhook.AdmittedConfigAnnotation] != ""
		if !ok {
			u, ok = a.specCacheURI(p)
		}
		if !ok {
			log.WithField("companion", p.Name).Warn("CRIU group capture: skipped; no configuration for a companion")
			return false
		}
		uris = append(uris, u)
	}
	key := criuGroupKey(instanceConfigURIOf(uris), scope)
	log = log.WithField("criuGroup", key)
	if prev, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{}); err == nil && !c.groupRecaptures(ctx, prev.Data) {
		c.writeGroupAliases(ctx, key, scope, uris, len(ranks), log)
		log.WithFields(logrus.Fields{"state": prev.Data["state"], "captures": prev.Data["captures"], "source": prev.Data["source"]}).
			Info("CRIU group capture: skipped; the configuration's record says it is captured, being captured, or out of attempts")
		return false
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
	members := append(append([]*corev1.Pod{}, ranks...), companions...)
	if ferr := freezeTolerated(members, a.config.Webhook.CaptureLivenessBudget); ferr != nil {
		// Capturing it would take down the instance it captures.
		log.WithError(ferr).Warn("CRIU group capture: skipped; the instance would not outlast its capture's freeze")
		return false
	}
	if serr := a.agentsSettled(ctx, members); serr != nil {
		// A rollout of the agents restarts them one by one; a member
		// whose agent restarts fails the capture, and the instance,
		// stopped for it, restarts cold.
		log.WithError(serr).Info("CRIU group capture: waiting for the node agents to settle")
		return true
	}
	// Everything that would fail the capture, found before it is claimed:
	// a capture that fails stops the instance and uses an attempt.
	findings := a.capturePreflight(ctx, ranks, companions, companionContainers, log)
	for _, f := range findings {
		fl := log.WithFields(logrus.Fields{"member": f.Pod, "container": f.Container, "check": f.Check, "severity": f.Severity})
		if f.Severity == preflightInfo {
			fl.Info("CRIU group capture preflight: " + f.Detail)
		} else {
			fl.Warn("CRIU group capture preflight: " + f.Detail)
		}
	}
	if b := blocking(findings); len(b) > 0 {
		log.WithField("blocking", len(b)).Warn("CRIU group capture: skipped; the preflight found what would fail it")
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
	// The Services that lead to the instance's pods, read while it serves:
	// once the capture holds the pods, their readiness probes fail and the
	// Services no longer list them, so read after the dump they lead
	// nowhere, and the restore closes every connection through them (seen
	// 2026-10-17: the ranks' etcd and NATS connections, and a worker that
	// shut down when its etcd lease could not reconnect).
	aliases := c.a.instanceServiceAliases(ctx, members)
	cctx, cancel := context.WithTimeout(ctx, criuGroupCaptureTimeout)
	defer cancel()
	t0 := time.Now()
	results, stopped, err := c.checkpointInstance(cctx, ranks, companions, companionContainers, log)
	data := record.Data
	if err != nil {
		log.WithError(err).Error("CRIU group capture failed; this configuration starts fresh")
		data["state"], data["reason"] = criuGroupStateFailed, truncate(err.Error(), groupReasonMax)
		if stopped || sourceMustBeReplacedText(err.Error()) {
			// The instance was stopped for the capture: rather than resume
			// a stopped instance, its controllers replace every pod of it,
			// cold.
			log.Error("CRIU group capture: the instance was stopped for it; replacing its pods, which start fresh")
			c.deleteInstance(context.WithoutCancel(ctx), members, log)
		}
	} else {
		// The dump stopped the instance for good. Its replacements restore
		// from the capture, which verifies it: until then, only they may.
		data["state"], data["sourceNamespace"] = criuGroupStateVerifying, leader.Namespace
		for i, r := range results {
			data[fmt.Sprintf("checkpoint.%d", i)] = r.CheckpointID
			data[fmt.Sprintf("node.%d", i)] = members[i].Spec.NodeName
			if id := webhook.PodBundleID(members[i]); id != "" {
				data[fmt.Sprintf("bundle.%d", i)] = id
			}
			if i >= len(ranks) {
				data[fmt.Sprintf("companion.%d", i)] = "true"
				data[fmt.Sprintf("container.%d", i)] = companionContainers[members[i].Name]
			} else {
				data[fmt.Sprintf("container.%d", i)] = gpuContainer(members[i])
			}
		}
		if c.a.gpushareEnabledFor(ranks[0]) {
			data["gpushare"] = "true"
		}
		if aliases != "" {
			data["serviceAddrs"] = aliases
		}
		log.WithField("duration", time.Since(t0).Round(time.Second).String()).Info("CRIU group capture: recorded; the instance restores from it to verify it")
	}
	started := record.Data["startedAt"]
	if uerr := c.updateGroupRecord(context.WithoutCancel(ctx), key, func(d map[string]string) bool {
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
	}); uerr != nil {
		log.WithError(uerr).Error("CRIU group capture: cannot update the group record")
	} else if data["state"] == criuGroupStateVerifying {
		c.writeGroupAliases(context.WithoutCancel(ctx), key, scope, uris, len(ranks), log)
		nodes := make([]string, len(members))
		for i, m := range members {
			nodes[i] = m.Spec.NodeName
		}
		// Off the capture's path: the replacements' restore starts now.
		go c.a.spreadSmallCheckpoints(context.WithoutCancel(ctx), results, nodes, log)
	}
	if err == nil && data["state"] == criuGroupStateVerifying {
		// Its pods were dumped and are gone; their replacements are
		// admitted as the restore's placeholders (or cold, without the
		// record).
		c.deleteInstance(context.WithoutCancel(ctx), members, log)
	}
	return false
}

// deleteInstance deletes the pods of a captured instance so its
// controllers replace them.
func (c *criuAutoRestorer) deleteInstance(ctx context.Context, members []*corev1.Pod, log *logrus.Entry) {
	for _, p := range members {
		if err := c.a.deletePodRetrying(ctx, p.Namespace, p.Name, p.UID); err != nil {
			log.WithError(err).WithField("pod", p.Name).Error("CRIU group capture: cannot delete a pod of the captured instance")
		}
	}
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
	// A companion is admitted by its own configuration alone: its alias
	// names its slot, or the slots of every companion of that
	// configuration (replicas of one template), which are
	// interchangeable.
	slots := map[string][]string{}
	for i := ranks; i < len(uris); i++ {
		slots[uris[i]] = append(slots[uris[i]], strconv.Itoa(i))
	}
	done := map[string]bool{key: true}
	for i, uri := range uris {
		akey := criuGroupKey(uri, scope)
		if done[akey] {
			continue
		}
		done[akey] = true
		data := map[string]string{"state": criuGroupStateAlias, "alias": key}
		if i >= ranks {
			data["slots"] = strings.Join(slots[uri], ",")
			if len(slots[uri]) == 1 {
				data["slot"] = slots[uri][0] // for agents that read one slot
			}
		}
		alias := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(akey), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
			Data:       data,
		}
		_, err := cms.Create(ctx, alias, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			prev, gerr := cms.Get(ctx, alias.Name, metav1.GetOptions{})
			if gerr != nil || prev.Data["state"] != criuGroupStateAlias ||
				(prev.Data["alias"] == key && prev.Data["slot"] == data["slot"] && prev.Data["slots"] == data["slots"]) {
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
// configuration be captured again by an agent running node bundle
// version bundle: its capture failed under another bundle version, or its
// restore failed, and attempts remain; or its capture stopped without
// finishing.
func criuGroupRecaptures(data map[string]string, now time.Time, bundle string) bool {
	n, _ := strconv.Atoi(data["captures"])
	switch data["state"] {
	case criuGroupStateFailed, criuGroupStateGPUShareFailed:
		return n < criuGroupMaxCaptures && data[groupAgentBundleKey] != bundle
	case criuGroupStateRestoreFailed:
		return n < criuGroupMaxCaptures
	case criuGroupStateVerifying:
		// Its own instance never got to restore from it.
		stalls, _ := strconv.Atoi(data["stalls"])
		return n < criuGroupMaxCaptures && stalls >= criuGroupMaxStalls
	case criuGroupStateCapturing:
		started, err := time.Parse(time.RFC3339, data["startedAt"])
		return n < criuGroupMaxCaptures && (err != nil || now.Sub(started) > criuGroupCaptureTimeout+10*time.Minute)
	}
	return false
}

// groupRecaptures is criuGroupRecaptures with the one fact the record
// cannot hold: whether the namespace of a capture being verified still
// exists. Only that namespace restores from such a capture, so once it is
// gone (the function undeployed, or scaled to zero, before a restore of
// it had served) the capture can never be verified or retried there, and
// the configuration would start cold for good. The next Ready instance
// captures it anew instead.
func (c *criuAutoRestorer) groupRecaptures(ctx context.Context, data map[string]string) bool {
	if criuGroupRecaptures(data, time.Now(), c.a.currentBundle()) {
		return true
	}
	if data["state"] != criuGroupStateVerifying || data["sourceNamespace"] == "" {
		return false
	}
	if n, _ := strconv.Atoi(data["captures"]); n >= criuGroupMaxCaptures {
		return false
	}
	return c.namespaceGone(ctx, data["sourceNamespace"])
}

// namespaceGone reports a namespace that no longer exists or is being
// deleted. Any other error reads as present: a record is only given up on
// what is known.
func (c *criuAutoRestorer) namespaceGone(ctx context.Context, name string) bool {
	ns, err := c.a.kubeClient.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true
	}
	return err == nil && ns.DeletionTimestamp != nil
}

// claimGroupRecord makes the agent the capturer of key: it creates the
// record, or takes over one that may be captured again. It returns nil
// when another agent holds it.
func (c *criuAutoRestorer) claimGroupRecord(ctx context.Context, key string, leader *corev1.Pod, size int) (*corev1.ConfigMap, error) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	data := map[string]string{"state": criuGroupStateCapturing, "size": strconv.Itoa(size), "captures": "1",
		"source": leader.Namespace + "/" + leader.Name, "startedAt": time.Now().UTC().Format(time.RFC3339),
		groupAgentBundleKey: c.a.currentBundle()}
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
	if !c.groupRecaptures(ctx, prev.Data) {
		return nil, nil
	}
	n, _ := strconv.Atoi(prev.Data["captures"])
	data["captures"] = strconv.Itoa(n + 1)
	if prev.Data["reason"] != "" {
		data["previousReason"] = truncate(prev.Data["reason"], groupReasonMax)
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

// checkpointInstance checkpoints the instance, ranks then companions, and
// returns their results in that order. The dump stops the instance for
// good (no --leave-running): a capture freezes it for minutes, and what
// keeps time around a frozen engine (an etcd lease, a watchdog sidecar, a
// liveness probe) would take down an instance resumed after it. Its pods
// are replaced by a restore instead (captureGroup). stopped reports that
// a failed capture got as far as stopping the instance.
func (c *criuAutoRestorer) checkpointInstance(ctx context.Context, ranks, companions []*corev1.Pod, companionContainers map[string]string, log *logrus.Entry) (results []*CheckpointResult, stopped bool, err error) {
	capture := time.Now().UTC().Format("20060102T150405.000000000")
	reqs := make([]CheckpointRequest, 0, len(ranks)+len(companions))
	for _, p := range ranks {
		reqs = append(reqs, CheckpointRequest{Namespace: p.Namespace, PodName: p.Name, ContainerName: gpuContainer(p), CaptureID: capture})
	}
	for _, p := range companions {
		reqs = append(reqs, CheckpointRequest{Namespace: p.Namespace, PodName: p.Name, ContainerName: companionContainers[p.Name],
			CaptureID: capture, Companion: true})
	}
	if len(reqs) == 1 {
		// A failed dump puts the pod back as it was.
		r, cerr := c.a.Checkpoint(ctx, reqs[0])
		if cerr != nil {
			return nil, false, cerr
		}
		return []*CheckpointResult{r}, true, nil
	}
	res, err := c.a.groupCheckpoint(ctx, GroupCheckpointRequest{Members: reqs}, log)
	stopped = res != nil && res.Fabric.Went
	if err != nil {
		return nil, stopped, err
	}
	out := make([]*CheckpointResult, len(res.Members))
	for i, m := range res.Members {
		if m.Result == nil || m.Result.CheckpointID == "" {
			return nil, stopped, fmt.Errorf("member %s/%s returned no checkpoint", m.Namespace, m.PodName)
		}
		out[i] = m.Result
	}
	return out, stopped, nil
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
			if err := doubledSlot(pods.Items, leader, key); err != nil {
				// No restore can use both; replacements are admitted anew.
				c.failGroup(ctx, leader, key, false, err, log)
				return
			}
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

	// Every rank's restore is its pod's to ask for, so each is checked as
	// the leader was (restore_trust.go).
	for _, p := range ranks {
		why, err := c.restoreRefusal(ctx, p)
		if err != nil {
			log.WithError(err).WithField("placeholder", p.Name).Warn("CRIU group restore: cannot verify a rank's restore yet; trying again on the leader's next event")
			c.attempted.Delete("restore/" + string(leader.UID))
			return
		}
		if why != "" {
			log.WithFields(logrus.Fields{"placeholder": p.Name, "reason": why}).
				Warn("CRIU group restore refused: a rank names a restore it may not have; leaving the instance as it is")
			return
		}
	}
	// failGroup deletes the pods after it has blocked their checkpoints. Were
	// the driver to delete them first, their replacements could be admitted
	// as placeholders of the same checkpoints and wait for a restore that
	// never comes.
	req := GroupRestoreRequest{Members: make([]GroupRestoreMember, size), KeepFailedPods: true, ServiceAddrs: c.groupServiceAddrs(ctx, key)}
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
	if ac := new(addressCollision); errors.As(err, &ac) {
		c.replaceColliding(ctx, leader, key, ac, log)
		return
	}
	if err == nil {
		log.WithField("duration", time.Since(t0).Round(time.Millisecond).String()).Info("CRIU group restore: restored")
		uids := make([]string, 0, len(ranks))
		for _, p := range ranks {
			a.markRestored(context.WithoutCancel(ctx), p.Namespace, p.Name, log)
			uids = append(uids, string(p.UID)+"/"+p.Annotations[webhook.CRIURestoreContainerAnnotation])
		}
		id := leader.Annotations[webhook.CRIURestoreAnnotation]
		if uerr := c.updateGroupRecord(context.WithoutCancel(ctx), key, func(d map[string]string) bool {
			// The pods this restore made: the restored-pod watch acts on a
			// rank only when its group record names it.
			return recordRestored(d, id, strings.Join(uids, ","))
		}); uerr != nil && !apierrors.IsNotFound(uerr) {
			log.WithError(uerr).Warn("CRIU group restore: cannot record the restored pods; their engines are not watched")
		}
		return
	}
	if ctx.Err() != nil && !errors.As(err, new(*groupRestoreFailure)) {
		return // the agent stops before the restore ran
	}
	c.failGroup(ctx, leader, key, errors.As(err, new(*groupRestoreFailure)), err, log)
}

// replaceColliding deletes the target pods of an address collision so their
// replacements get other addresses; nothing has been restored. The
// replacements are admitted as placeholders of the same checkpoints, and
// their instance's leader drives the restore again (this one, unless it is
// among them). The group record counts the replacements across leaders;
// past criuGroupMaxReplacements the instance starts cold.
func (c *criuAutoRestorer) replaceColliding(agentCtx context.Context, leader *corev1.Pod, key string, ac *addressCollision, log *logrus.Entry) {
	ctx := context.WithoutCancel(agentCtx)
	id := leader.Annotations[webhook.CRIURestoreAnnotation]
	// A replaced companion's slot is its replacement's: its claim, held for
	// companionClaimTTL after its admission, would leave the replacement
	// no slot and start it cold.
	var claims []string
	for _, name := range ac.Pods {
		ns, pod, _ := strings.Cut(name, "/")
		p, err := c.a.kubeClient.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err != nil || !isCompanion(p) || p.Annotations[webhook.CRIUGroupAnnotation] != key {
			continue
		}
		if o, aerr := strconv.Atoi(p.Annotations[webhook.CRIUGroupOrdinalAnnotation]); aerr == nil {
			claims = append(claims, claimKey(ns, o))
		}
	}
	n := 0
	if err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
		if !recordOf(d, id) {
			return false
		}
		n, _ = strconv.Atoi(d["replacements"])
		n++
		d["replacements"] = strconv.Itoa(n)
		for _, k := range claims {
			delete(d, k)
		}
		return true
	}); err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Warn("CRIU group restore: cannot update the group record")
	}
	log = log.WithFields(logrus.Fields{"replace": ac.Pods, "replacements": n})
	if n > criuGroupMaxReplacements {
		c.failGroup(ctx, leader, key, false, fmt.Errorf("%w; replaced target pods %d times", ac, n-1), log)
		return
	}
	log.Warn("CRIU group restore: a target pod got an address another pod of the instance had at capture; replacing it before restoring")
	for _, name := range ac.Pods {
		ns, pod, _ := strings.Cut(name, "/")
		if err := c.a.kubeClient.CoreV1().Pods(ns).Delete(ctx, pod, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("placeholder", name).Error("CRIU group restore: could not replace the target pod")
		}
	}
	if slices.Contains(ac.Pods, leader.Namespace+"/"+leader.Name) {
		return // the leader's replacement drives the restore
	}
	// The same leader drives it again once the replacements run.
	c.attempted.Delete("restore/" + string(leader.UID))
	c.considerGroupRestore(agentCtx, leader)
}

// groupRestoredPodsKey in a group record lists the pods its last restore
// made, as UID/container, comma-separated.
const groupRestoredPodsKey = "restoredPods"

// groupRestoredContainer returns the container the agent restored into
// pod uid as a rank of group record key, per the record, or "" when the
// record names no such pod.
func (c *criuAutoRestorer) groupRestoredContainer(ctx context.Context, key string, uid types.UID) (string, error) {
	cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	for _, e := range strings.Split(cm.Data[groupRestoredPodsKey], ",") {
		if u, container, ok := strings.Cut(e, "/"); ok && u == string(uid) && container != "" {
			return container, nil
		}
	}
	return "", nil
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
	// A member's checkpoint is blocked only when it is a capture of the
	// pods' tenant scope: the annotations are the pods' to write, and a
	// pod naming another tenant's checkpoint must not get it blocked.
	ids := map[string]bool{}
	for _, p := range append([]*corev1.Pod{leader}, members...) {
		id := p.Annotations[webhook.CRIURestoreAnnotation]
		if id == "" || ids[id] {
			continue
		}
		if why, err := c.restoreRefusal(ctx, p); err != nil || why != "" {
			log.WithError(err).WithFields(logrus.Fields{"placeholder": p.Name, "checkpoint": id, "reason": why}).
				Warn("CRIU group restore: not blocking a checkpoint the pod may not restore")
			continue
		}
		ids[id] = true
	}
	// The record and the checkpoint are named by the pod's annotations:
	// record the failure only for a pod that may act under that record and
	// restore that checkpoint (restore_trust.go), or a pod could add
	// failures to another tenant's capture and have its restores start cold.
	why, rerr := c.groupRecordRefusal(ctx, leader, key)
	mayRecord := rerr == nil && why == "" && ids[leaderID]
	if !mayRecord {
		log.WithError(rerr).WithField("reason", why).Warn("CRIU group restore: not recording the failure on a group record the pod may not act under")
	}
	switch {
	case !restoreFailed:
		log.WithError(cause).Warn("CRIU group restore did not run; deleting its pods so their replacements try again")
		if !mayRecord {
			break
		}
		if err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
			return recordFailure(d, leaderID, false, cause)
		}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Warn("CRIU group restore: cannot update the group record")
		}
	case !mayRecord || c.failedRestoreBlocks(ctx, key, leaderID, cause, log):
		// Without a record it may act under, a failed restore blocks the
		// pods' own checkpoints, as when the record cannot be read.
		log.WithError(cause).Error("CRIU group restore failed; blocking its checkpoints and deleting its pods so their replacements start fresh")
		for id := range ids {
			if id == "" {
				continue
			}
			if err := c.markFailed(ctx, id, cause); err != nil {
				log.WithError(err).WithField("checkpoint", id).Error("CRIU group restore: could not block the checkpoint")
			}
		}
	default:
		log.WithError(cause).Error("CRIU group restore failed; its capture was verified, so it is kept and the replacements restore from it again")
	}
	for _, p := range members {
		if err := c.a.deletePodRetrying(ctx, p.Namespace, p.Name, p.UID); err != nil {
			log.WithError(err).WithField("placeholder", p.Name).Error("CRIU group restore: could not delete the placeholder")
		}
	}
}

// failedRestoreBlocks records on group record key that a restore of
// checkpoint id ran and failed, and reports whether its checkpoints are to
// be blocked: always but for a verified capture of this record that has
// failures to spare. When the record cannot be read or updated, they are.
func (c *criuAutoRestorer) failedRestoreBlocks(ctx context.Context, key, id string, cause error, log *logrus.Entry) bool {
	if key == "" {
		return true
	}
	blocked := true
	err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
		blocked = true
		if !recordFailure(d, id, true, cause) {
			return false
		}
		blocked = d["state"] == criuGroupStateRestoreFailed
		return true
	})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.WithError(err).Warn("CRIU restore: cannot update the group record")
		}
		return true
	}
	return blocked
}

// recordFailure records on group record d a restore of checkpoint id that
// failed (restoreFailed) or did not get to run. A restore that failed ends
// a capture being verified, and a verified one once it has failed
// criuGroupMaxRestoreFailures times since a restore of it last served. A
// record of another capture is left as it is.
func recordFailure(d map[string]string, id string, restoreFailed bool, cause error) bool {
	if !recordUsable(d) || !recordOf(d, id) {
		return false
	}
	if restoreFailed {
		if d["state"] == criuGroupStateComplete {
			n, _ := strconv.Atoi(d[groupRestoreFailuresKey])
			if n+1 < criuGroupMaxRestoreFailures {
				d[groupRestoreFailuresKey], d[groupLastRestoreFailureKey] = strconv.Itoa(n+1), truncate(cause.Error(), groupReasonMax)
				return true
			}
		}
		delete(d, groupRestoreFailuresKey)
		delete(d, groupLastRestoreFailureKey)
		d["state"], d["reason"] = criuGroupStateRestoreFailed, truncate(cause.Error(), groupReasonMax)
		return true
	}
	n, _ := strconv.Atoi(d["stalls"])
	d["stalls"], d["lastStall"] = strconv.Itoa(n+1), truncate(cause.Error(), groupReasonMax)
	return true
}

// recordRestored records a restore of the capture checkpoint id belongs
// to, which made pods (groupRestoredPodsKey entries, uid/container): its
// stalls are cleared as its restores run. A capture being verified stays
// so until those pods serve (restored_verify.go).
func recordRestored(d map[string]string, id, pods string) bool {
	if !recordOf(d, id) {
		return false
	}
	changed := false
	if d[groupRestoredPodsKey] != pods {
		d[groupRestoredPodsKey], changed = pods, true
	}
	if d["stalls"] != "" || d["replacements"] != "" {
		delete(d, "stalls")
		delete(d, "replacements")
		changed = true
	}
	return changed
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

// groupMemberErrorMax bounds one member's error in a group's failure, and
// groupReasonMax a failure recorded in a group record: every member's
// error is kept, not the first member's alone. One member's own failure
// (a NATS server's MPTCP socket) was hidden behind another's abort, which
// filled the record's whole reason (2026-10-26).
const (
	groupMemberErrorMax = 768
	groupReasonMax      = 16 << 10
)

// memberFailure is one member's part of a group's failure.
func memberFailure(name, err string) string {
	return name + ": " + truncate(err, groupMemberErrorMax)
}

// membersFailed is the failure of a group operation (what, in session)
// that failed members of total failed, each named with its own error.
func membersFailed(what, session string, total int, failed []string) error {
	return fmt.Errorf("%s %s: %d of %d members failed: %s", what, session, len(failed), total, strings.Join(failed, "; "))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// groupServiceAddrs returns the Service aliases group key's capture
// recorded (serviceAliases), "" when there are none or the record cannot
// be read.
func (c *criuAutoRestorer) groupServiceAddrs(ctx context.Context, key string) string {
	cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return cm.Data["serviceAddrs"]
}
