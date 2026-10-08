/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package mscontroller

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

// The rendered Helm Chart of a MiniService is persisted in a Secret, similar to how Helm stores a
// release record (sh.helm.release.v1.<name>.v<N>). The Secret is the durable copy of the ReVal
// render output for the lifetime of the instance, so status checks and cleanup never need to call
// ReVal again after a successful render.
//
// The Secret lives in the agent's system namespace, not the instance namespace: the instance
// workload ServiceAccount is granted write access to Secrets in its own namespace (see the
// mini-service-restrictions Role), and the controller acts on the stored render with its own
// privileges, so the render must only be writable by the agent. It is owned by the cluster-scoped
// MiniService for garbage collection and is also deleted explicitly on cleanup.
//
// The Secret always holds the latest successful render and is overwritten on Helm values updates.
// It is written before workload objects are applied, so while an update is failing to apply, its
// revision label can be ahead of the latest revision ConfigMap (see revision.go), which remains the
// history of applied values, chart URL, and render hash per revision.
//
// No rendered data is retained in agent memory: reads go through the informer cache (which holds the
// gzipped Secret regardless), with an uncached fallback for the window right after a write.
//
//nolint:gosec // These are Secret object names, keys, and annotation keys, not credentials (G101).
const (
	// RenderedSecretNamePrefix prefixes the per-MiniService Secret name in the agent system namespace.
	RenderedSecretNamePrefix = "nvcf-miniservice-rendered-"
	// renderedSecretType versions the Secret format. Bump the suffix on incompatible changes, as Helm does.
	renderedSecretType = corev1.SecretType("nvca.nvcf.nvidia.io/rendered-chart.v1")
	// renderedSecretDataKey holds the gzipped ReVal render output.
	renderedSecretDataKey = "rendered.json.gz"

	// renderedSecretOutputHashAnnotation is the sha256 of the uncompressed render output,
	// matching MiniService status.renderedDetails.hash.
	renderedSecretOutputHashAnnotation = "nvca.nvcf.nvidia.io/render-hash"
	// renderedSecretInputHashAnnotation is the sha256 of the render inputs that affect template output.
	// A stored render is only reused when the inputs of the current spec hash to the same value.
	renderedSecretInputHashAnnotation = "nvca.nvcf.nvidia.io/render-input-hash"
	renderedSecretChartURLAnnotation  = "nvca.nvcf.nvidia.io/chart-url"
	renderedSecretTimestampAnnotation = "nvca.nvcf.nvidia.io/rendered-at"

	// renderedSecretMaxCompressedBytes leaves headroom under the 1 MiB etcd object size limit.
	// Larger renders are not persisted and fall back to re-rendering on demand.
	renderedSecretMaxCompressedBytes = 900 << 10
	// renderedSecretMaxUncompressedBytes bounds decompression so a corrupted or crafted Secret
	// cannot expand without limit. Real renders are a few MB at most.
	renderedSecretMaxUncompressedBytes = 64 << 20
)

// renderInput mirrors the fields of HelmReValRenderInput that affect Helm template output.
// Namespace is included because templates using .Release.Namespace render namespace-specific values.
type renderInput struct {
	HelmChartURL         string          `json:"helmChartURL"`
	HelmChartServicePort *int32          `json:"helmChartServicePort,omitempty"`
	HelmChartServiceName string          `json:"helmChartServiceName,omitempty"`
	Values               json.RawMessage `json:"values,omitempty"`
	Namespace            string          `json:"namespace"`
}

// renderInputHash returns a hash identifying the render inputs of ms.
func renderInputHash(ms *v1alpha1.MiniService) string {
	in := renderInput{
		HelmChartURL:         ms.Spec.HelmChartConfig.URL,
		HelmChartServicePort: ms.Spec.HelmChartConfig.ServicePort,
		HelmChartServiceName: ms.Spec.HelmChartConfig.ServiceName,
		Values:               ms.Spec.HelmChartConfig.Values,
		Namespace:            ms.Spec.Namespace,
	}
	if len(in.Values) == 0 {
		in.Values = nil
	}
	// Marshal cannot fail for this struct unless Values is invalid JSON, in which case
	// ReVal rejects the render anyway; fall back to hashing the raw fields.
	b, err := json.Marshal(in)
	if err != nil {
		b = []byte(in.HelmChartURL + "|" + in.HelmChartServiceName + "|" + in.Namespace + "|" + string(in.Values))
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// renderOutputHash returns the hash of rendered data stored in MiniService status.renderedDetails.hash.
func renderOutputHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// saveRenderedData records the hash of freshly rendered data in the MiniService status.
// It does not persist the Secret because the instance namespace may not exist yet during install;
// callers must call persistRenderedData once the namespace exists.
func (r *Reconciler) saveRenderedData(ctx context.Context, ms *v1alpha1.MiniService, data []byte) {
	logf.FromContext(ctx).Info("Saving rendered Helm Chart data")
	ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{
		Hash: renderOutputHash(data),
	}
}

// getRenderedData returns the rendered chart for ms from the rendered Secret. It returns false when
// no render matching the current spec is available, in which case callers render via ReVal.
//
// A stored render is trusted when it was produced from identical inputs and its content matches
// its recorded digest. The hash in status is informational and is resynced from the Secret, so a
// lost or stale status patch (crash or conflict after the Secret was written) never forces a
// re-render.
func (r *Reconciler) getRenderedData(ctx context.Context, ms *v1alpha1.MiniService) ([]byte, bool, error) {
	log := logf.FromContext(ctx)

	data, found, err := r.loadRenderedSecret(ctx, ms, renderInputHash(ms))
	if err != nil || !found {
		return nil, false, err
	}

	outputHash := renderOutputHash(data)
	switch rd := ms.Status.RenderDetails; {
	case rd == nil:
		log.V(1).Info("Restoring render hash to status from rendered Secret")
		ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{Hash: outputHash}
	case rd.Hash != outputHash:
		log.V(1).Info("Resyncing render hash in status from rendered Secret", "statusHash", rd.Hash)
		rd.Hash = outputHash
	}
	return data, true, nil
}

// persistRenderedData ensures the rendered Secret in the instance namespace holds data. It is
// idempotent: when the stored Secret already matches, only an informer cache read is performed.
// The instance namespace must exist. Like Helm, callers persist the record before applying objects.
func (r *Reconciler) persistRenderedData(ctx context.Context, ms *v1alpha1.MiniService, data []byte) error {
	return r.saveRenderedSecret(ctx, ms, data, renderInputHash(ms), renderOutputHash(data))
}

// RenderedSecretName returns the name of the rendered Secret for a MiniService.
func RenderedSecretName(ms *v1alpha1.MiniService) string {
	return RenderedSecretNamePrefix + ms.Name
}

func (r *Reconciler) renderedSecretKey(ms *v1alpha1.MiniService) client.ObjectKey {
	return client.ObjectKey{Namespace: r.SystemNamespace, Name: RenderedSecretName(ms)}
}

// deleteRenderedSecret removes the rendered Secret for ms. NotFound is not an error.
func (r *Reconciler) deleteRenderedSecret(ctx context.Context, ms *v1alpha1.MiniService) error {
	if r.SystemNamespace == "" {
		return nil
	}
	secret := &corev1.Secret{}
	secret.Namespace, secret.Name = r.SystemNamespace, RenderedSecretName(ms)
	if err := r.Client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete rendered secret: %w", err)
	}
	return nil
}

// getRenderedSecret reads the rendered Secret through the informer cache and, when the cache does
// not have it yet (for example right after it was created), directly from the API server.
func (r *Reconciler) getRenderedSecret(ctx context.Context, ms *v1alpha1.MiniService) (*corev1.Secret, error) {
	if r.SystemNamespace == "" {
		return nil, fmt.Errorf("system namespace is not configured; cannot locate rendered secret")
	}
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, r.renderedSecretKey(ms), secret)
	if apierrors.IsNotFound(err) && r.APIReader != nil {
		err = r.APIReader.Get(ctx, r.renderedSecretKey(ms), secret)
	}
	if err != nil {
		return nil, err
	}
	return secret, nil
}

// saveRenderedSecret creates or updates the rendered Secret for ms.
func (r *Reconciler) saveRenderedSecret(ctx context.Context,
	ms *v1alpha1.MiniService,
	data []byte,
	inputHash, outputHash string,
) error {
	log := logf.FromContext(ctx).WithValues("secret", RenderedSecretName(ms), "namespace", r.SystemNamespace)

	existing, err := r.getRenderedSecret(ctx, ms)
	switch {
	case err == nil:
		if existing.Type == renderedSecretType &&
			existing.Annotations[renderedSecretInputHashAnnotation] == inputHash &&
			existing.Annotations[renderedSecretOutputHashAnnotation] == outputHash {
			log.V(1).Info("Rendered Secret is up to date")
			return nil
		}
		if existing.Type != renderedSecretType {
			// Secret types are immutable, so an older format can only be replaced.
			log.Info("Replacing rendered Secret with a different type", "type", existing.Type)
			if err := r.Client.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete rendered secret of type %q: %w", existing.Type, err)
			}
			existing = nil
		}
	case apierrors.IsNotFound(err):
		existing = nil
	default:
		return fmt.Errorf("get rendered secret: %w", err)
	}

	compressed, err := gzipBytes(data)
	if err != nil {
		return fmt.Errorf("compress rendered data: %w", err)
	}
	if len(compressed) > renderedSecretMaxCompressedBytes {
		// Nothing more can be done within the etcd object size limit; later reconciles will
		// fall back to re-rendering on demand for this instance.
		log.Info("Rendered Helm Chart is too large to persist in a Secret, skipping",
			"compressedBytes", len(compressed), "maxBytes", renderedSecretMaxCompressedBytes)
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RenderedSecretName(ms),
			Namespace: r.SystemNamespace,
			Labels: map[string]string{
				managedByLabel:       managedByValue,
				miniserviceNameLabel: ms.Name,
				revisionLabel:        strconv.FormatInt(ms.Status.Revision, 10),
			},
			Annotations: map[string]string{
				renderedSecretInputHashAnnotation:  inputHash,
				renderedSecretOutputHashAnnotation: outputHash,
				renderedSecretChartURLAnnotation:   ms.Spec.HelmChartConfig.URL,
				renderedSecretTimestampAnnotation:  r.now().UTC().Format(time.RFC3339Nano),
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
				Kind:       miniServiceKind,
				Name:       ms.Name,
				UID:        ms.UID,
			}},
		},
		Type: renderedSecretType,
		Data: map[string][]byte{renderedSecretDataKey: compressed},
	}

	if existing == nil {
		log.Info("Creating rendered Secret", "revision", ms.Status.Revision)
		err = r.Client.Create(ctx, secret)
		if apierrors.IsAlreadyExists(err) {
			// Created concurrently; fetch the live object to update it.
			if existing, err = r.getRenderedSecret(ctx, ms); err != nil {
				return fmt.Errorf("get rendered secret after create conflict: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("create rendered secret: %w", err)
		} else {
			return nil
		}
	}

	log.Info("Updating rendered Secret", "revision", ms.Status.Revision)
	secret.ResourceVersion = existing.ResourceVersion
	if err := r.Client.Update(ctx, secret); err != nil {
		return fmt.Errorf("update rendered secret: %w", err)
	}
	return nil
}

// loadRenderedSecret returns the rendered data stored for ms if it was rendered from inputs
// matching inputHash. Like Flux's artifact verification, the content digest is verified before
// it is trusted.
func (r *Reconciler) loadRenderedSecret(ctx context.Context,
	ms *v1alpha1.MiniService,
	inputHash string,
) ([]byte, bool, error) {
	log := logf.FromContext(ctx).WithValues("secret", RenderedSecretName(ms), "namespace", r.SystemNamespace)

	secret, err := r.getRenderedSecret(ctx, ms)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Rendered Secret not found")
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get rendered secret: %w", err)
	}

	if got := secret.Annotations[renderedSecretInputHashAnnotation]; got != inputHash {
		log.V(1).Info("Rendered Secret was rendered from different inputs, ignoring", "storedInputHash", got)
		return nil, false, nil
	}
	storedOutputHash := secret.Annotations[renderedSecretOutputHashAnnotation]
	data, err := gunzipBytes(secret.Data[renderedSecretDataKey])
	if err != nil {
		log.Error(err, "Failed to decompress rendered Secret, ignoring")
		return nil, false, nil
	}
	if renderOutputHash(data) != storedOutputHash {
		log.Error(nil, "Rendered Secret content does not match its hash, ignoring")
		return nil, false, nil
	}
	return data, true, nil
}

func gzipBytes(data []byte) ([]byte, error) {
	buf := &bytes.Buffer{}
	// Best compression, as Helm uses for release records, to stay within the object size limit.
	w, err := gzip.NewWriterLevel(buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no data")
	}
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gzr.Close()
	// Read one byte past the limit to detect oversize content without buffering it all.
	out, err := io.ReadAll(io.LimitReader(gzr, renderedSecretMaxUncompressedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > renderedSecretMaxUncompressedBytes {
		return nil, fmt.Errorf("decompressed data exceeds %d bytes", renderedSecretMaxUncompressedBytes)
	}
	return out, nil
}
