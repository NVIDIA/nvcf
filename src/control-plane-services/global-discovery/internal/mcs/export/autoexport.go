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

package export

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/identity"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/client"
)

// AutoExportControllerName names the label-driven export controller.
const AutoExportControllerName = "autoexport"

// Managed reports whether the controller created the ServiceExport.
func Managed(se *mcsv1beta1.ServiceExport) bool {
	return se.Labels[api.LabelManagedBy] == api.ManagedByController
}

// managedLabels are the labels of every ServiceExport the controller
// creates.
func managedLabels(clusterset string) map[string]string {
	return map[string]string{api.LabelManagedBy: api.ManagedByController, api.LabelClusterSet: clusterset}
}

// ownerRef returns the owner reference of a ServiceExport to its Service.
func ownerRef(svc *corev1.Service) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "v1", Kind: "Service", Name: svc.Name, UID: svc.UID, Controller: ptrTo(true),
	}
}

func ptrTo[T any](v T) *T { return &v }

func hasOwner(se *mcsv1beta1.ServiceExport, uid types.UID) bool {
	return slices.ContainsFunc(se.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == uid })
}

// AutoExporterOptions configure an AutoExporter.
type AutoExporterOptions struct {
	Client                  ctrlclient.Client
	Identity                identity.Identity
	Metrics                 *metrics.Metrics
	MaxConcurrentReconciles int
	WatchesNamespace        func(string) bool
}

// AutoExporter is the label-driven half of auto-export: a Service labelled
// gds.nvidia.com/export=true gets a ServiceExport of the same name, owned by
// the Service and labelled gds.nvidia.com/managed-by, which is deleted when
// the label goes. ServiceExports the controller did not create are never
// touched. A ServiceExport also kept for a pool (gds.nvidia.com/pool-id)
// survives the label's removal; the pool half removes it.
type AutoExporter struct {
	opts AutoExporterOptions
}

// NewAutoExporter returns an AutoExporter.
func NewAutoExporter(opts AutoExporterOptions) *AutoExporter {
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	if opts.MaxConcurrentReconciles < 1 {
		opts.MaxConcurrentReconciles = 1
	}
	if opts.WatchesNamespace == nil {
		opts.WatchesNamespace = func(string) bool { return true }
	}
	return &AutoExporter{opts: opts}
}

// +kubebuilder:rbac:groups=multicluster.x-k8s.io,resources=serviceexports,verbs=create;delete

// SetupWithManager registers the controller on Services and on the
// ServiceExports it manages.
func (a *AutoExporter) SetupWithManager(mgr ctrl.Manager) error {
	managed := predicate.NewPredicateFuncs(func(o ctrlclient.Object) bool {
		return o.GetLabels()[api.LabelManagedBy] == api.ManagedByController
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(AutoExportControllerName).
		For(&corev1.Service{}).
		Watches(&mcsv1beta1.ServiceExport{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o ctrlclient.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}}}
		}), builder.WithPredicates(managed)).
		WithOptions(controller.Options{MaxConcurrentReconciles: a.opts.MaxConcurrentReconciles}).
		Complete(a)
}

// Reconcile implements reconcile.Reconciler for the Service req.
func (a *AutoExporter) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	start := time.Now()
	defer func() { a.opts.Metrics.ObserveReconcile(metrics.ControllerAutoExport, start, err) }()
	if !a.opts.WatchesNamespace(req.Namespace) {
		return ctrl.Result{}, nil
	}
	logger := log.FromContext(ctx)
	var svc corev1.Service
	svcFound := true
	if err := a.opts.Client.Get(ctx, req.NamespacedName, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		svcFound = false
	}
	want := svcFound && svc.DeletionTimestamp.IsZero() && api.IsExported(svc.Labels)

	var se mcsv1beta1.ServiceExport
	seFound := true
	if err := a.opts.Client.Get(ctx, req.NamespacedName, &se); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		seFound = false
	}

	switch {
	case want && !seFound:
		se = mcsv1beta1.ServiceExport{ObjectMeta: metav1.ObjectMeta{
			Namespace: req.Namespace, Name: req.Name,
			Labels:          managedLabels(a.opts.Identity.ClusterSet),
			OwnerReferences: []metav1.OwnerReference{ownerRef(&svc)},
		}}
		if err := a.opts.Client.Create(ctx, &se); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("create ServiceExport: %w", err)
		}
		logger.Info("Created ServiceExport for a labelled Service")
	case want && Managed(&se) && !hasOwner(&se, svc.UID):
		// Created for a pool first, or the Service was re-created: own it.
		patch := ctrlclient.MergeFrom(se.DeepCopy())
		se.OwnerReferences = append(slices.DeleteFunc(se.OwnerReferences, func(o metav1.OwnerReference) bool {
			return o.Kind == "Service" && o.APIVersion == "v1"
		}), ownerRef(&svc))
		if err := a.opts.Client.Patch(ctx, &se, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("set ServiceExport owner: %w", err)
		}
	case !want && seFound && Managed(&se) && se.DeletionTimestamp.IsZero():
		if se.Labels[api.LabelPoolID] != "" {
			// Kept for a pool: drop only the Service ownership.
			if !svcFound || !hasOwner(&se, svc.UID) {
				return ctrl.Result{}, nil
			}
			patch := ctrlclient.MergeFrom(se.DeepCopy())
			se.OwnerReferences = slices.DeleteFunc(se.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == svc.UID })
			return ctrl.Result{}, ctrlclient.IgnoreNotFound(a.opts.Client.Patch(ctx, &se, patch))
		}
		if err := a.opts.Client.Delete(ctx, &se); ctrlclient.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete ServiceExport: %w", err)
		}
		logger.Info("Deleted the ServiceExport of a Service no longer labelled for export")
	}
	return ctrl.Result{}, nil
}

// PoolExporterOptions configure a PoolExporter.
type PoolExporterOptions struct {
	Client   ctrlclient.Client
	GDS      *client.Client
	Identity identity.Identity
	Metrics  *metrics.Metrics
	Logger   logr.Logger
	// WatchesNamespace reports whether a namespace is handled; nil means
	// every namespace.
	WatchesNamespace func(string) bool
	// Interval is the period of the full sync in addition to pool changes;
	// default client.DefaultCacheTTL.
	Interval time.Duration
}

// PoolExporter is the pool half of auto-export, a leader-only
// manager.Runnable. It watches the pools of this cluster (a client.PoolCache
// filtered to the cluster) and, for every pool registered through the API,
// ensures a ServiceExport for each entry of services[], labelled
// gds.nvidia.com/pool-id. It keeps those exports while the pool exists in
// any state but GONE (UNHEALTHY included: removing the export would keep the
// pool from ever becoming healthy) and removes only the exports it labelled
// when the pool is GONE or deleted. A ServiceExport the controller did not
// create is never touched.
type PoolExporter struct {
	opts PoolExporterOptions
}

// NewPoolExporter returns a PoolExporter.
func NewPoolExporter(opts PoolExporterOptions) *PoolExporter {
	if opts.Metrics == nil {
		opts.Metrics = metrics.New()
	}
	if opts.WatchesNamespace == nil {
		opts.WatchesNamespace = func(string) bool { return true }
	}
	if opts.Interval <= 0 {
		opts.Interval = client.DefaultCacheTTL
	}
	return &PoolExporter{opts: opts}
}

// NeedLeaderElection makes the pool exporter leader-only.
func (p *PoolExporter) NeedLeaderElection() bool { return true }

// Start runs the pool cache and syncs exports after every change and every
// Interval until ctx is done.
func (p *PoolExporter) Start(ctx context.Context) error {
	cache, err := client.NewPoolCache(p.opts.GDS, client.PoolCacheConfig{
		Filter: &gdsv1.ListPoolsRequest{Clusterset: p.opts.Identity.ClusterSet, Cluster: p.opts.Identity.Cluster},
	})
	if err != nil {
		return err
	}
	updates := cache.Updates()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cache.Run(ctx)
	}()
	defer func() { <-done }()
	tick := time.NewTicker(p.opts.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-updates:
			if !ok {
				return nil
			}
		case <-tick.C:
		}
		if err := p.Sync(ctx, cache.Snapshot()); err != nil {
			p.opts.Logger.Info("Pool export sync failed; retrying on the next change", "error", err.Error())
		}
	}
}

// Sync makes the pool exports match a pool snapshot. It does nothing until
// the snapshot is synced, and removes nothing while it is stale, so that an
// unreachable GDS never deletes exports.
func (p *PoolExporter) Sync(ctx context.Context, st *client.PoolState) (err error) {
	start := time.Now()
	defer func() { p.opts.Metrics.ObserveReconcile(metrics.ControllerPoolExport, start, err) }()
	if st == nil || !st.Synced {
		return nil
	}
	desired := map[types.NamespacedName]string{}
	for _, pool := range st.Pools {
		if !apiRegistered(pool) || pool.GetState() == gdsv1.PoolState_POOL_STATE_GONE ||
			pool.GetLocation().GetCluster() != p.opts.Identity.Cluster {
			continue
		}
		for _, ref := range pool.GetServices() {
			key := types.NamespacedName{Namespace: ref.GetNamespace(), Name: ref.GetName()}
			if key.Namespace == "" || key.Name == "" || !p.opts.WatchesNamespace(key.Namespace) {
				continue
			}
			if cur, ok := desired[key]; !ok || pool.GetPoolId() < cur {
				desired[key] = pool.GetPoolId()
			}
		}
	}
	var errs []error
	for _, key := range slices.SortedFunc(maps.Keys(desired), compareKeys) {
		if err := p.ensure(ctx, key, desired[key]); err != nil {
			errs = append(errs, err)
		}
	}
	var list mcsv1beta1.ServiceExportList
	if err := p.opts.Client.List(ctx, &list, ctrlclient.HasLabels{api.LabelPoolID},
		ctrlclient.MatchingLabels{api.LabelManagedBy: api.ManagedByController}); err != nil {
		return fmt.Errorf("list pool ServiceExports: %w", err)
	}
	kept := 0
	for i := range list.Items {
		se := &list.Items[i]
		key := types.NamespacedName{Namespace: se.Namespace, Name: se.Name}
		if _, ok := desired[key]; ok {
			kept++
			continue
		}
		if st.Stale || !p.opts.WatchesNamespace(key.Namespace) {
			kept++
			continue
		}
		if err := p.remove(ctx, se); err != nil {
			errs = append(errs, err)
		}
	}
	p.opts.Metrics.PoolExports.Set(float64(kept))
	if len(errs) > 0 {
		return fmt.Errorf("pool exports: %v", errs)
	}
	return nil
}

// apiRegistered reports whether a pool was registered through the API: such
// pools carry a lease, label-only pools do not.
func apiRegistered(p *gdsv1.Pool) bool { return p.GetLease() != nil }

func compareKeys(a, b types.NamespacedName) int {
	return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

// ensure creates the pool ServiceExport of key or labels the managed one
// with poolID.
func (p *PoolExporter) ensure(ctx context.Context, key types.NamespacedName, poolID string) error {
	var se mcsv1beta1.ServiceExport
	err := p.opts.Client.Get(ctx, key, &se)
	switch {
	case apierrors.IsNotFound(err):
		labels := managedLabels(p.opts.Identity.ClusterSet)
		labels[api.LabelPoolID] = poolID
		se = mcsv1beta1.ServiceExport{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: labels}}
		if err := p.opts.Client.Create(ctx, &se); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create ServiceExport %s for pool %s: %w", key, poolID, err)
		}
		p.opts.Logger.Info("Created ServiceExport for a pool", "namespace", key.Namespace, "name", key.Name, "poolId", poolID)
		return nil
	case err != nil:
		return err
	case !Managed(&se) || se.Labels[api.LabelPoolID] == poolID || !se.DeletionTimestamp.IsZero():
		return nil
	}
	patch := ctrlclient.MergeFrom(se.DeepCopy())
	se.Labels[api.LabelPoolID] = poolID
	return p.opts.Client.Patch(ctx, &se, patch)
}

// remove deletes a pool ServiceExport, or only drops its pool label when the
// Service is also labelled for export.
func (p *PoolExporter) remove(ctx context.Context, se *mcsv1beta1.ServiceExport) error {
	key := types.NamespacedName{Namespace: se.Namespace, Name: se.Name}
	var svc corev1.Service
	err := p.opts.Client.Get(ctx, key, &svc)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && api.IsExported(svc.Labels) {
		patch := ctrlclient.MergeFrom(se.DeepCopy())
		delete(se.Labels, api.LabelPoolID)
		return ctrlclient.IgnoreNotFound(p.opts.Client.Patch(ctx, se, patch))
	}
	if err := p.opts.Client.Delete(ctx, se); ctrlclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete pool ServiceExport %s: %w", key, err)
	}
	p.opts.Logger.Info("Deleted the ServiceExport of a pool that is gone", "namespace", key.Namespace, "name", key.Name,
		"poolId", se.Labels[api.LabelPoolID])
	return nil
}
