// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A configuration CRIU cannot serve falls back to the caches
// (docs/design/CRIU-CAPTURE-FALLBACK.md §2.2, §2.6, §2.7):
//   - the webhook asks criuFallbackFor whether the configuration of a pod
//     it admits falls back, and for the cachedir capture of its rank;
//   - the agent marks a configuration whose pods do not start under the
//     gpushare library (considerGPUShareFailure);
//   - the agent on a fallback rank's node captures that rank's cachedir
//     into the checkpoint directory (considerFallbackCapture), and the
//     agent on the node of a pod seeded from a capture fetches it there
//     the way it fetches a checkpoint (considerFallbackFetch).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// criuGroupStateGPUShareFailed is a configuration whose pods do not
	// start under the gpushare library. groupGPUShareFailureKey holds the
	// last failed start; groupGPUSharePreviousKey the state it replaced
	// ("" for a record the mark created); groupGPUShareClearedKey the
	// bundle under which the mark was cleared, because pods without the
	// library failed the same way.
	criuGroupStateGPUShareFailed = webhook.FallbackGPUShareFailed
	groupGPUShareFailureKey      = "gpushareFailure"
	groupGPUSharePreviousKey     = "gpusharePreviousState"
	groupGPUShareClearedKey      = "gpushareCleared"
	// gpushareFailedStarts is how many failed starts of a GPU container,
	// its pod never Ready, mark the configuration.
	gpushareFailedStarts = 2

	// cachedirRecordPrefix names the record of a configuration's cachedir
	// captures, one entry set per rank: rank.<n> (capturing, complete),
	// node.<n>, startedAt.<n>, bytes.<n>.
	cachedirRecordPrefix = "nvsnap-cachedir-"
	cachedirCapturing    = "capturing"
	cachedirComplete     = "complete"
	// cachedirCaptureStale is how long a rank may be capturing before
	// another pod of it may take it over.
	cachedirCaptureStale = 30 * time.Minute
	// cachedirManifestFile, beside the tree in a capture, lists what the
	// capture holds. It is written last: a capture is complete when it
	// is there and every file it lists has its size.
	cachedirManifestFile = webhook.FallbackManifestFile
)

// cachedirWarmup is how long a fallback pod serves before its cachedir is
// captured, and cachedirSettle how long its tree must stay unchanged. The
// engine's first requests finish its lazy compiles. cachedirPodsRoot is
// the kubelet's pod directory in the agent's view of the host, where a
// pod's emptyDirs are. Variables for tests.
var (
	cachedirWarmup   = 60 * time.Second
	cachedirSettle   = 20 * time.Second
	cachedirPodsRoot = "/host/var/lib/kubelet/pods"
)

func cachedirRecordName(key string) string { return cachedirRecordPrefix + key }

// criuFallbackReason is the reason a configuration falls back, from its
// group record, for an agent of node bundle version bundle; "" when it
// does not. A capture or restore that failed, or a workload that does not
// start under the library, falls back once CRIU will not try it again
// under this bundle: a new bundle gets one more try.
func criuFallbackReason(d map[string]string, now time.Time, bundle string) string {
	switch d["state"] {
	case criuGroupStateFailed, criuGroupStateRestoreFailed, criuGroupStateGPUShareFailed:
	default:
		return ""
	}
	if bundle == "" || criuGroupRecaptures(d, now, bundle) {
		return ""
	}
	return d["state"]
}

// criuFallbackFor is the webhook's view: why the configuration uri falls
// back in namespace ns's scope, and the complete cachedir capture of rank
// ordinal.
func (a *Agent) criuFallbackFor(ctx context.Context, ns, uri string, ordinal int) (webhook.CRIUFallback, bool) {
	scope, err := a.criuGroupScope(ctx, ns)
	if err != nil {
		return webhook.CRIUFallback{}, false
	}
	key := criuGroupKey(uri, scope)
	data, err := a.groupRecordData(ctx, key)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			a.log.WithError(err).WithField("criuGroup", key).Warn("CRIU fallback: cannot read the group record")
		}
		return webhook.CRIUFallback{}, false
	}
	reason := criuFallbackReason(data, time.Now(), a.currentBundle())
	if reason == "" {
		return webhook.CRIUFallback{}, false
	}
	fb := webhook.CRIUFallback{Reason: reason}
	rec, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, cachedirRecordName(key), metav1.GetOptions{})
	if err == nil && rec.Data["rank."+strconv.Itoa(ordinal)] == cachedirComplete {
		fb.Capture = webhook.FallbackCaptureID(key, ordinal)
	}
	return fb, true
}

// groupRecordData returns the group record of key, through its alias when
// it is one: the instance's record a rank of several configurations
// belongs to.
func (a *Agent) groupRecordData(ctx context.Context, key string) (map[string]string, error) {
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
	cm, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if cm.Data["state"] != criuGroupStateAlias {
		return cm.Data, nil
	}
	if cm, err = cms.Get(ctx, criuGroupConfigMapName(cm.Data["alias"]), metav1.GetOptions{}); err != nil {
		return nil, err
	}
	return cm.Data, nil
}

// podFallbackKey returns the group record key of a pod's own configuration
// in its namespace's scope, and its rank, from the identity the webhook
// stamped. The scope is the agent's to read, so a pod names only a key of
// its own tenant.
func (a *Agent) podFallbackKey(ctx context.Context, pod *corev1.Pod) (key string, ordinal int, err error) {
	uri := pod.Annotations[cacheURIAnnotation]
	if uri == "" {
		return "", 0, errors.New("the pod carries no configuration")
	}
	ordinal, err = strconv.Atoi(pod.Annotations[modelvolume.CacheOrdinalAnnotation])
	if err != nil || ordinal < 0 {
		return "", 0, fmt.Errorf("the pod carries no rank: %q", pod.Annotations[modelvolume.CacheOrdinalAnnotation])
	}
	scope, err := a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		return "", 0, err
	}
	return criuGroupKey(uri, scope), ordinal, nil
}

// considerFallback runs the fallback's part of the agent for a pod on this
// node.
func (c *criuAutoRestorer) considerFallback(ctx context.Context, pod *corev1.Pod) {
	if pod.DeletionTimestamp != nil {
		return
	}
	if rootfsonly.IsPodReady(pod) {
		c.seenReady.Store(pod.UID, struct{}{})
	}
	c.considerGPUShareFailure(ctx, pod)
	c.considerFallbackCapture(ctx, pod)
	c.considerFallbackFetch(ctx, pod)
}

// fallbackAttempts are the attempted-map prefixes of the fallback's work
// on a pod.
var fallbackAttempts = []string{"gpushare-start/", "cachedir-capture/", "cachedir-fetch/"}

// forgetFallback drops what the fallback kept about a deleted pod.
func (c *criuAutoRestorer) forgetFallback(uid types.UID) {
	c.seenReady.Delete(uid)
	for _, p := range fallbackAttempts {
		c.attempted.Delete(p + string(uid))
	}
}

// failedStart returns the last failed termination of the pod's GPU
// container once it has failed gpushareFailedStarts times, and the pod is
// not Ready: "" otherwise.
func failedStart(pod *corev1.Pod) string {
	name := gpuContainer(pod)
	if name == "" || rootfsonly.IsPodReady(pod) {
		return ""
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name != name {
			continue
		}
		t := cs.LastTerminationState.Terminated
		failures := int(cs.RestartCount)
		if cur := cs.State.Terminated; cur != nil && (cur.ExitCode != 0 || cur.Signal != 0) {
			t = cur
			failures++
		}
		if t == nil || (t.ExitCode == 0 && t.Signal == 0) || failures < gpushareFailedStarts {
			return ""
		}
		return fmt.Sprintf("container %s failed %d times; last: %s, exit code %d, signal %d", name, failures, t.Reason, t.ExitCode, t.Signal)
	}
	return ""
}

// considerGPUShareFailure marks the configuration of a pod that does not
// start under the gpushare library, and deletes the pod so its replacement
// is admitted without it; and clears the mark when a pod admitted without
// the library fails the same way.
func (c *criuAutoRestorer) considerGPUShareFailure(ctx context.Context, pod *corev1.Pod) {
	if pod.Annotations[webhook.CRIURestoreAnnotation] != "" || pod.Annotations[webhook.CRIUGroupAnnotation] != "" {
		return // a placeholder: a failed restore is the restorer's
	}
	if _, seen := c.seenReady.Load(pod.UID); seen {
		return
	}
	failure := failedStart(pod)
	if failure == "" {
		return
	}
	withLib := c.a.gpushareEnabledFor(pod)
	without := pod.Annotations[webhook.FallbackReasonAnnotation] == webhook.FallbackGPUShareFailed && !withLib
	if !withLib && !without {
		return
	}
	attempt := "gpushare-start/" + string(pod.UID)
	if _, done := c.attempted.LoadOrStore(attempt, struct{}{}); done {
		return
	}
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "failure": failure})
	key, _, err := c.a.podFallbackKey(ctx, pod)
	if err != nil {
		log.WithError(err).Info("gpushare: a pod failed to start; its configuration cannot be named, so it is not marked")
		return
	}
	// A failure to read or write the record is tried again on the pod's
	// next event.
	retryLater := func() { c.attempted.Delete(attempt) }
	bundle := c.a.currentBundle()
	if bundle == "" {
		log.Warn("gpushare: a pod failed to start; the node bundle version is unknown, so its configuration is not marked")
		return
	}
	log = log.WithField("criuGroup", key)
	if without {
		cleared, err := c.clearGPUShareFailed(ctx, key, bundle, failure)
		switch {
		case err != nil:
			retryLater()
			log.WithError(err).Warn("gpushare: cannot clear the configuration's mark")
		case cleared:
			log.Warn("gpushare: the pod fails without the library as it did with it; the library was not the cause, so the configuration gets it again")
		}
		return
	}
	marked, err := c.markGPUShareFailed(ctx, key, bundle, failure)
	if err != nil {
		retryLater()
		log.WithError(err).Warn("gpushare: cannot mark the configuration; the pod is left as it is")
		return
	}
	if !marked {
		return
	}
	log.Warn("gpushare: the pod never started under the library; its configuration falls back to the cachedir without it, and the pod is replaced")
	if err := c.a.deletePodRetrying(ctx, pod.Namespace, pod.Name, pod.UID); err != nil {
		log.WithError(err).Warn("gpushare: cannot delete the pod; its replacement will be admitted without the library")
	}
}

// markGPUShare records on group record data d that the configuration does
// not start under the library with bundle. A usable record (its pods
// restore and never start under the library), one being captured, or one
// whose mark was cleared under bundle, is left alone.
func markGPUShare(d map[string]string, bundle, failure string, now time.Time) bool {
	if recordUsable(d) || d[groupGPUShareClearedKey] == bundle {
		return false
	}
	switch d["state"] {
	case "", criuGroupStateFailed, criuGroupStateRestoreFailed:
		d[groupGPUSharePreviousKey] = d["state"]
	case criuGroupStateGPUShareFailed:
		if d[groupAgentBundleKey] == bundle {
			d[groupGPUShareFailureKey] = truncate(failure, groupReasonMax)
			return true
		}
	case criuGroupStateCapturing:
		started, err := time.Parse(time.RFC3339, d["startedAt"])
		if err == nil && now.Sub(started) <= criuGroupCaptureTimeout+10*time.Minute {
			return false
		}
		d[groupGPUSharePreviousKey] = criuGroupStateFailed
	default:
		return false
	}
	d["state"], d[groupAgentBundleKey] = criuGroupStateGPUShareFailed, bundle
	d[groupGPUShareFailureKey] = truncate(failure, groupReasonMax)
	return true
}

// clearGPUShare undoes the mark of bundle on d.
func clearGPUShare(d map[string]string, bundle, failure string) bool {
	if d["state"] != criuGroupStateGPUShareFailed || d[groupAgentBundleKey] != bundle {
		return false
	}
	d["state"] = d[groupGPUSharePreviousKey]
	if d["state"] == "" {
		// The mark made the record: it records nothing else.
		d["state"] = criuGroupStateFailed
	}
	d[groupGPUShareClearedKey] = bundle
	d[groupGPUShareFailureKey] = truncate("without the library too: "+failure, groupReasonMax)
	delete(d, groupGPUSharePreviousKey)
	return true
}

// markGPUShareFailed marks the group record of key (its instance's
// through an alias), creating it when there is none.
func (c *criuAutoRestorer) markGPUShareFailed(ctx context.Context, key, bundle, failure string) (bool, error) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	target := key
	if cm, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{}); err == nil && cm.Data["state"] == criuGroupStateAlias {
		target = cm.Data["alias"]
	} else if apierrors.IsNotFound(err) {
		d := map[string]string{}
		markGPUShare(d, bundle, failure, time.Now())
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName(key), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
			Data:       d,
		}, metav1.CreateOptions{})
		if err == nil {
			return true, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	marked := false
	err := c.updateGroupRecord(ctx, target, func(d map[string]string) bool {
		marked = markGPUShare(d, bundle, failure, time.Now())
		return marked
	})
	return marked, err
}

// clearGPUShareFailed clears the mark of bundle on the group record of key
// (its instance's through an alias).
func (c *criuAutoRestorer) clearGPUShareFailed(ctx context.Context, key, bundle, failure string) (bool, error) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	target := key
	cm, err := cms.Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cm.Data["state"] == criuGroupStateAlias {
		target = cm.Data["alias"]
	}
	cleared := false
	err = c.updateGroupRecord(ctx, target, func(d map[string]string) bool {
		cleared = clearGPUShare(d, bundle, failure)
		return cleared
	})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return cleared, err
}

// cachedirManifest is what a cachedir capture holds, under its tree: the
// regular files with their sizes, the directories (empty ones included)
// and the symbolic links, which a fetch from another node does not carry.
type cachedirManifest struct {
	Files []cachedirFile    `json:"files"`
	Dirs  []string          `json:"dirs,omitempty"`
	Links map[string]string `json:"links,omitempty"`
	Bytes int64             `json:"bytes"`
	Node  string            `json:"node,omitempty"`
	At    time.Time         `json:"capturedAt"`
}

type cachedirFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// claimCachedirRank makes this node the capturer of rank ordinal of key's
// cachedir: it creates the record or takes the rank over when nobody
// captured it or its capture went stale. False when another node holds or
// completed it.
func (c *criuAutoRestorer) claimCachedirRank(ctx context.Context, key string, ordinal int, now time.Time) (bool, error) {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	n := strconv.Itoa(ordinal)
	claim := func(d map[string]string) {
		d["rank."+n], d["node."+n], d["startedAt."+n] = cachedirCapturing, c.a.config.NodeName, now.UTC().Format(time.RFC3339)
		delete(d, "bytes."+n)
	}
	d := map[string]string{}
	claim(d)
	_, err := cms.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cachedirRecordName(key), Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
		Data:       d,
	}, metav1.CreateOptions{})
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}
	rec, err := cms.Get(ctx, cachedirRecordName(key), metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if rec.Data == nil {
		rec.Data = map[string]string{}
	}
	switch rec.Data["rank."+n] {
	case cachedirComplete:
		return false, nil
	case cachedirCapturing:
		started, err := time.Parse(time.RFC3339, rec.Data["startedAt."+n])
		if err == nil && now.Sub(started) < cachedirCaptureStale {
			return false, nil
		}
	}
	claim(rec.Data)
	// The resource version makes the claim a compare-and-swap.
	if _, err := cms.Update(ctx, rec, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// endCachedirRank records the end of this node's capture of rank ordinal:
// complete with its bytes, or released for another pod to capture.
func (c *criuAutoRestorer) endCachedirRank(ctx context.Context, key string, ordinal int, bytes int64, ok bool) error {
	cms := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace())
	n := strconv.Itoa(ordinal)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		rec, err := cms.Get(ctx, cachedirRecordName(key), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if rec.Data["rank."+n] != cachedirCapturing || rec.Data["node."+n] != c.a.config.NodeName {
			return nil // taken over
		}
		if ok {
			rec.Data["rank."+n], rec.Data["bytes."+n] = cachedirComplete, strconv.FormatInt(bytes, 10)
		} else {
			delete(rec.Data, "rank."+n)
			delete(rec.Data, "node."+n)
			delete(rec.Data, "startedAt."+n)
		}
		_, err = cms.Update(ctx, rec, metav1.UpdateOptions{})
		return err
	})
}

// cachedirSource is the host path, in the agent's view, of a pod's
// cachedir emptyDir.
func cachedirSource(pod *corev1.Pod) (string, error) {
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name != webhook.CacheDirVolumeName {
			continue
		}
		if v.EmptyDir == nil {
			return "", fmt.Errorf("volume %s is not an emptyDir", v.Name)
		}
		return filepath.Join(cachedirPodsRoot, string(pod.UID), "volumes", "kubernetes.io~empty-dir", v.Name), nil
	}
	return "", fmt.Errorf("the pod has no %s volume", webhook.CacheDirVolumeName)
}

// considerFallbackCapture captures the cachedir of a fallback pod's rank
// once the pod has served for the warm-up and its tree has settled.
func (c *criuAutoRestorer) considerFallbackCapture(ctx context.Context, pod *corev1.Pod) {
	if pod.Annotations[webhook.FallbackReasonAnnotation] == "" || pod.Annotations[webhook.FallbackCaptureAnnotation] != "" ||
		!rootfsonly.IsPodReady(pod) {
		return
	}
	if _, busy := c.attempted.LoadOrStore("cachedir-capture/"+string(pod.UID), struct{}{}); busy {
		return
	}
	go func() {
		again, err := c.captureFallbackRank(ctx, pod)
		log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name})
		if err != nil {
			log.WithError(err).Warn("CRIU fallback: cachedir capture failed")
		}
		if again {
			c.attempted.Delete("cachedir-capture/" + string(pod.UID))
		}
	}()
}

// captureFallbackRank captures the pod's rank. again reports a capture
// that may succeed on a later event (the tree was still changing).
func (c *criuAutoRestorer) captureFallbackRank(ctx context.Context, pod *corev1.Pod) (again bool, err error) {
	key, ordinal, err := c.a.podFallbackKey(ctx, pod)
	if err != nil {
		return false, err
	}
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "criuGroup": key, "rank": ordinal})
	rec, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, cachedirRecordName(key), metav1.GetOptions{})
	if err == nil && rec.Data["rank."+strconv.Itoa(ordinal)] == cachedirComplete {
		return false, nil // another pod of the rank was captured
	}
	src, err := cachedirSource(pod)
	if err != nil {
		return false, err
	}
	select {
	case <-time.After(cachedirWarmup):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	before, err := treeSignature(src)
	if err != nil {
		return true, err
	}
	select {
	case <-time.After(cachedirSettle):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if after, err := treeSignature(src); err != nil || after != before {
		log.Info("CRIU fallback: the cachedir is still changing; capturing on a later event")
		return true, err
	}
	ok, err := c.claimCachedirRank(ctx, key, ordinal, time.Now())
	if err != nil || !ok {
		return false, err
	}
	id := webhook.FallbackCaptureID(key, ordinal)
	start := time.Now()
	man, err := c.a.writeCachedirCapture(src, id)
	if err == nil {
		err = c.a.markCachedirReady(id)
	}
	if err != nil {
		if endErr := c.endCachedirRank(context.WithoutCancel(ctx), key, ordinal, 0, false); endErr != nil {
			log.WithError(endErr).Warn("CRIU fallback: cannot release the rank")
		}
		return false, err
	}
	if c.a.config.CatalogURL != "" {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if _, err := c.a.registerCheckpointInCatalog(rctx, id, pod.Namespace, pod.Name, gpuContainer(pod), "", man.Bytes, time.Since(start).Seconds(), false, CatalogInfo{}); err != nil {
			log.WithError(err).Warn("CRIU fallback: cannot register the capture; other nodes cannot fetch it")
		} else if err := c.a.registerAsPeer(rctx, id); err != nil {
			log.WithError(err).Warn("CRIU fallback: cannot advertise this node as the capture's source")
		}
		cancel()
	}
	if err := c.endCachedirRank(context.WithoutCancel(ctx), key, ordinal, man.Bytes, true); err != nil {
		return false, fmt.Errorf("record the rank complete: %w", err)
	}
	log.WithFields(logrus.Fields{"capture": id, "bytes": man.Bytes, "files": len(man.Files), "elapsed": time.Since(start).Round(time.Millisecond)}).
		Info("CRIU fallback: captured the rank's cachedir")
	return false, nil
}

// treeSignature summarizes a tree for the settle check: its entries, bytes
// and latest modification.
func treeSignature(root string) (string, error) {
	var files, bytes, latest int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		if t := info.ModTime().UnixNano(); t > latest {
			latest = t
		}
		return nil
	})
	return fmt.Sprintf("%d/%d/%d", files, bytes, latest), err
}

// writeCachedirCapture copies the cachedir at src into capture id under
// the checkpoint directory, in place (a pod on this node may mount the
// directory already), with owners and modes; the manifest last. The pod
// can write src while it is read, so it is read through an os.Root: no
// path resolves outside it. Its symbolic links are recorded in the
// manifest only, never made in the checkpoint directory, which the peers
// serve; the seed makes them in the pod's own cachedir.
func (a *Agent) writeCachedirCapture(src, id string) (cachedirManifest, error) {
	dir := filepath.Join(a.config.CheckpointDir, id)
	tree := filepath.Join(dir, webhook.FallbackCaptureTree)
	_ = os.Remove(filepath.Join(dir, cachedirManifestFile))
	if err := os.RemoveAll(tree); err != nil {
		return cachedirManifest{}, err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return cachedirManifest{}, err
	}
	defer func() { _ = root.Close() }()
	man := cachedirManifest{Links: map[string]string{}, Node: a.config.NodeName, At: time.Now().UTC()}
	err = fs.WalkDir(root.FS(), ".", func(rel string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		dst := filepath.Join(tree, filepath.FromSlash(rel))
		switch {
		case info.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if rel != "." {
				man.Dirs = append(man.Dirs, rel)
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := root.Readlink(rel)
			if err != nil {
				return err
			}
			man.Links[rel] = target
			return nil
		case info.Mode().IsRegular():
			n, err := copyConfinedFile(root, rel, dst, info)
			if err != nil {
				return err
			}
			if n < 0 {
				return nil // no longer a regular file
			}
			man.Files = append(man.Files, cachedirFile{Path: rel, Size: n})
			man.Bytes += n
		default:
			return nil // sockets, fifos: nothing an engine reads back
		}
		keepOwner(dst, info)
		return nil
	})
	if err != nil {
		return cachedirManifest{}, err
	}
	b, err := json.Marshal(man)
	if err != nil {
		return cachedirManifest{}, err
	}
	tmp := filepath.Join(dir, "."+cachedirManifestFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return cachedirManifest{}, err
	}
	return man, os.Rename(tmp, filepath.Join(dir, cachedirManifestFile))
}

// copyConfinedFile copies rel, opened through root without blocking on
// anything but a regular file, to dst; it returns the bytes copied, -1
// when rel is no longer a regular file.
func copyConfinedFile(root *os.Root, rel, dst string, info os.FileInfo) (int64, error) {
	in, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() {
		return -1, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// keepOwner gives dst the owner and mode src had; best effort, as the seed
// opens the copy to the engine's user anyway.
func keepOwner(dst string, info os.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(dst, int(st.Uid), int(st.Gid))
	}
	if info.Mode()&os.ModeSymlink == 0 {
		_ = os.Chmod(dst, info.Mode().Perm())
	}
}

// readCachedirManifest returns the manifest of capture dir.
func readCachedirManifest(dir string) (cachedirManifest, error) {
	var man cachedirManifest
	b, err := os.ReadFile(filepath.Join(dir, cachedirManifestFile))
	if err != nil {
		return man, err
	}
	err = json.Unmarshal(b, &man)
	return man, err
}

// completeCachedirCapture checks capture dir against its manifest, and
// makes the directories a fetch does not carry. Its links are the seed's
// to make.
func completeCachedirCapture(dir string) error {
	man, err := readCachedirManifest(dir)
	if err != nil {
		return err
	}
	tree := filepath.Join(dir, webhook.FallbackCaptureTree)
	for _, f := range man.Files {
		p, err := joinWithinRoot(tree, f.Path)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || fi.Size() != f.Size {
			return fmt.Errorf("%s: %d bytes of mode %v, the capture has %d", f.Path, fi.Size(), fi.Mode(), f.Size)
		}
	}
	for _, d := range man.Dirs {
		p, err := joinWithinRoot(tree, d)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// cachedirReadyDir is the per-capture directory a seeding pod mounts.
func (a *Agent) cachedirReadyDir(id string) string {
	return filepath.Join(a.config.CheckpointDir, webhook.FallbackReadySubdir, id)
}

// markCachedirReady tells the pods seeded from capture id on this node
// that it is whole.
func (a *Agent) markCachedirReady(id string) error {
	dir := a.cachedirReadyDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, webhook.FallbackReadyFile), nil, 0o644)
}

// considerFallbackFetch brings the capture a pod is seeded from to this
// node and marks it ready. The pod may name only its own rank's capture.
func (c *criuAutoRestorer) considerFallbackFetch(ctx context.Context, pod *corev1.Pod) {
	id := pod.Annotations[webhook.FallbackCaptureAnnotation]
	if id == "" {
		return
	}
	if _, done := c.attempted.LoadOrStore("cachedir-fetch/"+string(pod.UID), struct{}{}); done {
		return
	}
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "capture": id})
	key, ordinal, err := c.a.podFallbackKey(ctx, pod)
	if err != nil || webhook.FallbackCaptureID(key, ordinal) != id {
		log.WithError(err).Warn("CRIU fallback: the pod names a capture that is not its own rank's; not fetching it")
		return
	}
	if _, err := os.Stat(filepath.Join(c.a.cachedirReadyDir(id), webhook.FallbackReadyFile)); err == nil {
		return
	}
	go func() {
		start := time.Now()
		if err := c.a.fetchCachedirCapture(ctx, id); err != nil {
			log.WithError(err).Warn("CRIU fallback: cannot bring the capture to this node; the pod starts without it")
			return
		}
		log.WithField("elapsed", time.Since(start).Round(time.Millisecond)).Info("CRIU fallback: the capture is on this node")
	}()
}

// fetchCachedirCapture fetches capture id like a checkpoint, checks it
// against its manifest and marks it ready.
func (a *Agent) fetchCachedirCapture(ctx context.Context, id string) error {
	if err := a.EnsureLocal(ctx, id); err != nil {
		return err
	}
	if err := completeCachedirCapture(filepath.Join(a.config.CheckpointDir, id)); err != nil {
		return fmt.Errorf("the fetched capture is not whole: %w", err)
	}
	return a.markCachedirReady(id)
}

// cachedirCaptureComplete reports a cachedir capture whose manifest is in
// place: written last on the capturing node, and on a fetching node
// checked by fetchCachedirCapture before the capture is marked ready.
func cachedirCaptureComplete(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, cachedirManifestFile))
	return err == nil && fi.Size() > 0 && strings.HasPrefix(filepath.Base(dir), webhook.FallbackCapturePrefix)
}
