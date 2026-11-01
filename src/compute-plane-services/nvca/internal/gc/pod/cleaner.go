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

package pod

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
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Cleaner handles the cleanup of orphaned pods in the specified namespace.
type Cleaner struct {
	k8sClient         kubernetes.Interface
	icmsRequestGetter ICMSRequestGetter
	metrics           *metrics.Metrics
	podsNamespace     string
}

// NewCleaner creates a new pod cleaner.
// podsNamespace is the namespace where function pods are deployed.
func NewCleaner(k8sClient kubernetes.Interface, icmsRequestGetter ICMSRequestGetter, m *metrics.Metrics, podsNamespace string) *Cleaner {
	return &Cleaner{
		k8sClient:         k8sClient,
		icmsRequestGetter: icmsRequestGetter,
		metrics:           m,
		podsNamespace:     podsNamespace,
	}
}

// Name returns the name of the pod cleaner job.
func (c *Cleaner) Name() string {
	return "PodCleaner"
}

// Run executes the pod cleanup process.
func (c *Cleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Track cleaner run
	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	// Collect orphaned pods
	orphaned, err := c.collectOrphanedPods(ctx)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to collect orphaned pods: %w", err)
	}

	if len(orphaned) == 0 {
		log.Info("No orphaned pods found, skipping cleanup")
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
		return nil
	}

	log.Infof("Found %d orphaned pods, starting cleanup", len(orphaned))

	// Execute cleanup in parallel
	err = c.executeCleanup(ctx, orphaned)
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return err
	}

	c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
	return nil
}

// collectOrphanedPods identifies pods in the specified namespace that are orphaned.
func (c *Cleaner) collectOrphanedPods(ctx context.Context) ([]corev1.Pod, error) {
	// List all pods in the namespace
	podList, err := c.k8sClient.CoreV1().Pods(c.podsNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", c.podsNamespace, err)
	}

	var orphaned []corev1.Pod

	for _, pod := range podList.Items {
		if c.isOrphaned(ctx, &pod) {
			orphaned = append(orphaned, pod)
		}
	}

	return orphaned, nil
}

// isOrphaned checks if a pod is orphaned by verifying its ICMSRequest owner still exists.
func (c *Cleaner) isOrphaned(ctx context.Context, pod *corev1.Pod) bool {
	log := core.GetLogger(ctx).WithField("pod", pod.Name).WithField("namespace", pod.Namespace)

	// Check if pod has ownerReferences
	if len(pod.OwnerReferences) == 0 {
		log.Debug("Pod has no owner references, skipping")
		return false
	}

	// Look for ICMSRequest owner reference
	var icmsRequestName string
	icmsGV := nvcav2beta1.SchemeGroupVersion.String()
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "ICMSRequest" && owner.APIVersion == icmsGV {
			icmsRequestName = owner.Name
			break
		}
	}

	if icmsRequestName == "" {
		log.Debug("Pod has no ICMSRequest owner reference, skipping")
		return false
	}

	// Check if ICMSRequest exists
	_, err := c.icmsRequestGetter.GetICMSRequest(ctx, icmsRequestName)
	if apierrors.IsNotFound(err) {
		log.Infof("ICMSRequest %s/%s no longer exists, marking pod for cleanup",
			types.DefaultICMSRequestNamespace, icmsRequestName)
		return true
	}
	if err != nil {
		log.WithError(err).Warnf("Failed to check ICMSRequest %s/%s, skipping pod",
			types.DefaultICMSRequestNamespace, icmsRequestName)
		return false
	}

	return false
}

// executeCleanup performs the actual cleanup of orphaned pods in parallel.
func (c *Cleaner) executeCleanup(ctx context.Context, orphaned []corev1.Pod) error {
	log := core.GetLogger(ctx)

	p := pool.New().WithMaxGoroutines(MaxCleanupWorkers)

	var cleanupErrorsMtx sync.Mutex
	var cleanupErrors []error

	for i := range orphaned {
		pod := orphaned[i] // Capture loop variable to avoid data race
		p.Go(func() {
			if err := c.cleanupPod(ctx, &pod); err != nil {
				cleanupErrorsMtx.Lock()
				cleanupErrors = append(cleanupErrors, err)
				cleanupErrorsMtx.Unlock()
			}
		})
	}

	p.Wait()

	if len(cleanupErrors) > 0 {
		log.Errorf("Encountered %d errors during cleanup", len(cleanupErrors))
		for _, err := range cleanupErrors {
			log.WithError(err).Error("Pod cleanup error")
		}
	}

	return nil
}

// cleanupPod removes finalizers and deletes a single pod.
func (c *Cleaner) cleanupPod(ctx context.Context, pod *corev1.Pod) error {
	log := core.GetLogger(ctx).WithField("pod", pod.Name).WithField("namespace", pod.Namespace)

	// Remove finalizers if present
	if len(pod.Finalizers) > 0 {
		log.Info("Removing finalizers from orphaned pod")

		// Use retry on conflict for finalizer removal
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get latest version before update
			latestPod, err := c.k8sClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					log.WithError(err).Debug("Pod already deleted")
					return nil // Resource already deleted
				}
				return err
			}

			if len(latestPod.Finalizers) == 0 {
				log.Debug("Finalizers already removed from pod")
				return nil // Finalizers already removed
			}

			latestPod.Finalizers = nil
			_, err = c.k8sClient.CoreV1().Pods(pod.Namespace).Update(ctx, latestPod, metav1.UpdateOptions{})
			return err
		}); err != nil {
			if apierrors.IsNotFound(err) {
				log.WithError(err).Debug("Pod already deleted")
				return nil
			}
			log.WithError(err).Error("Failed to remove finalizers from pod")
			return err
		}
	}

	// Delete the pod
	if err := c.k8sClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Pod already deleted")
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePod, metricsgctypes.StatusSuccess)
			return nil
		}
		log.WithError(err).Error("Failed to delete pod")
		c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePod, metricsgctypes.StatusFailure)
		return err
	}

	log.Info("Successfully cleaned up orphaned pod")
	c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePod, metricsgctypes.StatusSuccess)
	return nil
}
