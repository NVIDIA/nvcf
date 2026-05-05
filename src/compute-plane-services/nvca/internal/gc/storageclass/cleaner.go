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

package storageclass

import (
	"context"
	"fmt"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sourcegraph/conc/pool"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	metricsgctypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/gctypes"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Cleaner handles the cleanup of orphaned storage classes
type Cleaner struct {
	k8sClient         kubernetes.Interface
	icmsRequestGetter ICMSRequestGetter
	metrics           *metrics.Metrics
}

// NewCleaner creates a new storage class cleaner
func NewCleaner(k8sClient kubernetes.Interface, icmsRequestGetter ICMSRequestGetter, m *metrics.Metrics) *Cleaner {
	return &Cleaner{
		k8sClient:         k8sClient,
		icmsRequestGetter: icmsRequestGetter,
		metrics:           m,
	}
}

// Name returns the name of the storage class cleaner job
func (c *Cleaner) Name() string {
	return "StorageClassCleaner"
}

// Run executes the storage class cleanup process
func (c *Cleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Track cleaner run
	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	// Collect orphaned storage classes
	orphaned, err := c.collectOrphanedStorageClasses(ctx)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to collect orphaned storage classes: %w", err)
	}

	if len(orphaned) == 0 {
		log.Info("No orphaned storage classes found, skipping cleanup")
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
		return nil
	}

	log.Infof("Found %d orphaned storage classes, starting cleanup", len(orphaned))

	// Execute cleanup in parallel
	err = c.executeCleanup(ctx, orphaned)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return err
	}

	c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
	return nil
}

// collectOrphanedStorageClasses identifies storage classes that are orphaned
func (c *Cleaner) collectOrphanedStorageClasses(ctx context.Context) ([]storagev1.StorageClass, error) {
	// List all storage classes
	storageClasses, err := c.k8sClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", storage.StorageRequestOwnerKey, nvcav1new.SharedStorageRequest.Name()),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list storage classes: %w", err)
	}

	var orphaned []storagev1.StorageClass

	for _, sc := range storageClasses.Items {
		if c.isOrphaned(ctx, &sc) {
			orphaned = append(orphaned, sc)
		}
	}

	return orphaned, nil
}

// isOrphaned checks if a storage class is orphaned based on the cleanup criteria
func (c *Cleaner) isOrphaned(ctx context.Context, sc *storagev1.StorageClass) bool {
	log := core.GetLogger(ctx)

	// Check if storage class has required labels
	if sc.Labels == nil {
		return false
	}

	storageRequestNamespace, hasNamespaceLabel := sc.Labels[storage.StorageRequestNamespaceKey]
	if !hasNamespaceLabel {
		return false
	}

	_, err := c.k8sClient.CoreV1().Namespaces().Get(ctx, storageRequestNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		log.Infof("Namespace %s no longer exists, marking storage class %s for cleanup",
			storageRequestNamespace, sc.Name)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check namespace %s, skipping storage class %s",
			storageRequestNamespace, sc.Name)
		return false
	}

	// Check if ICMSRequest exists
	// ICMSRequest name is the value of storage-request-namespace label and exists in nvcf-backend namespace
	icmsRequestName := storageRequestNamespace
	_, err = c.icmsRequestGetter.GetICMSRequest(ctx, icmsRequestName)
	if apierrors.IsNotFound(err) {
		log.Infof("ICMSRequest %s/%s no longer exists, marking storage class %s for cleanup",
			types.DefaultICMSRequestNamespace, icmsRequestName, sc.Name)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check ICMSRequest %s/%s, skipping storage class %s",
			types.DefaultICMSRequestNamespace, icmsRequestName, sc.Name)
		return false
	}

	return false
}

// executeCleanup performs the actual cleanup of orphaned storage classes in parallel
func (c *Cleaner) executeCleanup(ctx context.Context, orphaned []storagev1.StorageClass) error {
	log := core.GetLogger(ctx)

	p := pool.New().WithMaxGoroutines(MaxCleanupWorkers)

	var cleanupErrorsMtx sync.Mutex
	var cleanupErrors []error

	for i := range orphaned {
		sc := orphaned[i] // Capture loop variable to avoid data race
		p.Go(func() {
			if err := c.cleanupStorageClass(ctx, &sc); err != nil {
				cleanupErrorsMtx.Lock()
				cleanupErrors = append(cleanupErrors, err)
				cleanupErrorsMtx.Unlock()
			}
		})
	}

	p.Wait()

	if len(cleanupErrors) > 0 {
		log.Errorf("Encountered %d errors during cleanup", len(cleanupErrors))
		// Log individual errors but don't fail startup
		for _, err := range cleanupErrors {
			log.WithError(err).Error("Storage class cleanup error")
		}
	}

	return nil
}

// cleanupStorageClass removes finalizers and deletes a single storage class
func (c *Cleaner) cleanupStorageClass(ctx context.Context, sc *storagev1.StorageClass) error {
	log := core.GetLogger(ctx).WithField("storage_class", sc.Name)

	// Remove finalizers if present
	if len(sc.Finalizers) > 0 {
		log.Info("Removing finalizers from orphaned storage class")

		// Use retry on conflict for finalizer removal
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get latest version before update
			latestSc, err := c.k8sClient.StorageV1().StorageClasses().Get(ctx, sc.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					log.WithError(err).Debugf("StorageClass %s already deleted", sc.Name)
					return nil // Resource already deleted
				}
				return err
			}

			if len(latestSc.Finalizers) == 0 {
				log.Debugf("Finalizers already removed from StorageClass %s", sc.Name)
				return nil // Finalizers already removed
			}

			latestSc.Finalizers = nil
			_, err = c.k8sClient.StorageV1().StorageClasses().Update(ctx, latestSc, metav1.UpdateOptions{})
			return err
		}); err != nil {
			if apierrors.IsNotFound(err) {
				log.WithError(err).Debug("StorageClass already deleted")
				return nil
			}
			log.WithError(err).Error("Failed to remove finalizers from storage class")
			return err
		}
	}

	// Delete the storage class
	if err := c.k8sClient.StorageV1().StorageClasses().Delete(ctx, sc.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Storage class already deleted")
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageClass, metricsgctypes.StatusSuccess)
			return nil
		}
		log.WithError(err).Error("Failed to delete storage class")
		c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageClass, metricsgctypes.StatusFailure)
		return err
	}

	log.Info("Successfully cleaned up orphaned storage class")
	c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeStorageClass, metricsgctypes.StatusSuccess)
	return nil
}
