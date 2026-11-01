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
	"errors"
	"fmt"
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var (
	_ admission.Handler = (*nvcaMutatingWebhook)(nil)
)

type nvcaMutatingWebhook struct {
	scheme         *runtime.Scheme
	decoder        runtime.Decoder
	fff            featureflag.Fetcher
	utilsResources corev1.ResourceList
}

func getNVCAMutatingWebhook(fff featureflag.Fetcher, utilsResources corev1.ResourceList) *nvcaMutatingWebhook {
	scheme := runtime.NewScheme()
	registerScheme(scheme, corev1.SchemeGroupVersion,
		&corev1.ConfigMap{},
		&corev1.Secret{},
		&corev1.Pod{},
		&corev1.PersistentVolumeClaim{},
		&corev1.Service{},
	)

	registerScheme(scheme, appsv1.SchemeGroupVersion,
		&appsv1.Deployment{},
		&appsv1.ReplicaSet{},
		&appsv1.StatefulSet{},
	)

	registerScheme(scheme, batchv1.SchemeGroupVersion,
		&batchv1.Job{},
		&batchv1.CronJob{},
	)
	return &nvcaMutatingWebhook{
		scheme:         scheme,
		decoder:        serializer.NewCodecFactory(scheme).UniversalDeserializer(),
		fff:            fff,
		utilsResources: utilsResources,
	}
}

func newNVCAMutatingWebhook(fff featureflag.Fetcher, utilsResources corev1.ResourceList) admission.Handler {
	return getNVCAMutatingWebhook(fff, utilsResources)
}

// Handle handles admission requests.
//
//nolint:dupl
func (v *nvcaMutatingWebhook) Handle(
	ctx context.Context,
	req admission.Request,
) admission.Response {
	log := core.GetLogger(ctx)

	gvk := schema.GroupVersionKind(req.Kind)
	if !v.scheme.Recognizes(gvk) {
		return admission.Allowed(fmt.Sprintf("gvk %q not handled by webhook", gvk))
	}

	log.WithFields(logrus.Fields{
		"gvk":  gvk,
		"name": req.Name,
	}).Debug("Validating that requested object has an instance type node selector")

	ctx = admission.NewContextWithRequest(ctx, req)

	var verr error
	var warnings []string

	switch req.Operation {
	case admissionv1.Connect,
		admissionv1.Delete:
	// NO-OP, but don't want this to display as unknown operation
	case admissionv1.Create,
		admissionv1.Update:
		obj, err := v.decode(req.Object, req.Kind)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		var ok bool
		ok, warnings, verr = v.mutate(ctx, obj)
		if ok {
			current, err := json.Marshal(obj)
			if err != nil {
				return admission.Errored(http.StatusInternalServerError, err)
			}
			return admission.PatchResponseFromRaw(req.AdmissionRequest.Object.Raw, current)
		}
	default:
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unknown operation %q", req.Operation))
	}

	// Check the error message first.
	if verr != nil {
		var apiStatus apierrors.APIStatus
		if errors.As(verr, &apiStatus) {
			return validationResponseFromStatus(apiStatus.Status()).WithWarnings(warnings...)
		}
		return admission.Denied(verr.Error()).WithWarnings(warnings...)
	}

	// Return allowed if everything succeeded.
	return admission.Allowed("").WithWarnings(warnings...)
}

func (v *nvcaMutatingWebhook) decode(rawObj runtime.RawExtension, gvk metav1.GroupVersionKind) (client.Object, error) {
	if len(rawObj.Raw) == 0 {
		return nil, fmt.Errorf("no raw data to decode")
	}
	sgvk := schema.GroupVersionKind(gvk)
	obj, _, err := v.decoder.Decode(rawObj.Raw, &sgvk, nil)
	if err != nil {
		return nil, err
	}
	return obj.(client.Object), err
}

type podMutateFunc func(ctx context.Context, ps *corev1.PodSpec) bool

func (v *nvcaMutatingWebhook) mutate(ctx context.Context, obj client.Object) (ok bool, warnings admission.Warnings, err error) {
	var (
		errs, merrs []error
		mwarnings   admission.Warnings
		mok         bool
	)

	// Pod Spec mutations
	mok, mwarnings, merrs = v.mutatePodSpecs(ctx, obj)
	errs = append(errs, merrs...)
	warnings = append(warnings, mwarnings...)
	ok = ok || mok

	return ok, warnings, errors.Join(errs...)
}

func (v *nvcaMutatingWebhook) mutatePodSpecs(ctx context.Context, obj client.Object) (ok bool, warnings admission.Warnings, errs []error) {
	var mutations []podMutateFunc

	// If explicit overwriting or kata is enabled overwrite pod resource requests
	if shouldEnforceResourceLimits(v.fff, obj) {
		// Set infra limits if needed.
		if pod, ok := obj.(*corev1.Pod); ok && k8sutil.IsUtilsPod(pod) {
			mutations = append(mutations, func(ctx context.Context, ps *corev1.PodSpec) bool {
				return defaultInfraContainerResourceLimits(ctx, v.utilsResources.DeepCopy(), ps)
			})
		}
		// Overwrite pod resource requests with limits.
		// Must be added after infra container limit defaults.
		mutations = append(mutations, overwritePodResourceRequests)
	}

	// Add the NVIDIA GPU toleration to the pod
	mutations = append(mutations, func(_ context.Context, ps *corev1.PodSpec) bool {
		return translatecommon.AddNVIDIAGPUNoScheduleToleration(ps)
	})

	// Disable automountServiceAccountToken if not an infra object or AllowWorkloadKubernetesAPIAccess is disabled.
	if !types.IsInfraOwnedObject(obj) && !v.fff.IsFeatureFlagEnabled(featureflag.AllowWorkloadKubernetesAPIAccess) {
		mutations = append(mutations, func(_ context.Context, ps *corev1.PodSpec) bool {
			if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
				b := false
				ps.AutomountServiceAccountToken = &b
				return true
			}
			return false
		})
	}

	var mod bool
	for _, mf := range mutations {
		modSpec := false
		switch t := obj.(type) {
		case *corev1.Pod:
			modSpec = mf(ctx, &t.Spec)
		case *appsv1.Deployment:
			// NO-OP: Deployments are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created by the ReplicaSet.
		case *appsv1.ReplicaSet:
			// NO-OP: ReplicaSets are intentionally not mutated. Mutating them can diverge
			// the controller-created template from the stored Deployment template and
			// trigger repeated rollout collisions. Pods are mutated directly.
		case *appsv1.StatefulSet:
			// NO-OP: StatefulSets are intentionally not mutated here. Pod mutation handles
			// the actual PodSpec when pods are created. VolumeClaimTemplates are handled
			// by the persistent storage webhook.
		case *batchv1.Job:
			// NO-OP: Jobs are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created by the Job controller.
		case *batchv1.CronJob:
			// NO-OP: CronJobs are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created by the Job controller.
		}
		mod = mod || modSpec
	}

	return mod, warnings, errs
}

func shouldEnforceResourceLimits(fff featureflag.Fetcher, obj client.Object) bool {
	return shouldEnforceKataWorkloadResourceLimits(fff, obj) ||
		shouldEnforceHelmWorkloadResourceLimits(fff, obj) ||
		shouldEnforceContainerWorkloadResourceLimits(fff, obj)
}

func shouldEnforceKataWorkloadResourceLimits(fff featureflag.Fetcher, obj client.Object) bool {
	return fff.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation) &&
		k8sutil.IsMiniServiceNamespaceName(obj.GetNamespace())
}

func shouldEnforceHelmWorkloadResourceLimits(fff featureflag.Fetcher, obj client.Object) bool {
	return ((isTaskObject(obj) && fff.IsFeatureFlagEnabled(featureflag.EnforceHelmTaskResourceLimits)) ||
		(isFunctionObject(obj) && fff.IsFeatureFlagEnabled(featureflag.EnforceHelmFunctionResourceLimits))) &&
		k8sutil.IsMiniServiceNamespaceName(obj.GetNamespace())
}

func shouldEnforceContainerWorkloadResourceLimits(fff featureflag.Fetcher, obj client.Object) bool {
	return ((isTaskObject(obj) && fff.IsFeatureFlagEnabled(featureflag.EnforceContainerTaskResourceLimits)) ||
		(isFunctionObject(obj) && fff.IsFeatureFlagEnabled(featureflag.EnforceContainerFunctionResourceLimits))) &&
		!k8sutil.IsMiniServiceNamespaceName(obj.GetNamespace())
}

func isTaskObject(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels != nil && labels[types.TaskIDUpperKey] != ""
}

func isFunctionObject(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels != nil && labels[types.FunctionIDUpperKey] != ""
}

// filterEmptyOverrideableEnvVars removes env vars that are overrideable and have empty values.
// This allows the webhook to inject non-empty values for BYOO env vars that were defined
// as empty strings in Helm charts.
func filterEmptyOverrideableEnvVars(envs []corev1.EnvVar, overrideableEnvVars map[string]bool) []corev1.EnvVar {
	if overrideableEnvVars == nil {
		return envs
	}
	filtered := make([]corev1.EnvVar, 0, len(envs))
	for _, env := range envs {
		// Keep env var if:
		// - It's not overrideable, OR
		// - It's overrideable but has a non-empty value, OR
		// - It uses valueFrom (not a literal value)
		if !overrideableEnvVars[env.Name] || env.Value != "" || env.ValueFrom != nil {
			filtered = append(filtered, env)
		}
	}
	return filtered
}

func overwritePodResourceRequests(_ context.Context, ps *corev1.PodSpec) bool {
	modInit := mergeContainerResources(ps.InitContainers)
	mod := mergeContainerResources(ps.Containers)
	return mod || modInit
}

func mergeContainerResources(containers []corev1.Container) (mod bool) {
	for i := range containers {
		if containers[i].Resources.Requests == nil {
			containers[i].Resources.Requests = corev1.ResourceList{}
		}
		if containers[i].Resources.Limits == nil {
			containers[i].Resources.Limits = corev1.ResourceList{}
		}
		c := containers[i]
		// First set limits to resource requests without a corresponding limit.
		// This ensures all resources are accounted for in both requests and limits.
		for rk, rrv := range c.Resources.Requests {
			if _, ok := c.Resources.Limits[rk]; !ok {
				c.Resources.Limits[rk] = rrv
				mod = true
			}
		}
		// Then overwrite all requests.
		for rk, lrv := range c.Resources.Limits {
			if rrv, ok := c.Resources.Requests[rk]; !ok || !rrv.Equal(lrv) {
				c.Resources.Requests[rk] = lrv
				mod = true
			}
		}
	}
	return mod
}

func defaultInfraContainerResourceLimits(_ context.Context, utilsResources corev1.ResourceList, ps *corev1.PodSpec) (mod bool) {
	for i, c := range ps.InitContainers {
		if c.Name == translatecommon.InitContainerName {
			modc := defaultContainerResourceLimits(&ps.InitContainers[i], utilsResources)
			mod = mod || modc
			break
		}
	}
	for i, c := range ps.Containers {
		if c.Name == translatecommon.UtilsContainerName {
			modc := defaultContainerResourceLimits(&ps.Containers[i], utilsResources)
			mod = mod || modc
			break
		}
	}
	return mod
}

func defaultContainerResourceLimits(c *corev1.Container, limits corev1.ResourceList) (mod bool) {
	if len(c.Resources.Limits) == 0 {
		c.Resources.Limits = limits
		return true
	}
	for rk, rv := range limits {
		if _, ok := c.Resources.Limits[rk]; !ok {
			c.Resources.Limits[rk] = rv
			mod = true
		}
	}
	return mod
}
