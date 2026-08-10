/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package checkpointstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ConfigMapBackend wraps an inner Backend (typically Local) with K8s
// ConfigMap-backed manifest storage. The data tree (cache contents)
// stays where the inner Backend writes it (per-node hostPath for
// Local, per-capture PVC for GPDRox); the manifest goes into a
// ConfigMap so any pod with K8s API access can resolve a hash to
// metadata regardless of which node holds the data.
//
// This is the architectural fix for the cross-node visibility problem:
// the agent on node A captures + writes the manifest CM. Any agent
// (acting as the admission webhook) on any node reads the CM at
// admission time. The Mutator uses Manifest.CapturedOnNodes to inject
// nodeAffinity so the FRESH pod lands on a node whose local cache
// actually has the data.
//
// Naming: a capture under hash h is stored as a ConfigMap named
// "nvsnap-capture-<short-hash>" in Namespace, with the full JSON-encoded
// Manifest in data["manifest.json"]. ConfigMaps are cluster-readable
// by anyone with RBAC for the namespace; for production, restrict
// the read role to nvsnap's ServiceAccount.
type ConfigMapBackend struct {
	inner     Backend
	client    kubernetes.Interface
	namespace string
	log       logrus.FieldLogger
}

const (
	// CMLabelKind marks ConfigMaps managed by nvsnap so list/cleanup
	// queries can filter them.
	CMLabelKind = "nvsnap.io/kind"

	// CMLabelKindCapture is the value for capture manifest ConfigMaps.
	CMLabelKindCapture = "rootfs-capture-manifest"

	// CMLabelShortHash is the 32-char short hash (128 bits), suitable
	// for label selectors. K8s label values are ≤ 63 chars; full sha256
	// is 64 — too long. The 32-char prefix gives crypto-strength
	// uniqueness while staying inside the limit.
	CMLabelShortHash = "nvsnap.io/short-hash"

	// CMAnnotationHash is the full sha256 hex; annotations have a
	// per-object 256KiB total cap so 64 chars is fine.
	CMAnnotationHash = "nvsnap.io/capture-hash"

	// CMDataKey is the data field that holds the JSON manifest.
	CMDataKey = "manifest.json"

	// LabelSourceNamespace identifies the originating pod's namespace.
	// Lets `kubectl get pvc -l nvsnap.io/source-namespace=nvsnap-system`
	// find every capture from one namespace.
	LabelSourceNamespace = "nvsnap.io/source-namespace"

	// LabelSourceEngine is the engine type ("vllm", "sglang", "trtllm",
	// "nim"). Pulled from Manifest.SourcePodMeta["engine"]; empty if
	// the orchestrator didn't classify it.
	LabelSourceEngine = "nvsnap.io/source-engine"

	// LabelSourceImageBase is the base image name (no registry, no tag,
	// DNS-label-sanitized). e.g. "vllm/vllm-openai:v0.11.2" → "vllm-openai".
	// Lets `kubectl get pvc -l nvsnap.io/source-image-base=vllm-openai`
	// find every capture of that image family.
	LabelSourceImageBase = "nvsnap.io/source-image-base"
)

// SourceIdentityLabels derives the human-friendly identity labels from
// a Manifest's SourcePodMeta. Returns an empty map if no usable fields
// are present. Values are sanitized to DNS-1123 label rules and capped
// at 63 chars. Safe to merge into an existing label map.
func SourceIdentityLabels(m Manifest) map[string]string {
	out := map[string]string{}
	if m.SourcePodMeta == nil {
		return out
	}
	if ns := sanitizeLabelValue(m.SourcePodMeta["namespace"]); ns != "" {
		out[LabelSourceNamespace] = ns
	}
	if engine := sanitizeLabelValue(m.SourcePodMeta["engine"]); engine != "" {
		out[LabelSourceEngine] = engine
	}
	if base := sanitizeLabelValue(imageBase(m.SourcePodMeta["image"])); base != "" {
		out[LabelSourceImageBase] = base
	}
	return out
}

// imageBase returns the image's repo basename (no registry, no tag, no digest).
//
//	"vllm/vllm-openai:v0.11.2"        → "vllm-openai"
//	"stg.nvcr.io/zq9/nvsnap-agent:v1"   → "nvsnap-agent"
//	"nvcr.io/nim/llama3-8b-instruct"  → "llama3-8b-instruct"
func imageBase(image string) string {
	if image == "" {
		return ""
	}
	// Strip digest (@sha256:...)
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	// Strip tag (last ':')
	if i := strings.LastIndex(image, ":"); i >= 0 {
		image = image[:i]
	}
	// Take the basename after last '/'.
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	return image
}

// sanitizeLabelValue conforms a string to DNS-1123 label rules (lowercase
// alphanumeric, '-', start/end alphanumeric, ≤63 chars). Empty input or
// fully-stripped input returns "".
func sanitizeLabelValue(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		default:
			out = append(out, '-')
		}
	}
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) > 63 {
		out = out[:63]
		for len(out) > 0 && out[len(out)-1] == '-' {
			out = out[:len(out)-1]
		}
	}
	return string(out)
}

// NewConfigMapBackend wraps inner with ConfigMap-backed manifests in
// namespace. The inner Backend keeps responsibility for data + Mount;
// only Put + Stat (the manifest-bearing operations) go through the CM.
func NewConfigMapBackend(inner Backend, client kubernetes.Interface, namespace string, log logrus.FieldLogger) *ConfigMapBackend {
	if log == nil {
		log = logrus.NewEntry(logrus.New()).WithField("subsys", "checkpointstore.cmregistry")
	}
	return &ConfigMapBackend{
		inner:     inner,
		client:    client,
		namespace: namespace,
		log:       log,
	}
}

// Compile-time assertion that *ConfigMapBackend satisfies Backend.
var _ Backend = (*ConfigMapBackend)(nil)

// CMNameFor returns the canonical ConfigMap name for a capture hash.
func CMNameFor(hash string) string {
	return "nvsnap-capture-" + ShortHash(hash)
}

// Put delegates to the inner Backend, then publishes the manifest to a ConfigMap.
func (c *ConfigMapBackend) Put(ctx context.Context, hash string, sources []CaptureSource, m Manifest) (Manifest, error) {
	stored, err := c.inner.Put(ctx, hash, sources, m)
	if err != nil {
		return Manifest{}, err
	}
	// After data is committed, write the manifest to a ConfigMap. If
	// this fails, the data is committed but unreachable from other
	// nodes — surface the error so the caller can retry / clean up.
	if err := c.writeManifestCM(ctx, hash, stored); err != nil {
		return Manifest{}, fmt.Errorf("write manifest configmap: %w", err)
	}
	return stored, nil
}

// Get delegates to the inner Backend.
func (c *ConfigMapBackend) Get(ctx context.Context, hash, dstDir string) (Manifest, error) {
	return c.inner.Get(ctx, hash, dstDir)
}

// Stat prefers the ConfigMap and self-heals by back-filling it from the inner Backend.
func (c *ConfigMapBackend) Stat(ctx context.Context, hash string) (Manifest, error) {
	// Prefer the ConfigMap (globally readable). If the CM is missing
	// but the inner Backend (Local on this node) has the data, the
	// node is in an "orphan" state — typically a capture committed by
	// an older agent version that didn't write CMs, or a CM that got
	// manually deleted. Self-heal by back-filling the CM from the
	// inner manifest. Idempotent: subsequent Stats find the CM directly.
	m, err := c.readManifestCM(ctx, hash)
	if err == nil {
		return m, nil
	}
	cmErr := err
	if !errors.Is(cmErr, ErrNotFound) {
		c.log.WithError(cmErr).WithField("hash", ShortHash(hash)).
			Warn("CM read failed; falling back to inner Stat")
	}
	innerM, innerErr := c.inner.Stat(ctx, hash)
	if innerErr != nil {
		return innerM, innerErr
	}
	// CM was missing but inner found data — back-fill the CM so other
	// agents (admission webhooks) can resolve this hash.
	if errors.Is(cmErr, ErrNotFound) {
		// Ensure CapturedOnNodes is populated from local context if the
		// inner manifest didn't already record it. inner.Stat returns
		// whatever's on disk; if that's missing CapturedOnNodes (older
		// schema), we can't synthesize the node here without leaking
		// hostname into the registry — leave the field as inner returned.
		if writeErr := c.writeManifestCM(ctx, hash, innerM); writeErr != nil {
			c.log.WithError(writeErr).WithField("hash", ShortHash(hash)).
				Warn("CM back-fill failed; webhook on other nodes won't see this capture")
		} else {
			c.log.WithField("hash", ShortHash(hash)).
				Info("CM back-filled from inner Stat (orphan recovery)")
		}
	}
	return innerM, nil
}

// Delete removes the manifest ConfigMap (best-effort) and then the inner data.
func (c *ConfigMapBackend) Delete(ctx context.Context, hash string) error {
	// Best-effort delete the CM first; if it succeeds and the inner
	// Delete fails we'll have orphan data, but the metadata is gone
	// so the webhook treats the hash as not-found (correct outcome).
	if err := c.deleteManifestCM(ctx, hash); err != nil && !errors.Is(err, ErrNotFound) {
		c.log.WithError(err).WithField("hash", ShortHash(hash)).
			Warn("CM delete failed; continuing with data delete")
	}
	return c.inner.Delete(ctx, hash)
}

// Mount delegates to the inner Backend.
func (c *ConfigMapBackend) Mount(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	return c.inner.Mount(ctx, hash, vol)
}

func (c *ConfigMapBackend) writeManifestCM(ctx context.Context, hash string, m Manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	labels := map[string]string{
		CMLabelKind:      CMLabelKindCapture,
		CMLabelShortHash: ShortHash(hash),
	}
	for k, v := range SourceIdentityLabels(m) {
		labels[k] = v
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CMNameFor(hash),
			Namespace: c.namespace,
			Labels:    labels,
			Annotations: map[string]string{
				CMAnnotationHash: hash,
			},
		},
		Data: map[string]string{CMDataKey: string(data)},
	}
	_, err = c.client.CoreV1().ConfigMaps(c.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Update in place — captures are content-addressed, so an
		// existing CM with the same hash should hold equivalent data.
		// Update covers the case where CapturedOnNodes grows (a second
		// node also cached the same hash).
		existing, getErr := c.client.CoreV1().ConfigMaps(c.namespace).Get(ctx, CMNameFor(hash), metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get existing cm: %w", getErr)
		}
		// Merge CapturedOnNodes (union, dedup, preserve order).
		merged := mergeNodeLists(existingManifestNodes(existing), m.CapturedOnNodes)
		m.CapturedOnNodes = merged
		newData, _ := json.Marshal(m)
		existing.Data = map[string]string{CMDataKey: string(newData)}
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels[CMLabelKind] = CMLabelKindCapture
		existing.Labels[CMLabelShortHash] = ShortHash(hash)
		for k, v := range SourceIdentityLabels(m) {
			existing.Labels[k] = v
		}
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		existing.Annotations[CMAnnotationHash] = hash
		_, err = c.client.CoreV1().ConfigMaps(c.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	}
	return err
}

func (c *ConfigMapBackend) readManifestCM(ctx context.Context, hash string) (Manifest, error) {
	cm, err := c.client.CoreV1().ConfigMaps(c.namespace).Get(ctx, CMNameFor(hash), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	raw, ok := cm.Data[CMDataKey]
	if !ok || raw == "" {
		return Manifest{}, ErrNotFound
	}
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest from CM: %w", err)
	}
	return m, nil
}

func (c *ConfigMapBackend) deleteManifestCM(ctx context.Context, hash string) error {
	err := c.client.CoreV1().ConfigMaps(c.namespace).Delete(ctx, CMNameFor(hash), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// mergeNodeLists returns the union of a and b in order: a's entries
// first, then b's entries that aren't already present. Stable across
// callers so manifest diffs are minimal.
func mergeNodeLists(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, n := range a {
		if _, ok := seen[n]; ok || n == "" {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for _, n := range b {
		if _, ok := seen[n]; ok || n == "" {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// existingManifestNodes parses an existing CM and returns its
// CapturedOnNodes; empty on parse failure (we'll just take the new value).
func existingManifestNodes(cm *corev1.ConfigMap) []string {
	if cm == nil || cm.Data == nil {
		return nil
	}
	raw, ok := cm.Data[CMDataKey]
	if !ok {
		return nil
	}
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m.CapturedOnNodes
}
