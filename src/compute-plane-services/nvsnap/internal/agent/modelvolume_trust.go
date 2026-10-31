// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// What the controller acts on comes from pod and Job metadata. The webhook
// stamps it, but a pod admitted without the webhook (nvsnap.io/inject=false,
// no GPU request) carries whatever its author wrote, and labels and
// annotations stay mutable. So nothing stamped is trusted on its own: the
// identity must belong to the pod's tenant, only the namespace holding the
// download-owner Lease writes or finishes a download, and host paths are
// resolved from the pod spec, never joined from annotations.

// scopeTTL is how long a namespace's tenant is remembered. Pod events
// arrive per pod update and on every resync; a tenant label rarely changes.
const scopeTTL = time.Minute

type scopeEntry struct {
	scope string
	until time.Time
}

// scopeOf is Provisioner.Scope, remembered for scopeTTL.
func (c *ModelVolumeController) scopeOf(ctx context.Context, ns string) (string, error) {
	now := time.Now()
	c.mu.Lock()
	e, ok := c.scopes[ns]
	c.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.scope, nil
	}
	scope, err := c.Provisioner.Scope(ctx, ns)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.scopes == nil {
		c.scopes = map[string]scopeEntry{}
	}
	c.scopes[ns] = scopeEntry{scope: scope, until: now.Add(scopeTTL)}
	c.mu.Unlock()
	return scope, nil
}

// inScope reports whether model identity uri belongs to the tenant of
// namespace ns (modelvolume.ScopedURI). A pod or Job naming another
// tenant's model is ignored, logged once per object.
func (c *ModelVolumeController) inScope(ctx context.Context, ns, uri, object string) bool {
	scope, err := c.scopeOf(ctx, ns)
	if err != nil {
		c.log().WithError(err).WithFields(logrus.Fields{"object": object, "model": uri}).Warn("model volume: tenant of namespace unknown; ignoring")
		return false
	}
	if strings.HasPrefix(uri, scope+"/") {
		return true
	}
	c.mu.Lock()
	seen := c.attempts["scope:"+object] > 0
	c.attempts["scope:"+object] = 1
	c.mu.Unlock()
	if !seen {
		c.log().WithFields(logrus.Fields{"object": object, "model": uri, "scope": scope}).Warn("model volume: identity is not the namespace's tenant; ignoring")
	}
	return false
}

// ownsDownload reports whether ns holds the download-owner Lease of uri:
// the one namespace whose Job or staging pod may write or finish it.
// recorded allows the agents that see an outcome after the first one
// released the Lease (completion and failure both do) to repeat that
// outcome idempotently; it is consulted only when no Lease is left.
func (c *ModelVolumeController) ownsDownload(ctx context.Context, uri, ns string, recorded func(modelvolume.State) bool, log logrus.FieldLogger) bool {
	owner, err := c.Provisioner.DownloadOwner(ctx, uri)
	if err != nil {
		log.WithError(err).Warn("model volume: read download owner failed")
		return false
	}
	if owner == ns {
		return true
	}
	if owner == "" && recorded != nil {
		st, err := c.Provisioner.Lookup(ctx, uri)
		if err != nil {
			log.WithError(err).Warn("model volume: lookup failed")
			return false
		}
		if recorded(st) {
			return true
		}
	}
	log.WithField("owner", owner).Warn("model volume: namespace does not own this download; ignoring")
	return false
}

func complete(st modelvolume.State) bool { return st.Complete }
func failed(st modelvolume.State) bool   { return st.Failed }

// emptyDirTree is the kubelet path (under KubeletPodsDir, not HostFSRoot)
// of subtree sub of the pod's emptyDir volume vol. vol must be an emptyDir
// of the pod spec and sub a local path ("" for the volume root); the
// result is checked to stay inside the volume.
func (c *ModelVolumeController) emptyDirTree(pod *corev1.Pod, vol, sub string) (string, error) {
	found := false
	for i := range pod.Spec.Volumes {
		if v := &pod.Spec.Volumes[i]; v.Name == vol {
			if v.EmptyDir == nil {
				return "", fmt.Errorf("volume %q is not an emptyDir", vol)
			}
			found = true
			break
		}
	}
	if !found || vol == "" || !filepath.IsLocal(vol) || filepath.Base(vol) != vol {
		return "", fmt.Errorf("volume %q is not an emptyDir of the pod", vol)
	}
	if sub != "" && !filepath.IsLocal(sub) {
		return "", fmt.Errorf("subpath %q leaves the volume", sub)
	}
	root := filepath.Join(c.KubeletPodsDir, string(pod.UID), "volumes", "kubernetes.io~empty-dir", vol)
	p := filepath.Join(root, sub)
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("subpath %q leaves the volume", sub)
	}
	return p, nil
}

// strandedRecheck bounds how often one agent looks at the producer of an
// identity its pending readers wait on.
const strandedRecheck = time.Minute

// releaseIfStranded deletes a pending reader of an incomplete identity
// that nothing produces any more (no owner, or the owner's download Job
// gone: failed and removed, or deleted by hand), so its controller
// recreates it and admission starts a download afresh. A failure handled
// on the Job or staging pod event releases readers itself; this is the
// backstop for the cases with no such event. Readers younger than the
// owner grace are left alone: admission creates the Lease before the Job.
func (c *ModelVolumeController) releaseIfStranded(ctx context.Context, pod *corev1.Pod, uri string, st modelvolume.State, log logrus.FieldLogger) {
	if st.Complete || time.Since(pod.CreationTimestamp.Time) < modelvolume.DownloadOwnerGrace {
		return
	}
	key := "stranded:" + uri
	now := time.Now()
	c.mu.Lock()
	if last, ok := c.checked[key]; ok && now.Sub(last) < strandedRecheck {
		c.mu.Unlock()
		return
	}
	if c.checked == nil {
		c.checked = map[string]time.Time{}
	}
	c.checked[key] = now
	c.mu.Unlock()
	stranded := st.Failed
	if !stranded {
		var err error
		if stranded, err = c.Provisioner.DownloadStranded(ctx, uri); err != nil {
			log.WithError(err).Warn("model volume: download owner check failed")
			return
		}
	}
	if !stranded {
		return
	}
	opts := metav1.DeleteOptions{}
	if pod.UID != "" {
		uid := pod.UID // not a pod recreated under the same name since
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if err := c.Kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, opts); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		log.WithError(err).Warn("model volume: release stranded reader failed")
		return
	}
	log.WithField("failed", st.Failed).Warn("model volume: no download produces this model any more; released the waiting reader to a fresh admission")
}
