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

package persistentvolume

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
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Cleaner handles the cleanup of orphaned persistent volumes
type Cleaner struct {
	k8sClient         kubernetes.Interface
	icmsRequestGetter ICMSRequestGetter
	metrics           *metrics.Metrics
}

// NewCleaner creates a new persistent volume cleaner
func NewCleaner(k8sClient kubernetes.Interface, icmsRequestGetter ICMSRequestGetter, m *metrics.Metrics) *Cleaner {
	return &Cleaner{
		k8sClient:         k8sClient,
		icmsRequestGetter: icmsRequestGetter,
		metrics:           m,
	}
}

// Name returns the name of the persistent volume cleaner job
func (c *Cleaner) Name() string {
	return "PersistentVolumeCleaner"
}

// Run executes the persistent volume cleanup process
func (c *Cleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Track cleaner run
	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	// Collect orphaned persistent volumes
	orphaned, err := c.collectOrphanedPersistentVolumes(ctx)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to collect orphaned persistent volumes: %w", err)
	}

	if len(orphaned) == 0 {
		log.Info("No orphaned persistent volumes found, skipping cleanup")
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
		return nil
	}

	log.Infof("Found %d orphaned persistent volumes, starting cleanup", len(orphaned))

	// Execute cleanup in parallel
	err = c.executeCleanup(ctx, orphaned)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return err
	}

	c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
	return nil
}

// collectOrphanedPersistentVolumes identifies persistent volumes that are orphaned
func (c *Cleaner) collectOrphanedPersistentVolumes(ctx context.Context) ([]corev1.PersistentVolume, error) {
	// List all persistent volumes with the storage request namespace label
	persistentVolumes, err := c.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{
		LabelSelector: storage.StorageRequestNamespaceKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list persistent volumes: %w", err)
	}

	var orphaned []corev1.PersistentVolume

	for _, pv := range persistentVolumes.Items {
		if c.isOrphaned(ctx, &pv) {
			orphaned = append(orphaned, pv)
		}
	}

	return orphaned, nil
}

// isOrphaned checks if a persistent volume is orphaned based on the cleanup criteria
func (c *Cleaner) isOrphaned(ctx context.Context, pv *corev1.PersistentVolume) bool {
	log := core.GetLogger(ctx)

	// Check if persistent volume has required labels
	if pv.Labels == nil {
		return false
	}

	storageRequestNamespace, hasNamespaceLabel := pv.Labels[storage.StorageRequestNamespaceKey]
	if !hasNamespaceLabel {
		return false
	}

	// Check if the namespace still exists
	_, err := c.k8sClient.CoreV1().Namespaces().Get(ctx, storageRequestNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		log.Infof("Namespace %s no longer exists, marking persistent volume %s for cleanup",
			storageRequestNamespace, pv.Name)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check namespace %s, skipping persistent volume %s",
			storageRequestNamespace, pv.Name)
		return false
	}

	// Check if ICMSRequest exists
	// ICMSRequest name is the value of storage-request-namespace label and exists in nvcf-backend namespace
	icmsRequestName := storageRequestNamespace
	_, err = c.icmsRequestGetter.GetICMSRequest(ctx, icmsRequestName)
	if apierrors.IsNotFound(err) {
		log.Infof("ICMSRequest %s/%s no longer exists, marking persistent volume %s for cleanup",
			types.DefaultICMSRequestNamespace, icmsRequestName, pv.Name)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check ICMSRequest %s/%s, skipping persistent volume %s",
			types.DefaultICMSRequestNamespace, icmsRequestName, pv.Name)
		return false
	}

	return false
}

// executeCleanup performs the actual cleanup of orphaned persistent volumes in parallel
func (c *Cleaner) executeCleanup(ctx context.Context, orphaned []corev1.PersistentVolume) error {
	log := core.GetLogger(ctx)

	p := pool.New().WithMaxGoroutines(MaxCleanupWorkers)

	var cleanupErrorsMtx sync.Mutex
	var cleanupErrors []error

	for i := range orphaned {
		pv := orphaned[i] // Capture loop variable to avoid data race
		p.Go(func() {
			if err := c.cleanupPersistentVolume(ctx, &pv); err != nil {
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
			log.WithError(err).Error("Persistent volume cleanup error")
		}
	}

	return nil
}

// cleanupPersistentVolume removes finalizers and deletes a single persistent volume
func (c *Cleaner) cleanupPersistentVolume(ctx context.Context, pv *corev1.PersistentVolume) error {
	log := core.GetLogger(ctx).WithField("persistent_volume", pv.Name)

	// Remove finalizers if present
	if len(pv.Finalizers) > 0 {
		log.Info("Removing finalizers from orphaned persistent volume")

		// Use retry on conflict for finalizer removal
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get latest version before update
			latestPv, err := c.k8sClient.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					log.WithError(err).Debug("PersistentVolume already deleted")
					return nil // Resource already deleted
				}
				return err
			}

			if len(latestPv.Finalizers) == 0 {
				log.Debug("Finalizers already removed from PersistentVolume")
				return nil // Finalizers already removed
			}

			latestPv.Finalizers = nil
			_, err = c.k8sClient.CoreV1().PersistentVolumes().Update(ctx, latestPv, metav1.UpdateOptions{})
			return err
		}); err != nil {
			if apierrors.IsNotFound(err) {
				log.WithError(err).Debug("PersistentVolume already deleted")
				return nil
			}
			log.WithError(err).Error("Failed to remove finalizers from persistent volume")
			return err
		}
	}

	// Delete the persistent volume
	if err := c.k8sClient.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug("Persistent volume already deleted")
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePersistentVolume, metricsgctypes.StatusSuccess)
			return nil
		}
		log.WithError(err).Error("Failed to delete persistent volume")
		c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePersistentVolume, metricsgctypes.StatusFailure)
		return err
	}

	log.Info("Successfully cleaned up orphaned persistent volume")
	c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePersistentVolume, metricsgctypes.StatusSuccess)
	return nil
}
