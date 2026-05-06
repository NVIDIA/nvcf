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
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
)

type helmMiniServiceValWebhookHandler struct {
	decoder runtime.Decoder
	fff     featureflag.Fetcher
}

func newHelmMiniServiceValWebhookHandler(fff featureflag.Fetcher) admission.Handler {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	return &helmMiniServiceValWebhookHandler{
		fff:     fff,
		decoder: serializer.NewCodecFactory(scheme).UniversalDeserializer(),
	}
}

// Handle handles admission requests.
func (v *helmMiniServiceValWebhookHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := core.GetLogger(ctx)

	gvk := schema.GroupVersionKind(req.Kind)

	log.WithFields(logrus.Fields{
		"gvk": gvk,
	}).Debug("Validating request")

	if !miniservice.HelmChartInstanceServiceAccountNameRegexp.MatchString(req.UserInfo.Username) {
		log.WithField("user", req.UserInfo.Username).
			Debug("Skipping validation of non-ICMS instance user request")
		return admission.Allowed("non-ICMS instance user request skipped")
	}

	ctx = admission.NewContextWithRequest(ctx, req)

	var verr error
	var warnings []string

	switch req.Operation {
	case admissionv1.Connect, admissionv1.Delete:
	case admissionv1.Create:
		obj, err := v.decode(req.Object, req.Kind)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		warnings, verr = v.validateCreate(ctx, obj)
	case admissionv1.Update:
		oldObj, err := v.decode(req.OldObject, req.Kind)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		newObj, err := v.decode(req.Object, req.Kind)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		warnings, verr = v.validateUpdate(ctx, oldObj, newObj)
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

func (v *helmMiniServiceValWebhookHandler) decode(rawObj runtime.RawExtension, gvk metav1.GroupVersionKind) (client.Object, error) {
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

func (v *helmMiniServiceValWebhookHandler) validateCreate(ctx context.Context, obj client.Object) (admission.Warnings, error) {
	return v.validate(ctx, obj)
}

func (v *helmMiniServiceValWebhookHandler) validateUpdate(ctx context.Context, _, newObj client.Object) (admission.Warnings, error) {
	return v.validate(ctx, newObj)
}

func (v *helmMiniServiceValWebhookHandler) validate(ctx context.Context, obj client.Object) (warnings admission.Warnings, err error) {
	var errs []error

	if shouldEnforceResourceLimits(v.fff, obj) {
		warns, verrs := v.validateResourceLimits(ctx, obj)
		warnings = append(warnings, warns...)
		errs = append(errs, verrs...)
	}

	return warnings, errors.Join(errs...)
}

func (v *helmMiniServiceValWebhookHandler) validateResourceLimits(ctx context.Context, obj client.Object) (
	warnings admission.Warnings,
	errs []error,
) {
	switch t := obj.(type) {
	case *corev1.Pod:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec)
	case *appsv1.Deployment:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec.Template.Spec)
	case *appsv1.ReplicaSet:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec.Template.Spec)
	case *appsv1.StatefulSet:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec.Template.Spec)
	case *batchv1.Job:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec.Template.Spec)
	case *batchv1.CronJob:
		warnings, errs = validatePodSpecLimits(ctx, t.Spec.JobTemplate.Spec.Template.Spec)
	}
	return warnings, errs
}

func validatePodSpecLimits(_ context.Context, ps corev1.PodSpec) (warnings admission.Warnings, errs []error) {
	cwarns, cerrs := validateContainerLimits(ps.Containers)
	warnings = append(warnings, cwarns...)
	errs = append(errs, cerrs...)
	cwarns, cerrs = validateContainerLimits(ps.InitContainers)
	warnings = append(warnings, cwarns...)
	errs = append(errs, cerrs...)
	return warnings, errs
}

func validateContainerLimits(containers []corev1.Container) (warnings admission.Warnings, errs []error) {
	for _, c := range containers {
		if len(c.Resources.Limits) == 0 {
			errs = append(errs, fmt.Errorf("container %s has no resource limits", c.Name))
			continue
		}
		// The only required resources are CPU and memory, since ResourceQuota's with limits
		// require those resource limits be set.
		var missingRNs []string
		for _, rk := range []corev1.ResourceName{
			corev1.ResourceCPU,
			corev1.ResourceMemory,
		} {
			if _, ok := c.Resources.Limits[rk]; !ok {
				missingRNs = append(missingRNs, rk.String())
			}
		}
		if len(missingRNs) != 0 {
			errs = append(errs, fmt.Errorf("container %s missing resource limits: %q", c.Name, missingRNs))
		}
		// Only resources in instance types are allowed.
		// For now these are: cpu, memory, ephemeral-storage, GPUs.
		var badRNs []string
		for rk, rv := range c.Resources.Limits {
			switch rk {
			case corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage:
			default:
				if !strings.HasPrefix(rk.String(), "nvidia.com/") && !rv.IsZero() {
					badRNs = append(badRNs, rk.String())
				}
			}
		}
		if len(badRNs) != 0 {
			errs = append(errs, fmt.Errorf("container %s has non-zero disallowed resources in limits: %q", c.Name, badRNs))
		}
	}
	return warnings, errs
}
