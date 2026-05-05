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

	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	_ admission.Handler = (*helmPersistentStorageWebhook)(nil)
)

type helmPersistentStorageWebhook struct {
	scheme                           *runtime.Scheme
	decoder                          runtime.Decoder
	defaultStorageClassName          string
	internalPersistentStorageEnabled bool
}

func getHelmPersistentStorageWebhook(defaultStorageClassName string, internalPersistentStorageEnabled bool) *helmPersistentStorageWebhook {
	scheme := runtime.NewScheme()
	registerScheme(scheme, corev1.SchemeGroupVersion,
		&corev1.PersistentVolumeClaim{},
	)
	registerScheme(scheme, appsv1.SchemeGroupVersion,
		&appsv1.StatefulSet{},
	)
	return &helmPersistentStorageWebhook{
		defaultStorageClassName:          defaultStorageClassName,
		internalPersistentStorageEnabled: internalPersistentStorageEnabled,
		scheme:                           scheme,
		decoder:                          serializer.NewCodecFactory(scheme).UniversalDeserializer(),
	}
}

func newHelmPersistentStorageWebhook(defaultStorageClassName string, internalPersistentStorageEnabled bool) admission.Handler {
	return getHelmPersistentStorageWebhook(defaultStorageClassName, internalPersistentStorageEnabled)
}

// Handle handles admission requests.
func (v *helmPersistentStorageWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := core.GetLogger(ctx)

	if !v.internalPersistentStorageEnabled {
		log.Debug("internal-persistent-storage is disabled skipping helm-persistent-storage-webhook")
		return admission.Allowed("")
	}

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
			return admission.PatchResponseFromRaw(req.AdmissionRequest.Object.Raw, current) //nolint:staticcheck
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

func (v *helmPersistentStorageWebhook) decode(rawObj runtime.RawExtension, gvk metav1.GroupVersionKind) (client.Object, error) {
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

func (v *helmPersistentStorageWebhook) mutate(ctx context.Context, obj client.Object) (ok bool, warnings admission.Warnings, err error) {
	switch t := obj.(type) {
	case *corev1.PersistentVolumeClaim:
		v.mutatePVC(ctx, t)
	case *appsv1.StatefulSet:
		v.mutateStatefulSet(ctx, t)
	}

	return true, warnings, nil
}

func (v *helmPersistentStorageWebhook) mutatePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) {
	// Default the storage class name
	//nolint
	if storageClass, ok := pvc.GetAnnotations()[nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey]; ok && storageClass != "" {
		core.GetLogger(ctx).Debugf("changing pvc %s/%s storageclass from %v to %s",
			pvc.Namespace,
			pvc.Name,
			pvc.Spec.StorageClassName,
			storageClass)
		pvc.Spec.StorageClassName = &storageClass
	}
}

func (v *helmPersistentStorageWebhook) mutateStatefulSet(_ context.Context, ss *appsv1.StatefulSet) {
	for i := range ss.Spec.VolumeClaimTemplates {
		ss.Spec.VolumeClaimTemplates[i].Spec.StorageClassName = &v.defaultStorageClassName
	}
}
