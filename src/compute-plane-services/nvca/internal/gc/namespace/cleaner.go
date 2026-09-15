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

package namespace

import (
	"context"
	"fmt"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sourcegraph/conc/pool"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	metricsgctypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/gctypes"
	bartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Cleaner handles the cleanup of dangling namespaces & StorageRequests.
type Cleaner struct {
	k8sClient         kubernetes.Interface
	nvcaClient        bartclient.Interface
	icmsRequestGetter ICMSRequestGetter
	metrics           *metrics.Metrics
}

// NewCleaner creates a new namespace cleaner.
func NewCleaner(
	k8sClient kubernetes.Interface,
	nvcaClient bartclient.Interface,
	icmsRequestGetter ICMSRequestGetter,
	m *metrics.Metrics,
) *Cleaner {
	return &Cleaner{
		k8sClient:         k8sClient,
		nvcaClient:        nvcaClient,
		icmsRequestGetter: icmsRequestGetter,
		metrics:           m,
	}
}

// Name returns the name of the namespace cleaner job.
func (c *Cleaner) Name() string { return "NamespaceCleaner" }

// Run executes the namespace cleanup process.
func (c *Cleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Track cleaner run
	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	orphaned, err := c.collectOrphanedNamespaces(ctx)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to collect orphaned namespaces: %w", err)
	}

	if len(orphaned) == 0 {
		log.Info("No orphaned namespaces found, skipping cleanup")
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
		return nil
	}

	log.Infof("Found %d orphaned namespaces, starting cleanup", len(orphaned))

	err = c.executeCleanup(ctx, orphaned)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return err
	}

	c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
	return nil
}

// collectOrphanedNamespaces returns namespaces eligible for cleanup:
// 1. Orphaned: instance namespace where the ICMSRequest no longer exists.
// 2. Stuck terminating: namespace has DeletionTimestamp but StorageRequest finalizers block deletion.
func (c *Cleaner) collectOrphanedNamespaces(ctx context.Context) ([]corev1.Namespace, error) {
	// label selector for instance-type miniservice
	selector := "nvca.nvcf.nvidia.io/workload-instance-type in (miniservice)"

	nsList, err := c.k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	var toCleanup []corev1.Namespace
	for _, ns := range nsList.Items {
		if c.isOrphaned(ctx, &ns) {
			toCleanup = append(toCleanup, ns)
		} else if ns.DeletionTimestamp != nil {
			// Stuck terminating: namespace is being deleted but may be blocked by StorageRequest finalizers.
			// Run cleanup to remove finalizers and unblock namespace deletion.
			toCleanup = append(toCleanup, ns)
		}
	}
	return toCleanup, nil
}

func (c *Cleaner) isOrphaned(ctx context.Context, ns *corev1.Namespace) bool {
	log := core.GetLogger(ctx)

	if ns == nil {
		return false
	}

	// Namespace name equals expected ICMSRequest name
	icmsReqName := ns.Name

	_, err := c.icmsRequestGetter.GetICMSRequest(ctx, icmsReqName)
	if apierrors.IsNotFound(err) {
		log.Infof("ICMSRequest %s/%s absent; namespace %s marked orphaned", types.DefaultICMSRequestNamespace, icmsReqName, ns.Name)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check ICMSRequest %s/%s, skipping namespace %s", types.DefaultICMSRequestNamespace, icmsReqName, ns.Name)
		return false
	}
	return false
}

// executeCleanup performs cleanup in parallel.
func (c *Cleaner) executeCleanup(ctx context.Context, orphaned []corev1.Namespace) error {
	log := core.GetLogger(ctx)

	p := pool.New().WithMaxGoroutines(MaxCleanupWorkers)

	var mtx sync.Mutex
	var errs []error

	for i := range orphaned {
		ns := orphaned[i]
		p.Go(func() {
			if err := c.cleanupNamespace(ctx, &ns); err != nil {
				mtx.Lock()
				errs = append(errs, err)
				mtx.Unlock()
			}
		})
	}

	p.Wait()

	if len(errs) > 0 {
		log.Errorf("Encountered %d errors during namespace cleanup", len(errs))
		for _, err := range errs {
			log.WithError(err).Error("Namespace cleanup error")
		}
	}

	return nil
}

// cleanupNamespace removes StorageRequest and PVC finalizers (if any) then deletes the namespace.
func (c *Cleaner) cleanupNamespace(ctx context.Context, ns *corev1.Namespace) error {
	log := core.GetLogger(ctx).WithField("namespace", ns.Name)

	// List StorageRequests in this namespace (use NvcaV2beta1 to align with ICMSRequests)
	stList, err := c.nvcaClient.NvcaV2beta1().StorageRequests(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// StorageRequest CRD may not exist
			return nil
		}
		log.WithError(err).Error("Failed to list StorageRequests in namespace")
		return err
	}

	// Remove finalizers and delete each StorageRequest
	srAPI := c.nvcaClient.NvcaV2beta1().StorageRequests(ns.Name)
	//nolint:dupl
	for i := range stList.Items {
		st := &stList.Items[i]
		if len(st.Finalizers) > 0 {
			log = log.WithField("storage_request", st.Name)
			// Use retry on conflict for finalizer removal
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Get latest version before update
				latestSt, err := srAPI.Get(ctx, st.Name, metav1.GetOptions{})
				if err != nil {
					if apierrors.IsNotFound(err) {
						log.WithError(err).Debug("StorageRequest already deleted")
						return nil // Resource already deleted
					}
					return err
				}

				if len(latestSt.Finalizers) == 0 {
					log.Debug("Finalizers already removed from StorageRequest")
					return nil // Finalizers already removed
				}

				latestSt.Finalizers = nil
				_, err = srAPI.Update(ctx, latestSt, metav1.UpdateOptions{})
				return err
			}); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).Warnf("Failed to remove finalizers from StorageRequest %s", st.Name)
				c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageRequest, metricsgctypes.StatusFailure)
				continue
			}
		}
		err := srAPI.Delete(ctx, st.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Warnf("Failed to delete StorageRequest %s", st.Name)
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageRequest, metricsgctypes.StatusFailure)
		} else {
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageRequest, metricsgctypes.StatusSuccess)
		}
	}

	// List PersistentVolumeClaims in this namespace
	pvcList, err := c.k8sClient.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to list PersistentVolumeClaims in namespace")
		return err
	}

	// Remove finalizers and delete each PersistentVolumeClaim
	//nolint:dupl
	for i := range pvcList.Items {
		pvc := pvcList.Items[i]
		if len(pvc.Finalizers) > 0 {
			log = log.WithField("pvcName", pvc.Name)
			// Use retry on conflict for finalizer removal
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Get latest version before update
				latestPvc, err := c.k8sClient.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, pvc.Name, metav1.GetOptions{})
				if err != nil {
					if apierrors.IsNotFound(err) {
						log.WithError(err).Debug("PersistentVolumeClaim already deleted")
						return nil // Resource already deleted
					}
					return err
				}

				if len(latestPvc.Finalizers) == 0 {
					log.Debug("Finalizers already removed from PersistentVolumeClaim")
					return nil // Finalizers already removed
				}

				latestPvc.Finalizers = nil
				_, err = c.k8sClient.CoreV1().PersistentVolumeClaims(ns.Name).Update(ctx, latestPvc, metav1.UpdateOptions{})
				return err
			}); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).Warnf("Failed to remove finalizers from PersistentVolumeClaim %s", pvc.Name)
				c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePVC, metricsgctypes.StatusFailure)
				continue
			}
		}
		if err := c.k8sClient.CoreV1().PersistentVolumeClaims(ns.Name).
			Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Warnf("Failed to delete PersistentVolumeClaim %s", pvc.Name)
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePVC, metricsgctypes.StatusFailure)
		} else {
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePVC, metricsgctypes.StatusSuccess)
		}
	}

	// Delete namespace
	if err := c.k8sClient.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Namespace already deleted")
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
			return nil
		}
		if apierrors.IsConflict(err) {
			log.Debugf("Namespace %s deletion already in progress", ns.Name)
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
			return nil
		}
		log.WithError(err).Error("Failed to delete namespace")
		c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusFailure)
		return err
	}

	log.Info("Successfully cleaned up orphaned namespace")
	c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
	return nil
}
