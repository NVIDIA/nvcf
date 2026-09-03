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

// Package recreationbudget deletes MiniService recreation-budget ConfigMaps
// (see pkg/nvca/recreation_budget.go) once their tracked purge timestamps
// have all aged out of the budget window. Without this, a ConfigMap created
// for a (function, version) pair that is later deleted or never purged
// again is never revisited and accumulates in the system namespace
// indefinitely.
package recreationbudget

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	metricsgctypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/gctypes"
)

// Cleaner deletes expired recreation-budget ConfigMaps in a single namespace.
type Cleaner struct {
	k8sClient       kubernetes.Interface
	systemNamespace string
	metrics         *metrics.Metrics
}

// NewCleaner creates a new recreation-budget ConfigMap cleaner.
func NewCleaner(k8sClient kubernetes.Interface, systemNamespace string, m *metrics.Metrics) *Cleaner {
	return &Cleaner{k8sClient: k8sClient, systemNamespace: systemNamespace, metrics: m}
}

// Name returns the name of the recreation-budget cleaner job.
func (c *Cleaner) Name() string {
	return "RecreationBudgetCleaner"
}

// Run deletes recreation-budget ConfigMaps whose recorded purge timestamps
// have all aged out of the budget window.
func (c *Cleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	cms, err := c.k8sClient.CoreV1().ConfigMaps(c.systemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", purposeLabel, purposeValue),
	})
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to list recreation-budget ConfigMaps: %w", err)
	}

	var expired int
	for i := range cms.Items {
		cm := &cms.Items[i]
		if !isExpired(cm) {
			continue
		}
		// Preconditioned on ResourceVersion: if a reservation attempt wrote
		// a fresh timestamp between our List and this Delete, the object no
		// longer matches what we judged expired, and the delete must be
		// rejected rather than silently discarding that new reservation.
		deleteOpts := metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{ResourceVersion: &cm.ResourceVersion},
		}
		if err := c.k8sClient.CoreV1().ConfigMaps(c.systemNamespace).Delete(ctx, cm.Name, deleteOpts); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			if apierrors.IsConflict(err) {
				log.Debugf("Recreation-budget ConfigMap %s changed since it was listed, skipping delete this cycle", cm.Name)
				continue
			}
			log.WithError(err).Warnf("Failed to delete expired recreation-budget ConfigMap %s", cm.Name)
			c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeRecreationBudget, metricsgctypes.StatusFailure)
			continue
		}
		expired++
		c.metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeRecreationBudget, metricsgctypes.StatusSuccess)
	}

	if expired > 0 {
		log.Infof("Deleted %d expired recreation-budget ConfigMap(s)", expired)
	}

	c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusSuccess)
	return nil
}

// isExpired reports whether every purge timestamp recorded on cm has aged
// out of the budget window, meaning the ConfigMap carries no useful state.
func isExpired(cm *corev1.ConfigMap) bool {
	raw := cm.Data[timestampsKey]
	if raw == "" {
		return true
	}
	cutoff := time.Now().Add(-recreationBudgetWindow)
	for _, s := range strings.Split(raw, ",") {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if t.After(cutoff) {
			return false
		}
	}
	return true
}
