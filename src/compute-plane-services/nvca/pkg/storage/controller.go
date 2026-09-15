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

package storage

import (
	"context"
	"time"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
)

var (
	SchemeBuilder = runtime.NewSchemeBuilder(
		nvcav2beta1.AddToScheme,
		nvcav1new.AddToScheme,
		nvcav2beta1.AddToScheme,
		v1alpha1.AddToScheme,
		corev1.AddToScheme,
		coordv1.AddToScheme,
		appsv1.AddToScheme,
		rbacv1.AddToScheme,
		netv1.AddToScheme,
		storagev1.AddToScheme,
		batchv1.AddToScheme,
	)

	mgrScheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(SchemeBuilder.AddToScheme(mgrScheme))
}

type ControllerOptions struct {
	ICMSRequestNamespace  string
	CSIVolumeMountOptions []string
	Metrics               *metrics.Metrics
	// BARTClient when set enables get/update for both v1 and v2beta1 StorageRequests and watch of both; create uses v2beta1 only.
	BARTClient versioned.Interface

	// Overridden for testing.
	nowFunc func() time.Time
}

// BuildController creates a controller for StorageRequests
func BuildController(
	cfg nvcaconfig.Config,
	storageReqType nvcav1new.StorageRequestType,
	mgr ctrl.Manager,
	clusterName string,
	clusterRegion string,
	k8sTimeConfig *k8sutil.TimeConfig,
	opts ControllerOptions,
) error {
	reconcilerOpts := []ReconcilerOption{
		WithNowFunc(opts.nowFunc),
		WithICMSRequestNamespace(opts.ICMSRequestNamespace),
		WithCSIVolumeMountOptions(opts.CSIVolumeMountOptions),
		WithMetrics(opts.Metrics),
	}
	if opts.BARTClient != nil {
		reconcilerOpts = append(reconcilerOpts, WithStorageRequestAPI(NewStorageRequestAPI(opts.BARTClient)))
	}
	r := NewReconciler(cfg, mgr.GetClient(),
		serializer.NewCodecFactory(mgr.GetScheme()).UniversalDeserializer(),
		mgr.GetEventRecorderFor("storage-controller"),
		clusterName,
		clusterRegion,
		k8sTimeConfig,
		reconcilerOpts...,
	)

	if storageReqType == nvcav1new.ModelCacheRequest {
		return buildControllerModelCache(r, mgr, opts)
	}

	clusterwideEventHandler := handler.EnqueueRequestsFromMapFunc(getClusterWideEventHandlerMapFunc(storageReqType))
	storageReqPredicate := predicate.NewPredicateFuncs(filterOnStorageRequestType(storageReqType))
	b := builder.
		ControllerManagedBy(mgr).
		Named(string(storageReqType)).
		For(&nvcav1new.StorageRequest{}, builder.WithPredicates(storageReqPredicate)).
		WithEventFilter(storageReqPredicate)
	if opts.BARTClient != nil {
		enqueueV2Beta1 := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		})
		b = b.Watches(&nvcav2beta1.StorageRequest{}, enqueueV2Beta1, builder.WithPredicates(storageReqPredicate))
	}
	return b.
		// Controller owner references will be set on these objects.
		Owns(&batchv1.Job{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ResourceQuota{}).
		// Leases are namespaced but are created in one namespace, so use CW predicate to scope events.
		Watches(&coordv1.Lease{}, clusterwideEventHandler).
		// StorageRequests are namespace scoped but these are cluster scoped,
		// so can only be watched.
		Watches(&corev1.PersistentVolume{}, clusterwideEventHandler).
		Watches(&storagev1.StorageClass{}, clusterwideEventHandler).
		Complete(r)
}

func filterOnStorageRequestType(storageReqType nvcav1new.StorageRequestType) func(object client.Object) bool {
	return func(object client.Object) bool {
		if st, ok := object.(*nvcav1new.StorageRequest); ok {
			return st.Spec.Type == storageReqType
		}
		if st, ok := object.(*nvcav2beta1.StorageRequest); ok {
			return st.Spec.Type == nvcav2beta1.StorageRequestType(storageReqType)
		}
		if stReqName, ok := object.GetLabels()[StorageRequestOwnerKey]; ok {
			// Check the labels on the resource and if it has the owner key and matches the type we should watch it
			return stReqName == storageReqType.Name()
		}
		// Lastly check if it has an owner reference or not
		// This filters for the controller type and should not
		// filter out by namespace
		return hasStorageRequestOwnerReference(storageReqType, object)
	}
}

func hasStorageRequestOwnerReference(storageReqType nvcav1new.StorageRequestType, object client.Object) bool {
	_, found := getStorageRequestOwnerReference(storageReqType, object)
	return found
}

func getStorageRequestOwnerReference(storageReqType nvcav1new.StorageRequestType, object client.Object) (metav1.OwnerReference, bool) {
	for _, ownerRef := range object.GetOwnerReferences() {
		if ownerRef.Kind != "StorageRequest" || ownerRef.Name != storageReqType.Name() {
			continue
		}
		gv := storageRequestGVK.GroupVersion().String()
		gv2 := storageRequestV2Beta1GVK.GroupVersion().String()
		if ownerRef.APIVersion == gv || ownerRef.APIVersion == gv2 {
			return ownerRef, true
		}
	}
	return metav1.OwnerReference{}, false
}

func getClusterWideEventHandlerMapFunc(storageReqType nvcav1new.StorageRequestType) handler.MapFunc {
	return func(_ context.Context, o client.Object) []reconcile.Request {
		labels := o.GetLabels()
		if len(labels) == 0 {
			return nil
		}
		srName := labels[StorageRequestOwnerKey]
		srNamespace := labels[StorageRequestNamespaceKey]
		if srName != storageReqType.Name() || srNamespace == "" {
			return nil
		}
		return []reconcile.Request{
			{NamespacedName: client.ObjectKey{Namespace: srNamespace, Name: srName}},
		}
	}
}
