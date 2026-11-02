// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// What a pod may ask of the restorer. A pod's restore annotations are its
// author's to write: a pod the webhook never saw (nvsnap.io/inject=false)
// or one edited after admission can name any checkpoint or group record.
// They say what the pod asks for, never what it may have. So the agent
// decides from what it owns:
//   - a lone restore runs only from a capture of the pod's own tenant
//     scope (the capture record, the rule the webhook applies);
//   - a group restore runs only under a group record bound to the pod's
//     scope, each rank from the checkpoint the record names for it;
//   - a restored pod is watched, and its checkpoints blocked, only when
//     the agent recorded restoring it: the record says which checkpoint.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// restoreRecordsDir holds the agent's record of each pod it restored on
// its own, one file per pod UID, beside the checkpoints (a dot directory,
// which no checkpoint listing takes for a checkpoint).
const restoreRecordsDir = ".restored"

// restoreRecord is what the agent restored into a pod.
type restoreRecord struct {
	Checkpoint string    `json:"checkpoint"`
	Container  string    `json:"container"`
	Namespace  string    `json:"namespace"`
	Pod        string    `json:"pod"`
	At         time.Time `json:"at"`
}

func (c *criuAutoRestorer) restoreRecordPath(uid types.UID) string {
	return filepath.Join(c.a.config.CheckpointDir, restoreRecordsDir, string(uid)+".json")
}

// recordRestore writes the record of a lone restore into pod uid.
func (c *criuAutoRestorer) recordRestore(uid types.UID, rec restoreRecord) error {
	if uid == "" || strings.ContainsAny(string(uid), "/.") {
		return fmt.Errorf("pod UID %q cannot name a record", uid)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := c.restoreRecordPath(uid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// restoredRecordOf returns the record of the lone restore the agent made
// into pod uid, if it made one.
func (c *criuAutoRestorer) restoredRecordOf(uid types.UID) (restoreRecord, bool) {
	var rec restoreRecord
	if uid == "" || strings.ContainsAny(string(uid), "/.") {
		return rec, false
	}
	b, err := os.ReadFile(c.restoreRecordPath(uid))
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.Checkpoint == "" || rec.Container == "" {
		return restoreRecord{}, false
	}
	return rec, true
}

func (c *criuAutoRestorer) dropRestoreRecord(uid types.UID) {
	if uid != "" && !strings.ContainsAny(string(uid), "/.") {
		_ = os.Remove(c.restoreRecordPath(uid))
	}
}

// sweepRestoreRecords drops the records of pods no longer on this node.
func (c *criuAutoRestorer) sweepRestoreRecords(onNode map[types.UID]bool) {
	ents, err := os.ReadDir(filepath.Join(c.a.config.CheckpointDir, restoreRecordsDir))
	if err != nil {
		return
	}
	for _, e := range ents {
		uid := types.UID(strings.TrimSuffix(e.Name(), ".json"))
		if !onNode[uid] {
			_ = os.Remove(filepath.Join(c.a.config.CheckpointDir, restoreRecordsDir, e.Name()))
		}
	}
}

// errRestoreUnverified marks a pod whose claim could not be checked yet
// (the capture record or the namespace unreadable): nothing is done for
// it now, and it is checked again on its next event.
var errRestoreUnverified = errors.New("cannot verify the restore yet")

// loneRestoreRefusal returns why pod may not restore checkpoint id on its
// own, "" when it may, or errRestoreUnverified when that cannot be told.
func (c *criuAutoRestorer) loneRestoreRefusal(ctx context.Context, pod *corev1.Pod, id string) (string, error) {
	scope, err := c.a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		return "", fmt.Errorf("%w: the namespace's tenant scope: %w", errRestoreUnverified, err)
	}
	hash, err := c.checkpointHash(ctx, id)
	if err != nil {
		return "", fmt.Errorf("%w: the checkpoint's hash: %w", errRestoreUnverified, err)
	}
	if hash == "" {
		return "the checkpoint records no content hash, so no capture record vouches for it", nil
	}
	man, found, err := c.captureRecord(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("%w: the capture record: %w", errRestoreUnverified, err)
	}
	if !found {
		return "no capture record names the checkpoint", nil
	}
	if got := webhook.CRIUCheckpointID(man); got != id {
		return fmt.Sprintf("the capture record of its hash names checkpoint %q", got), nil
	}
	return webhook.CRIUScopeMismatch(man, scope), nil
}

// captureRecord reads the capture record of hash from the stores the
// webhook reads it from.
func (c *criuAutoRestorer) captureRecord(ctx context.Context, hash string) (checkpointstore.Manifest, bool, error) {
	var lastErr error
	for _, b := range []checkpointstore.Backend{c.a.captureBackend, c.a.l2Backend} {
		if b == nil {
			continue
		}
		man, err := b.Stat(ctx, hash)
		if err == nil {
			if webhook.CRIUCheckpointID(man) != "" {
				return man, true, nil
			}
			continue
		}
		if !errors.Is(err, checkpointstore.ErrNotFound) {
			lastErr = err
		}
	}
	return checkpointstore.Manifest{}, false, lastErr
}

// checkpointHash returns the content hash checkpoint id was captured
// under: from its metadata on this node, else from the catalog.
func (c *criuAutoRestorer) checkpointHash(ctx context.Context, id string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(c.a.config.CheckpointDir, id, "metadata.json")); err == nil {
		var md CheckpointMetadata
		if json.Unmarshal(b, &md) == nil {
			if md.Hash != "" {
				return md.Hash, nil
			}
			if md.CatalogInfo != nil && md.CatalogInfo.Hash != "" {
				return md.CatalogInfo.Hash, nil
			}
		}
	}
	if c.a.config.CatalogURL == "" {
		return "", fmt.Errorf("checkpoint %s is not on this node and no catalog is configured", id)
	}
	u := fmt.Sprintf("%s/api/v1/checkpoints/%s", c.a.config.CatalogURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := peerHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("catalog returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var row struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&row); err != nil {
		return "", fmt.Errorf("decode the catalog's checkpoint: %w", err)
	}
	return row.Hash, nil
}

// groupKeyRefusal returns why pod may not act under group record key, ""
// when it may, or errRestoreUnverified. A record is keyed by its
// configuration and tenant scope (criuGroupKey); the pod's key must be its
// own configuration's in its namespace's scope, or the instance record its
// own configuration's record names (an instance whose ranks differ in
// configuration).
func (c *criuAutoRestorer) groupKeyRefusal(ctx context.Context, pod *corev1.Pod, key string) (string, error) {
	uri := pod.Annotations[cacheURIAnnotation]
	if uri == "" {
		return "the pod carries no configuration", nil
	}
	scope, err := c.a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		return "", fmt.Errorf("%w: the namespace's tenant scope: %w", errRestoreUnverified, err)
	}
	own := criuGroupKey(uri, scope)
	if own == key {
		return "", nil
	}
	alias, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(own), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "the group record is not its configuration's in its tenant scope", nil
		}
		return "", fmt.Errorf("%w: its configuration's group record: %w", errRestoreUnverified, err)
	}
	if alias.Data["state"] == criuGroupStateAlias && alias.Data["alias"] == key {
		return "", nil
	}
	return "the group record is not its configuration's in its tenant scope", nil
}

// groupRecordRefusal returns why pod may not act under group record key,
// "" when it may, or errRestoreUnverified: a pod with a configuration
// through groupKeyRefusal, a member without one (a companion) through
// memberRefusal on the checkpoint it restores.
func (c *criuAutoRestorer) groupRecordRefusal(ctx context.Context, pod *corev1.Pod, key string) (string, error) {
	if pod.Annotations[cacheURIAnnotation] != "" {
		return c.groupKeyRefusal(ctx, pod, key)
	}
	return c.memberRefusal(ctx, key, pod.Annotations[webhook.CRIURestoreAnnotation])
}

// memberRefusal returns why a pod without a configuration may not restore
// checkpoint id as a member of group record key, "" when it may, or
// errRestoreUnverified: the record must list id among the checkpoints of
// its capture. The record and the capture records are the agent's, so a
// pod can name only a checkpoint an instance was captured with, and
// rankRefusal then holds it to its own tenant scope.
func (c *criuAutoRestorer) memberRefusal(ctx context.Context, key, id string) (string, error) {
	if id == "" {
		return "a member without a configuration names no checkpoint of its instance", nil
	}
	cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "no group record of that key", nil
		}
		return "", fmt.Errorf("%w: the group record: %w", errRestoreUnverified, err)
	}
	for k, v := range cm.Data {
		if strings.HasPrefix(k, "checkpoint.") && v == id {
			return "", nil
		}
	}
	return "the group record does not list the checkpoint", nil
}

// rankRefusal returns why pod may not restore checkpoint id as a rank of
// its instance, "" when it may, or errRestoreUnverified: the checkpoint's
// capture record must be of the pod's tenant scope.
func (c *criuAutoRestorer) rankRefusal(ctx context.Context, pod *corev1.Pod, id string) (string, error) {
	scope, err := c.a.criuGroupScope(ctx, pod.Namespace)
	if err != nil {
		return "", fmt.Errorf("%w: the namespace's tenant scope: %w", errRestoreUnverified, err)
	}
	hash, err := c.checkpointHash(ctx, id)
	if err != nil {
		return "", fmt.Errorf("%w: the checkpoint's hash: %w", errRestoreUnverified, err)
	}
	if hash == "" {
		return "the checkpoint records no content hash, so no capture record vouches for it", nil
	}
	man, found, err := c.captureRecord(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("%w: the capture record: %w", errRestoreUnverified, err)
	}
	if !found {
		return "no capture record names the checkpoint", nil
	}
	if captured := man.SourcePodMeta[checkpointstore.CRIUScopeMetaKey]; captured == "" || captured != scope {
		return "the capture belongs to another tenant scope", nil
	}
	return "", nil
}

// restoreRefusal returns why the restorer may not act on pod's restore
// annotations, "" when it may, or errRestoreUnverified. A lone pod's
// checkpoint must be a capture of its scope; a rank's group record must be
// bound to its scope and its checkpoint a capture of that scope. A verdict
// is kept for the pod's life, for the annotations it was reached on.
func (c *criuAutoRestorer) restoreRefusal(ctx context.Context, pod *corev1.Pod) (string, error) {
	id, key := pod.Annotations[webhook.CRIURestoreAnnotation], pod.Annotations[webhook.CRIUGroupAnnotation]
	if id == "" && key == "" {
		return "", nil
	}
	ck := string(pod.UID) + "\x00" + id + "\x00" + key
	if v, ok := c.verdicts.Load(ck); ok {
		return v.(string), nil
	}
	var why string
	var err error
	if key != "" {
		if pod.Annotations[cacheURIAnnotation] == "" {
			// A member without a configuration of its own (a companion:
			// etcd, NATS, a frontend) is tied to the instance by the
			// record, which lists every checkpoint it captured.
			why, err = c.memberRefusal(ctx, key, id)
		} else {
			why, err = c.groupKeyRefusal(ctx, pod, key)
		}
		if err == nil && why == "" && id != "" {
			why, err = c.rankRefusal(ctx, pod, id)
		}
	} else {
		why, err = c.loneRestoreRefusal(ctx, pod, id)
	}
	if err != nil {
		return "", err
	}
	c.verdicts.Store(ck, why)
	return why, nil
}

// forgetPod drops what the restorer keeps about a pod that is gone.
func (c *criuAutoRestorer) forgetPod(pod *corev1.Pod) {
	c.dropRestoreRecord(pod.UID)
	prefix := string(pod.UID) + "\x00"
	c.verdicts.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), prefix) {
			c.verdicts.Delete(k)
		}
		return true
	})
}
