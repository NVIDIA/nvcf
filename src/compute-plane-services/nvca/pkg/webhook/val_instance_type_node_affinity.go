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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilerror "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type instanceTypeNodeAffinityValWebhookHandler struct {
	scheme  *runtime.Scheme
	decoder runtime.Decoder
}

func newInstanceTypeNodeAffinityValWebhookHandler() admission.Handler {
	scheme := runtime.NewScheme()

	registerScheme(scheme, v1.SchemeGroupVersion,
		&v1.Pod{},
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
	return &instanceTypeNodeAffinityValWebhookHandler{
		scheme:  scheme,
		decoder: serializer.NewCodecFactory(scheme).UniversalDeserializer(),
	}
}

func registerScheme(scheme *runtime.Scheme, gv schema.GroupVersion, ts ...runtime.Object) {
	scheme.AddKnownTypes(gv, ts...)
	metav1.AddToGroupVersion(scheme, gv)
}

// Handle handles admission requests.
func (v *instanceTypeNodeAffinityValWebhookHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := core.GetLogger(ctx)

	gvk := schema.GroupVersionKind(req.Kind)

	if !v.scheme.Recognizes(gvk) {
		return admission.Allowed(fmt.Sprintf("gvk %q not handled by webhook", gvk))
	}

	log.WithFields(logrus.Fields{
		"gvk": gvk,
	}).Debug("Validating that requested object has an instance type node selector")

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

func (v *instanceTypeNodeAffinityValWebhookHandler) decode(rawObj runtime.RawExtension, gvk metav1.GroupVersionKind) (runtime.Object, error) {
	if len(rawObj.Raw) == 0 {
		return nil, fmt.Errorf("no raw data to decode")
	}
	sgvk := schema.GroupVersionKind(gvk)
	obj, _, err := v.decoder.Decode(rawObj.Raw, &sgvk, nil)
	return obj, err
}

func (v *instanceTypeNodeAffinityValWebhookHandler) validateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return v.validate(ctx, obj)
}

func (v *instanceTypeNodeAffinityValWebhookHandler) validateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return v.validate(ctx, newObj)
}

func (v *instanceTypeNodeAffinityValWebhookHandler) validate(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	var errors []error
	switch t := obj.(type) {
	case *v1.Pod:
		var nodeSel *v1.NodeSelector
		if t.Spec.Affinity != nil && t.Spec.Affinity.NodeAffinity != nil {
			nodeSel = t.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(t.Spec.NodeSelector, nodeSel)
	case *appsv1.Deployment:
		s := t.Spec.Template.Spec
		var nodeSel *v1.NodeSelector
		if s.Affinity != nil && s.Affinity.NodeAffinity != nil {
			nodeSel = s.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(s.NodeSelector, nodeSel)
	case *appsv1.ReplicaSet:
		s := t.Spec.Template.Spec
		var nodeSel *v1.NodeSelector
		if s.Affinity != nil && s.Affinity.NodeAffinity != nil {
			nodeSel = s.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(s.NodeSelector, nodeSel)
	case *appsv1.StatefulSet:
		s := t.Spec.Template.Spec
		var nodeSel *v1.NodeSelector
		if s.Affinity != nil && s.Affinity.NodeAffinity != nil {
			nodeSel = s.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(s.NodeSelector, nodeSel)
	case *batchv1.Job:
		s := t.Spec.Template.Spec
		var nodeSel *v1.NodeSelector
		if s.Affinity != nil && s.Affinity.NodeAffinity != nil {
			nodeSel = s.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(s.NodeSelector, nodeSel)
	case *batchv1.CronJob:
		s := t.Spec.JobTemplate.Spec.Template.Spec
		var nodeSel *v1.NodeSelector
		if s.Affinity != nil && s.Affinity.NodeAffinity != nil {
			nodeSel = s.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		warnings, errors = v.validateNodeSelection(s.NodeSelector, nodeSel)
	}

	return warnings, utilerror.NewAggregate(errors)
}

// validateNodeSelection ensures that a node selector or affinity for
// the uniform instance-type label is present.
// Validating webhooks run after mutating webhooks,
// so node selectors for deprecated instance-type labels will already
// be transformed to uniform labels by the pod node affinity mutating webhook.
func (v *instanceTypeNodeAffinityValWebhookHandler) validateNodeSelection(
	nodeSel map[string]string,
	nodeAffSel *v1.NodeSelector,
) (warnings admission.Warnings, errs []error) {
	// Node selectors and affinities are AND'ed together, so having a node selector
	// is sufficient for proper scheduling.
	if nodeSel != nil && nodeSel[instanceTypeLK] != "" {
		return nil, nil
	}
	if nodeAffSel == nil {
		errs = append(errs, fmt.Errorf("no valid node selector for %s", instanceTypeLK))
		return warnings, errs
	}
	nodeAffSelHasITLabel := true
	for _, nst := range nodeAffSel.NodeSelectorTerms {
		selTermHasValidITLabel := false
		for _, me := range nst.MatchExpressions {
			if me.Key == instanceTypeLK && me.Operator == v1.NodeSelectorOpIn &&
				len(me.Values) == 1 && me.Values[0] != "" {
				selTermHasValidITLabel = true
				break
			}
		}
		nodeAffSelHasITLabel = nodeAffSelHasITLabel && selTermHasValidITLabel
	}

	if !nodeAffSelHasITLabel {
		errs = append(errs, fmt.Errorf("no valid node selector or affinity for %s", instanceTypeLK))
		return warnings, errs
	}
	return warnings, errs
}

func validationResponseFromStatus(status metav1.Status) admission.Response {
	return admission.Response{
		AdmissionResponse: admissionv1.AdmissionResponse{
			Allowed: false,
			Result:  &status,
		},
	}
}
