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

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcfdra "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/dra"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// miniserviceMutatingWebhook mutates objects admitted into operator-mode MiniService namespaces.
//
// The webhook reads the nvcf-miniservice-metadata ConfigMap from the target namespace to
// resolve NVCF metadata, then injects labels, annotations, and container env vars.
// Mutations are always additive: operator-set fields are never removed or overwritten.
//
// All object types receive metadata injection (labels and annotations). Pod-spec mutations
// (env var injection into containers) are applied only to types registered in
// podTemplateExtractors. Pods are handled directly since they do not use PodTemplateSpec.
// Unknown CRD types receive only metadata injection; no registration is required.
type miniserviceMutatingWebhook struct {
	// k8sClient reads the nvcf-miniservice-metadata ConfigMap from each MiniService namespace.
	// Using a direct API call is correct and simple; an informer-backed cache would reduce
	// API server load at high admission rates and can be added as a follow-up optimization.
	getConfigMap func(namespace string) (*corev1.ConfigMap, error)

	fff featureflag.Fetcher

	scheme  *runtime.Scheme
	decoder runtime.Decoder

	// Shim for shared storage mutating webhook that needed object annoations applied by this webhook.
	// Since NVCA cannot guarantee webhook execution order, its mutate() method is called here.
	sharedStorageMutator *helmStorageMutatingWebhook

	// Cache of MiniserviceMetadata for each namespace.
	// Cleaned up periodically by a background goroutine.
	metaCacheMu sync.RWMutex
	metaCache   map[string]nvcatypes.MiniserviceMetadata
	// Use a separate map/mutex for last accessed time to avoid bottleneck on metaCache access.
	lastAccessedMu   sync.Mutex
	metaLastAccessed map[string]time.Time

	// For testing.
	now func() time.Time
}

// NewMiniserviceMutatingWebhook returns an HTTP handler for mutating miniservice objects.
func NewMiniserviceMutatingWebhook(ctx context.Context, name string, k8sClient kubernetes.Interface) (http.Handler, error) {
	wh, err := newMiniserviceMutatingWebhook(ctx, k8sClient)
	if err != nil {
		return nil, err
	}
	return newStandaloneWebhook(ctx, name, wh)
}

func newMiniserviceMutatingWebhook(ctx context.Context, k8sClient kubernetes.Interface) (admission.Handler, error) {
	scheme := runtime.NewScheme()
	registerScheme(scheme, corev1.SchemeGroupVersion,
		&corev1.Pod{},
	)

	decoder := serializer.NewCodecFactory(scheme).UniversalDeserializer()

	wh := &miniserviceMutatingWebhook{
		fff:                  featureflag.DefaultFetcher,
		scheme:               scheme,
		decoder:              decoder,
		sharedStorageMutator: &helmStorageMutatingWebhook{},
		metaCache:            make(map[string]nvcatypes.MiniserviceMetadata),
		metaLastAccessed:     make(map[string]time.Time),
		now:                  time.Now,
	}

	go wh.metaCacheCleanup(ctx)

	if err := wh.initConfigMapInformer(ctx, k8sClient); err != nil {
		return nil, err
	}

	return wh, nil
}

func (w *miniserviceMutatingWebhook) initConfigMapInformer(ctx context.Context, k8sClient kubernetes.Interface) error {
	cmIF := informers.NewSharedInformerFactoryWithOptions(
		k8sClient,
		resync,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fields.OneTermEqualSelector(metav1.ObjectNameField, nvcatypes.MiniserviceMetadataConfigMapName).String()
		}),
	)
	cmInf := cmIF.Core().V1().ConfigMaps().Informer()
	cmLister := cmIF.Core().V1().ConfigMaps().Lister()

	w.getConfigMap = func(namespace string) (*corev1.ConfigMap, error) {
		return cmLister.ConfigMaps(namespace).Get(nvcatypes.MiniserviceMetadataConfigMapName)
	}

	cmIF.Start(ctx.Done())
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(cctx.Done(), cmInf.HasSynced) {
		return fmt.Errorf("timeout while waiting for configmap informer sync")
	}

	return nil
}

// miniserviceMetadataFromConfigMap parses a MiniserviceMetadata from ConfigMap data.
func (w *miniserviceMutatingWebhook) miniserviceMetadataFromConfigMap(
	namespace string,
	data map[string]string,
) (nvcatypes.MiniserviceMetadata, error) {
	meta, err := nvcatypes.FromConfigMapData(data)
	if err != nil {
		return nvcatypes.MiniserviceMetadata{}, fmt.Errorf("parse miniservice metadata: %w", err)
	}

	w.metaCacheMu.Lock()
	w.metaCache[namespace] = meta
	w.metaCacheMu.Unlock()
	go func() {
		w.lastAccessedMu.Lock()
		w.metaLastAccessed[namespace] = w.now()
		w.lastAccessedMu.Unlock()
	}()

	return meta, nil
}

func (w *miniserviceMutatingWebhook) getMiniserviceMetadata(namespace string) (nvcatypes.MiniserviceMetadata, bool) {
	w.metaCacheMu.RLock()
	meta, ok := w.metaCache[namespace]
	if ok {
		go func() {
			w.lastAccessedMu.Lock()
			w.metaLastAccessed[namespace] = w.now()
			w.lastAccessedMu.Unlock()
		}()
	}
	w.metaCacheMu.RUnlock()
	return meta, ok
}

func (w *miniserviceMutatingWebhook) metaCacheCleanup(ctx context.Context) {
	cacheCleanupInterval := 5 * time.Minute
	ticker := time.NewTicker(cacheCleanupInterval)
	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
			w.metaCacheMu.Lock()
			w.lastAccessedMu.Lock()
			for namespace, lastAccessed := range w.metaLastAccessed {
				if lastAccessed.Before(w.now().Add(-cacheCleanupInterval)) {
					delete(w.metaLastAccessed, namespace)
					delete(w.metaCache, namespace)
				}
			}
			w.lastAccessedMu.Unlock()
			w.metaCacheMu.Unlock()
		}
	}
}

// Handle implements admission.Handler.
func (w *miniserviceMutatingWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := core.GetLogger(ctx)

	namespace := req.Namespace
	if namespace == "" {
		// Cluster-scoped objects have no namespace. The namespaceSelector on the
		// MutatingWebhookConfiguration should prevent this case in practice.
		return admission.Allowed("cluster-scoped object: skipping miniservice mutating webhook")
	}

	gvk := schema.GroupVersionKind(req.Kind)

	// Decode the admitted object. Typed objects are decoded for known GVK registrations;
	// everything else (operator CRDs) falls back to unstructured.Unstructured.
	obj, err := w.decodeObject(req.Object.Raw, gvk)
	if err != nil {
		return admission.Errored(http.StatusBadRequest,
			fmt.Errorf("decode admitted %s: %w", gvk, err))
	}

	isCreate := req.Operation == admissionv1.Create

	if pod, ok := obj.(*corev1.Pod); ok && pod.Name == translatecommon.UtilsPodName {
		if isCreate {
			// Shared storage mutation does not need metadata.
			if _, _, err := w.sharedStorageMutator.mutate(ctx, obj); err != nil {
				return admission.Errored(http.StatusInternalServerError,
					fmt.Errorf("mutate shared storage on utils pod: %w", err))
			}
		}
	} else {
		// The metadata configmap is only written once, otherwise the webhook is not guaranteed
		// to have the latest metadata on invocation. Therefore caching in memory is safe.
		meta, cacheHit := w.getMiniserviceMetadata(namespace)
		if !cacheHit {
			// Read the metadata ConfigMap written by the MiniService controller before object
			// creation. A missing ConfigMap triggers a Denied response (Fail policy) so that no
			// uninjected object is admitted into a MiniService namespace.
			cm, err := w.getConfigMap(namespace)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return admission.Denied(fmt.Sprintf(
						"namespace %q missing required ConfigMap %q: cannot inject NVCF metadata",
						namespace, nvcatypes.MiniserviceMetadataConfigMapName,
					))
				}
				log.WithError(err).Error("Failed to read miniservice metadata ConfigMap")
				return admission.Errored(http.StatusInternalServerError, err)
			}

			if meta, err = w.miniserviceMetadataFromConfigMap(namespace, cm.Data); err != nil {
				return admission.Errored(http.StatusInternalServerError,
					fmt.Errorf("parse miniservice metadata: %w", err))
			}
		}

		if err := w.mutate(ctx, obj, meta, isCreate); err != nil {
			return admission.Errored(http.StatusInternalServerError,
				fmt.Errorf("mutate admitted %s: %w", gvk, err))
		}
	}

	mutated, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("marshal mutated %s: %w", gvk, err))
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, mutated)
}

// decodeObject decodes raw admission bytes into a typed client.Object for known GVKs,
// or into an *unstructured.Unstructured for all other types.
func (w *miniserviceMutatingWebhook) decodeObject(raw []byte, gvk schema.GroupVersionKind) (client.Object, error) {
	if w.scheme.Recognizes(gvk) {
		robj, _, err := w.decoder.Decode(raw, &gvk, nil)
		if err != nil {
			return nil, err
		}
		cobj, ok := robj.(client.Object)
		if !ok {
			return nil, fmt.Errorf("%s does not implement client.Object", gvk)
		}
		return cobj, nil
	}
	// Unknown type (operator CRD or any future type): use unstructured so the webhook
	// can inject metadata without knowing the type's Go struct.
	u := &unstructured.Unstructured{}
	if _, _, err := unstructured.UnstructuredJSONScheme.Decode(raw, &gvk, u); err != nil {
		return nil, fmt.Errorf("decode unknown type %s: %w", gvk, err)
	}
	return u, nil
}

// mutate applies all NVCF mutations to obj in-place.
func (w *miniserviceMutatingWebhook) mutate(ctx context.Context, obj client.Object, meta nvcatypes.MiniserviceMetadata, isCreate bool) error {
	// Always inject labels and annotations on the object itself.
	labels, annotations := obj.GetLabels(), obj.GetAnnotations()
	if labels == nil {
		labels = make(map[string]string)
		obj.SetLabels(labels)
	}
	if annotations == nil {
		annotations = make(map[string]string)
		obj.SetAnnotations(annotations)
	}
	maps.Copy(labels, meta.Labels)
	maps.Copy(annotations, meta.Annotations)

	if t, ok := obj.(*corev1.Pod); ok {
		// Metadata updates.
		maps.Copy(labels, meta.PodLabels)
		maps.Copy(annotations, meta.PodAnnotations)

		// GXCache label for injection.
		w.mutateGXCache(labels)

		// Pod spec mutations must only be applied on creation events.
		if isCreate {
			w.mutatePodSpec(&t.Spec, meta)

			// NVLink DRA mutations for claims/scheduling.
			if w.fff.IsAttributeEnabled(featureflag.AttrNVLinkOptimized) {
				w.mutateNVLinkDRA(obj.GetNamespace(), t)
			}

			if _, _, err := w.sharedStorageMutator.mutate(ctx, obj); err != nil {
				return fmt.Errorf("mutate shared storage: %w", err)
			}
		}
	}

	// Unstructured objects indirectly set labels/annotations, so call methods again.
	obj.SetLabels(labels)
	obj.SetAnnotations(annotations)

	return nil
}

// mutatePodSpec applies all pod-spec-level mutations: env vars, service account,
// tolerations, image pull secrets, scheduler, node affinity, and termination grace period.
func (w *miniserviceMutatingWebhook) mutatePodSpec(ps *corev1.PodSpec, meta nvcatypes.MiniserviceMetadata) {
	// Add override-able envs to containers if necessary
	var overrideableEnvVars map[string]bool
	if w.fff.IsFeatureFlagEnabled(featureflag.BYOObservability) {
		overrideableEnvVars = translatecommon.OverrideableEnvVars
	}
	if overrideableEnvVars != nil {
		// Filter out any empty existing values first so overrides can be set
		for i := range ps.Containers {
			ps.Containers[i].Env = filterEmptyOverrideableEnvVars(ps.Containers[i].Env, overrideableEnvVars)
		}
		for i := range ps.InitContainers {
			ps.InitContainers[i].Env = filterEmptyOverrideableEnvVars(ps.InitContainers[i].Env, overrideableEnvVars)
		}
	}
	_ = translatecommon.AddMixedEnvsToContainers(overrideableEnvVars, ps.InitContainers, meta.EnvVars...)
	_ = translatecommon.AddMixedEnvsToContainers(overrideableEnvVars, ps.Containers, meta.EnvVars...)
	k8sutil.AddBYOOLogChunkingEnvVarsToPodSpec(ps, meta.OTelCollectorEnvVars)

	// If the pod is allowed k8s api access (ex. when an operator created it), and it has set a non-default service account,
	// use it instead of the service account override.
	if meta.ServiceAccountName != "" && (!w.fff.IsFeatureFlagEnabled(featureflag.AllowWorkloadKubernetesAPIAccess) ||
		ps.ServiceAccountName == "" || ps.ServiceAccountName == "default") {
		ps.ServiceAccountName = meta.ServiceAccountName
	}

	if len(meta.Tolerations) > 0 {
		k8sutil.MergePodSpecTolerations(ps, meta.Tolerations...)
	}

	mergeImagePullSecrets(ps, meta.ImagePullSecretNames)

	ps.SchedulerName = meta.SchedulerName

	if meta.NodeAffinityKey != "" {
		if w.fff.IsFeatureFlagEnabled(featureflag.HelmAllowCPUNodes) && !k8sutil.PodSpecRequestsGPU(ps) {
			k8sutil.SetCPUWorkloadNodeAffinity(ps, meta.NodeAffinityKey)
		}
	}

	if meta.TerminationGracePeriodSeconds != nil {
		ps.TerminationGracePeriodSeconds = meta.TerminationGracePeriodSeconds
	}
}

func (w *miniserviceMutatingWebhook) mutateNVLinkDRA(key string, pod *corev1.Pod) {
	// The ComputeDomain's fields are static for the workload so recreating it here is fine.
	cd := nvcfdra.NewSingleChannelComputeDomain()
	nvcfdra.SetComputeDomainToGPUPodResourceClaims(cd, pod)
	annos := pod.GetAnnotations()
	if idxStr := annos[nvcfdra.RequiredNVLinkDomainIndexAnnotation]; idxStr != "" {
		nvcfdra.SetRequiredNVLinkDomainSchedulingParameters(key, idxStr, pod)
	} else {
		nvcfdra.SetPreferredNVLinkDomainSchedulingParameters(key, pod)
	}
}

func (w *miniserviceMutatingWebhook) mutateGXCache(lbls map[string]string) {
	if w.fff.IsFeatureFlagEnabled(featureflag.GXCache) {
		if _, ok := lbls[nvcatypes.ShaderCacheLabelKey]; !ok {
			lbls[nvcatypes.ShaderCacheLabelKey] = strconv.FormatBool(true)
		}
	} else {
		delete(lbls, nvcatypes.ShaderCacheLabelKey)
	}
}

// mergeImagePullSecrets adds secretNames as LocalObjectReferences to the pod spec's
// ImagePullSecrets, deduplicating by name and sorting for deterministic output.
func mergeImagePullSecrets(ps *corev1.PodSpec, secretNames []string) {
	if len(secretNames) == 0 {
		return
	}
	nameSet := make(map[string]struct{}, len(secretNames))
	for _, n := range secretNames {
		nameSet[n] = struct{}{}
	}
	var out []corev1.LocalObjectReference
	for _, ref := range ps.ImagePullSecrets {
		if _, dup := nameSet[ref.Name]; !dup {
			out = append(out, ref)
		}
	}
	for _, n := range secretNames {
		out = append(out, corev1.LocalObjectReference{Name: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	ps.ImagePullSecrets = out
}
