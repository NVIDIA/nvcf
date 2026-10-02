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

// The rendered Helm Chart of a MiniService is persisted in a Secret in the instance namespace,
// similar to how Helm stores a release record (sh.helm.release.v1.<name>.v<N>) in the release
// namespace. The Secret is the durable copy of the ReVal render output for the lifetime of the
// instance, so status checks and cleanup never need to call ReVal again after a successful render.
//
// The Secret always holds the latest successful render and is overwritten on Helm values updates.
// It is written before workload objects are applied, so while an update is failing to apply, its
// revision label can be ahead of the latest revision ConfigMap (see revision.go), which remains the
// history of applied values, chart URL, and render hash per revision.
//
//nolint:gosec // These are Secret object names, keys, and annotation keys, not credentials (G101).
const (
	// RenderedSecretName is the name of the Secret holding the rendered Helm Chart in the instance namespace.
	RenderedSecretName = "nvcf-miniservice-rendered"
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
)

// renderedEntry is the in-memory copy of a MiniService's rendered chart. Entries are immutable
// and replaced wholesale, so they can be shared without locking.
type renderedEntry struct {
	inputHash  string
	outputHash string
	data       []byte
	// synced is true once the entry has been reconciled with the rendered Secret
	// (stored, found already stored, or skipped because it is too large).
	synced bool
}

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

// saveRenderedData records freshly rendered data in the MiniService status and in memory.
// It does not persist the Secret because the instance namespace may not exist yet during install;
// callers must call persistRenderedData once the namespace exists.
func (r *Reconciler) saveRenderedData(ctx context.Context, ms *v1alpha1.MiniService, data []byte) {
	logf.FromContext(ctx).Info("Saving rendered Helm Chart data")

	outputHash := renderOutputHash(data)
	ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{
		Hash: outputHash,
	}
	r.renderedCache.Store(ms.Name, &renderedEntry{
		inputHash:  renderInputHash(ms),
		outputHash: outputHash,
		data:       data,
	})
}

// getRenderedData returns the rendered chart for ms from memory or from the rendered Secret.
// It returns false when no render matching the current spec is available, in which case
// callers render via ReVal.
//
// When status has no render details (first install, or a values update whose status patch was
// lost before it landed), the Secret is still consulted: a render stored for identical inputs is
// reused and its hash restored to status instead of calling ReVal again.
func (r *Reconciler) getRenderedData(ctx context.Context, ms *v1alpha1.MiniService) ([]byte, bool, error) {
	log := logf.FromContext(ctx)

	var expectedOutputHash string
	if rd := ms.Status.RenderDetails; rd != nil {
		expectedOutputHash = rd.Hash
	} else {
		log.V(1).Info("Rendered data not found in status")
	}

	inputHash := renderInputHash(ms)
	if entry, ok := r.loadRenderedEntry(ms); ok {
		if entry.inputHash == inputHash && (expectedOutputHash == "" || entry.outputHash == expectedOutputHash) {
			return entry.data, true, nil
		}
		log.V(1).Info("Discarding in-memory rendered data for different inputs or hash")
		r.renderedCache.Delete(ms.Name)
	}

	data, found, err := r.loadRenderedSecret(ctx, ms, inputHash, expectedOutputHash)
	if err != nil || !found {
		return nil, false, err
	}
	outputHash := renderOutputHash(data)
	log.V(1).Info("Loaded rendered Helm Chart data from Secret")
	if ms.Status.RenderDetails == nil {
		ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{Hash: outputHash}
	}
	r.renderedCache.Store(ms.Name, &renderedEntry{
		inputHash:  inputHash,
		outputHash: outputHash,
		data:       data,
		synced:     true,
	})
	return data, true, nil
}

func (r *Reconciler) loadRenderedEntry(ms *v1alpha1.MiniService) (*renderedEntry, bool) {
	v, ok := r.renderedCache.Load(ms.Name)
	if !ok {
		return nil, false
	}
	entry, ok := v.(*renderedEntry)
	return entry, ok
}

// forgetRenderedData drops the in-memory rendered data for ms. The rendered Secret is owned by
// the MiniService and lives in the instance namespace, so it is garbage collected with either.
func (r *Reconciler) forgetRenderedData(ms *v1alpha1.MiniService) {
	r.renderedCache.Delete(ms.Name)
}

// persistRenderedData ensures the rendered Secret in the instance namespace holds data.
// It is idempotent and cheap once the in-memory entry is marked synced. The instance namespace
// must exist. Like Helm, callers persist the record before applying workload objects.
func (r *Reconciler) persistRenderedData(ctx context.Context, ms *v1alpha1.MiniService, data []byte) error {
	inputHash := renderInputHash(ms)
	outputHash := renderOutputHash(data)

	if entry, ok := r.loadRenderedEntry(ms); ok &&
		entry.synced && entry.inputHash == inputHash && entry.outputHash == outputHash {
		return nil
	}

	if err := r.saveRenderedSecret(ctx, ms, data, inputHash, outputHash); err != nil {
		return err
	}

	r.renderedCache.Store(ms.Name, &renderedEntry{
		inputHash:  inputHash,
		outputHash: outputHash,
		data:       data,
		synced:     true,
	})
	return nil
}

func renderedSecretKey(ms *v1alpha1.MiniService) client.ObjectKey {
	return client.ObjectKey{Namespace: ms.Spec.Namespace, Name: RenderedSecretName}
}

// saveRenderedSecret creates or updates the rendered Secret for ms.
func (r *Reconciler) saveRenderedSecret(ctx context.Context,
	ms *v1alpha1.MiniService,
	data []byte,
	inputHash, outputHash string,
) error {
	log := logf.FromContext(ctx).WithValues("secret", RenderedSecretName, "namespace", ms.Spec.Namespace)

	existing := &corev1.Secret{}
	err := r.Client.Get(ctx, renderedSecretKey(ms), existing)
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
		// Nothing more can be done within the etcd object size limit; status checks will
		// fall back to re-rendering on demand for this instance.
		log.Info("Rendered Helm Chart is too large to persist in a Secret, skipping",
			"compressedBytes", len(compressed), "maxBytes", renderedSecretMaxCompressedBytes)
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RenderedSecretName,
			Namespace: ms.Spec.Namespace,
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
			// Created concurrently or not yet visible in the informer cache; fall through to update.
			if err = r.Client.Get(ctx, renderedSecretKey(ms), existing); err != nil {
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

// loadRenderedSecret returns the rendered data stored for ms if it matches the given hashes.
// Like Flux's artifact verification, the content digest is verified before it is trusted.
func (r *Reconciler) loadRenderedSecret(ctx context.Context,
	ms *v1alpha1.MiniService,
	inputHash, outputHash string,
) ([]byte, bool, error) {
	log := logf.FromContext(ctx).WithValues("secret", RenderedSecretName, "namespace", ms.Spec.Namespace)

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, renderedSecretKey(ms), secret); err != nil {
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
	if outputHash != "" && storedOutputHash != outputHash {
		log.V(1).Info("Rendered Secret hash does not match MiniService status, ignoring",
			"storedHash", storedOutputHash, "statusHash", outputHash)
		return nil, false, nil
	}

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
	return io.ReadAll(gzr)
}
