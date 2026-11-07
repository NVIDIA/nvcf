// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mscontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

const (
	// NvSnapInjectLabel is the label the NvSnap admission webhook uses to skip a namespace.
	// A namespace labelled "false" is never mutated by NvSnap.
	NvSnapInjectLabel = "nvsnap.io/inject"

	nvsnapInjectOptOut = "false"

	// NvSnapInjectManagedAnnotation marks a namespace whose nvsnap.io/inject label was set from the
	// workload, so it can be removed when the workload stops setting a model cache path. A label set
	// any other way, such as cluster configuration, is never removed.
	NvSnapInjectManagedAnnotation = "nvca.nvcf.nvidia.io/nvsnap-inject-managed"
)

// modelCachePathEnvNames are the variables that choose where a workload keeps its model
// weights. NvSnap redirects the same variables to its own volume when it admits a pod.
var modelCachePathEnvNames = map[string]struct{}{
	"HF_HOME":        {},
	"NIM_CACHE_PATH": {},
}

// nvsnapNamespaceLabels returns the labels that keep NvSnap out of a workload namespace.
// A workload that sets its own model cache path has chosen where its model lives, and the
// NvSnap redirect would leave that location unused. Other workloads are left to NvSnap.
func nvsnapNamespaceLabels(objs []client.Object) map[string]string {
	if !workloadSetsModelCachePath(objs) {
		return nil
	}
	return map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}
}

// workloadSetsModelCachePath reports whether any rendered workload object sets one of
// modelCachePathEnvNames in an env list. The search covers every object generically,
// because operator CRDs such as a DynamoGraphDeployment carry env lists NVCA cannot type.
func workloadSetsModelCachePath(objs []client.Object) bool {
	for _, obj := range objs {
		content, ok := objectContent(obj)
		if ok && hasModelCachePathEnv(content) {
			return true
		}
	}
	return false
}

// objectContent converts typed and unknown workload objects into the same searchable representation.
func objectContent(obj client.Object) (map[string]any, bool) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.Object, true
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, false
	}
	return content, true
}

// envListKeys are the field names that hold container env lists: "env" on Pods and pod templates,
// and "envs" on operator CRDs such as a DynamoGraphDeployment.
var envListKeys = map[string]struct{}{
	"env":  {},
	"envs": {},
}

// hasModelCachePathEnv walks a decoded object and reports whether any env list in it sets one of
// modelCachePathEnvNames. Only entries of an env list count, so a ConfigMap whose data happens to
// have keys named "name" and "value" does not.
func hasModelCachePathEnv(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for key, child := range t {
			if _, isEnvList := envListKeys[key]; isEnvList {
				if list, ok := child.([]any); ok && envListSetsModelCachePath(list) {
					return true
				}
			}
			if hasModelCachePathEnv(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if hasModelCachePathEnv(child) {
				return true
			}
		}
	}
	return false
}

// envListSetsModelCachePath reports whether an env list has an entry for one of
// modelCachePathEnvNames with a value, literal or by reference.
func envListSetsModelCachePath(list []any) bool {
	for _, entry := range list {
		env, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := env["name"].(string)
		if _, isPath := modelCachePathEnvNames[name]; !isPath {
			continue
		}
		// The workload chose the location either way: a literal value, or one read from a ConfigMap,
		// Secret or field reference.
		if value, _ := env["value"].(string); value != "" || env["valueFrom"] != nil {
			return true
		}
	}
	return false
}

// releaseNvSnapOptOut gives an instance namespace back to NvSnap once the workload no longer sets a
// model cache path. It only removes a label NVCA set itself, marked by NvSnapInjectManagedAnnotation.
// A label from cluster configuration, or one set by anyone else, is left alone.
//
// Call it only after the workload objects that no longer use the path were applied, because until then
// the old workload can still create pods that NvSnap would redirect.
//
// Known limit: a successful apply does not mean the rollout has finished. Pods that the previous revision
// still creates during the rollout (for example from an old ReplicaSet) can be redirected once the label is
// gone. Waiting for that needs a convergence check per workload kind, which NVCA cannot do for operator CRDs.
func (r *Reconciler) releaseNvSnapOptOut(ctx context.Context, ms *v1alpha1.MiniService, workloadLabels map[string]string) error {
	if _, wanted := workloadLabels[NvSnapInjectLabel]; wanted {
		return nil
	}
	if _, configured := r.InstanceNamespaceLabels[NvSnapInjectLabel]; configured {
		return nil
	}

	log := logf.FromContext(ctx).WithValues("namespace", ms.Spec.Namespace, "miniservice", ms.Name)

	namespace := &corev1.Namespace{}
	if err := r.nvsnapLiveReader().Get(ctx, client.ObjectKey{Name: ms.Spec.Namespace}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get namespace %q to release the NvSnap opt-out of MiniService %q: %w",
			ms.Spec.Namespace, ms.Name, err)
	}
	if namespace.Annotations[NvSnapInjectManagedAnnotation] != "true" {
		log.V(1).Info("Keeping the NvSnap opt-out, the workload did not set it")
		return nil
	}

	if namespace.Labels[NvSnapInjectLabel] == nvsnapInjectOptOut {
		delete(namespace.Labels, NvSnapInjectLabel)
	}
	delete(namespace.Annotations, NvSnapInjectManagedAnnotation)
	log.Info("Releasing the NvSnap opt-out, the workload no longer sets a model cache path")
	if err := r.Client.Update(ctx, namespace); err != nil {
		return fmt.Errorf("update namespace %q to release the NvSnap opt-out of MiniService %q: %w",
			ms.Spec.Namespace, ms.Name, err)
	}
	return nil
}

// restoreNvSnapOptOutAfterFailedUpdate releases a newly introduced opt-out only when no live target
// needs it. Existing opt-outs remain protected, including workloads omitted from the new revision.
// The API reader bypasses the informer cache so a partial apply is visible before releasing it.
func (r *Reconciler) restoreNvSnapOptOutAfterFailedUpdate(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	previousNamespace *corev1.Namespace,
	objs []client.Object,
) error {
	log := logf.FromContext(ctx).WithValues("namespace", ms.Spec.Namespace, "miniservice", ms.Name)

	if previousNamespace.Labels[NvSnapInjectLabel] == nvsnapInjectOptOut {
		log.V(1).Info("Keeping the NvSnap opt-out after the failed update, the namespace already had one")
		return nil
	}
	for _, obj := range objs {
		gvk, err := r.getObjectGVK(ctx, obj)
		if err != nil {
			return fmt.Errorf("resolve the kind of %T %q for MiniService %q: %w", obj, obj.GetName(), ms.Name, err)
		}
		namespaced, err := apiutil.IsGVKNamespaced(gvk, r.Client.RESTMapper())
		if err != nil {
			return fmt.Errorf("check whether %s is namespaced for MiniService %q: %w", gvk, ms.Name, err)
		}
		key := client.ObjectKey{Name: obj.GetName()}
		if namespaced {
			key.Namespace = ms.Spec.Namespace
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(gvk)
		if err := r.nvsnapLiveReader().Get(ctx, key, live); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("read live %s %s: %w", gvk, key, err)
		}
		if workloadSetsModelCachePath([]client.Object{live}) {
			log.Info("Keeping the NvSnap opt-out after the failed update, a live workload sets a model cache path",
				"workload", key.String(), "kind", gvk.String())
			return nil
		}
	}
	return r.releaseNvSnapOptOut(ctx, ms, nil)
}

// nvsnapLiveReader reads admission state directly after namespace and workload writes.
func (r *Reconciler) nvsnapLiveReader() client.Reader {
	if r.apiReader != nil {
		return r.apiReader
	}
	return r.Client
}
