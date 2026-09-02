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

package nvca

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	metricsgctypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/gctypes"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
)

const (
	// Max force-purges per function+version (or per task) in the window.
	recreationBudgetWindow        = 15 * time.Minute
	recreationBudgetMaxPurges     = 3
	recreationBudgetTimestampsKey = "purgeTimestamps"
	recreationBudgetPurposeLabel  = "nvca.nvcf.nvidia.io/purpose"
	recreationBudgetPurposeValue  = "recreation-budget"

	recreationBudgetKindFunction = "function"
	recreationBudgetKindTask     = "task"
)

// recreationBudgetIdentity derives the workload identity a purge's budget
// should be scoped to. ICMSRequests populate exactly one of FunctionDetails
// or TaskDetails depending on the request type (see icmsrequest_types.go);
// using FunctionDetails unconditionally for both would give every task an
// identical, empty identity, letting unrelated task purges share and
// exhaust the same budget. Returns id == "" when neither is populated, so
// the caller can fail closed instead of reserving a bogus shared budget.
func recreationBudgetIdentity(req *nvcav2beta1.ICMSRequest) (kind, id, versionID string) {
	if fid := req.Spec.FunctionDetails.FunctionID; fid != "" {
		return recreationBudgetKindFunction, fid, req.Spec.FunctionDetails.FunctionVersionID
	}
	if tid := req.Spec.TaskDetails.TaskID; tid != "" {
		return recreationBudgetKindTask, tid, ""
	}
	return "", "", ""
}

// recreationBudgetConfigMapName derives a name scoped to the specific
// workload the purge is for. kind distinguishes function purges from task
// purges (which populate ICMSRequest.Spec.TaskDetails, not FunctionDetails,
// and have no version) so the two never collide on the same budget.
func recreationBudgetConfigMapName(kind, id, versionID string) string {
	if versionID == "" {
		return fmt.Sprintf("nvca-recreation-budget-%s-%s", kind, id)
	}
	return fmt.Sprintf("nvca-recreation-budget-%s-%s-%s", kind, id, versionID)
}

// tryReserveRecreationSlot checks the purge budget and reserves a slot in
// one read-modify-write cycle, retried on Conflict/AlreadyExists so
// concurrent callers can't all read the same pre-purge count and bypass the
// cap. State lives in a ConfigMap so it survives NVCA restarts. On
// allowed=true, reservedAt is the exact timestamp written; if the purge
// that follows doesn't actually succeed, pass it to releaseRecreationSlot
// so the slot isn't burned for nothing. kind/id/versionID must uniquely
// identify the workload (see recreationBudgetConfigMapName); callers must
// not call this with an empty id.
func (c K8sComputeBackend) tryReserveRecreationSlot(ctx context.Context, kind, id, versionID string) (allowed bool, reservedAt time.Time, err error) {
	name := recreationBudgetConfigMapName(kind, id, versionID)
	ns := c.bk8s.systemNamespace
	cmClient := c.clients.K8s.CoreV1().ConfigMaps(ns)

	retriable := func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}

	err = retry.OnError(retry.DefaultRetry, retriable, func() error {
		cm, getErr := cmClient.Get(ctx, name, metav1.GetOptions{})
		notFound := false
		if getErr != nil {
			if !apierrors.IsNotFound(getErr) {
				return getErr
			}
			notFound = true
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
					Labels: map[string]string{
						"nvca.nvcf.nvidia.io/workload-kind": kind,
						"nvca.nvcf.nvidia.io/workload-id":   id,
						// Dedicated marker so recreationBudgetCleaner below scopes to
						// exactly these ConfigMaps, not anything else that happens to
						// reuse the workload-kind/workload-id labels above.
						recreationBudgetPurposeLabel: recreationBudgetPurposeValue,
					},
				},
			}
		}

		recent := recentPurgeTimestamps(cm.Data[recreationBudgetTimestampsKey])
		if len(recent) >= recreationBudgetMaxPurges {
			allowed = false
			return nil
		}
		allowed = true
		// Truncate to what formatPurgeTimestamps/RFC3339 actually persist
		// (second precision) so a later releaseRecreationSlot's exact-value
		// match against the round-tripped, string-stored timestamp succeeds.
		reservedAt = time.Now().Truncate(time.Second)

		recent = append(recent, reservedAt)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[recreationBudgetTimestampsKey] = formatPurgeTimestamps(recent)

		var updateErr error
		if notFound {
			_, updateErr = cmClient.Create(ctx, cm, metav1.CreateOptions{})
		} else {
			_, updateErr = cmClient.Update(ctx, cm, metav1.UpdateOptions{})
		}
		return updateErr
	})
	if err != nil {
		return false, time.Time{}, err
	}
	return allowed, reservedAt, nil
}

// releaseRecreationSlot removes a single reservedAt timestamp (as returned
// by tryReserveRecreationSlot) from the budget ConfigMap. Called when the
// purge that consumed the slot didn't actually happen (e.g. HelmV2.Delete
// failed), so a real transient delete failure doesn't burn the budget
// without ever making progress. Best-effort: errors are returned for the
// caller to log, not retried beyond the usual Conflict handling, since a
// failure here just means the slot stays reserved -- overly conservative,
// not unsafe.
func (c K8sComputeBackend) releaseRecreationSlot(ctx context.Context, kind, id, versionID string, reservedAt time.Time) error {
	name := recreationBudgetConfigMapName(kind, id, versionID)
	ns := c.bk8s.systemNamespace
	cmClient := c.clients.K8s.CoreV1().ConfigMaps(ns)

	retriable := func(err error) bool {
		return apierrors.IsConflict(err)
	}

	return retry.OnError(retry.DefaultRetry, retriable, func() error {
		cm, err := cmClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		recent := recentPurgeTimestamps(cm.Data[recreationBudgetTimestampsKey])
		kept := recent[:0]
		removed := false
		for _, t := range recent {
			if !removed && t.Equal(reservedAt) {
				removed = true
				continue
			}
			kept = append(kept, t)
		}
		if !removed {
			return nil
		}

		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[recreationBudgetTimestampsKey] = formatPurgeTimestamps(kept)
		_, err = cmClient.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}

func recentPurgeTimestamps(raw string) []time.Time {
	if raw == "" {
		return nil
	}
	cutoff := time.Now().Add(-recreationBudgetWindow)
	var recent []time.Time
	for _, s := range strings.Split(raw, ",") {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	return recent
}

func formatPurgeTimestamps(ts []time.Time) string {
	strs := make([]string, len(ts))
	for i, t := range ts {
		strs[i] = t.Format(time.RFC3339)
	}
	return strings.Join(strs, ",")
}

// recreationBudgetCleaner deletes recreation-budget ConfigMaps (created
// above by tryReserveRecreationSlot) whose recorded purge timestamps have
// all aged out of the budget window. Without this, a ConfigMap created for
// a (function, version) pair that is later deleted or never purged again is
// never revisited and accumulates in the system namespace indefinitely.
type recreationBudgetCleaner struct {
	k8sClient       kubernetes.Interface
	systemNamespace string
	metrics         *metrics.Metrics
}

// newRecreationBudgetCleaner creates a new recreation-budget ConfigMap cleaner.
func newRecreationBudgetCleaner(k8sClient kubernetes.Interface, systemNamespace string, m *metrics.Metrics) *recreationBudgetCleaner {
	return &recreationBudgetCleaner{k8sClient: k8sClient, systemNamespace: systemNamespace, metrics: m}
}

// Name returns the name of the recreation-budget cleaner job.
func (c *recreationBudgetCleaner) Name() string {
	return "RecreationBudgetCleaner"
}

// Run deletes recreation-budget ConfigMaps whose recorded purge timestamps
// have all aged out of the budget window.
func (c *recreationBudgetCleaner) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)

	defer func() {
		if r := recover(); r != nil {
			c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
			panic(r)
		}
	}()

	cms, err := c.k8sClient.CoreV1().ConfigMaps(c.systemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", recreationBudgetPurposeLabel, recreationBudgetPurposeValue),
	})
	if err != nil {
		c.metrics.RecordGCCleanerRun(c.Name(), metricsgctypes.StatusFailure)
		return fmt.Errorf("failed to list recreation-budget ConfigMaps: %w", err)
	}

	var expired int
	for i := range cms.Items {
		cm := &cms.Items[i]
		if !isRecreationBudgetConfigMapExpired(cm) {
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

// isRecreationBudgetConfigMapExpired reports whether every purge timestamp
// recorded on cm has aged out of the budget window, meaning the ConfigMap
// carries no useful state.
func isRecreationBudgetConfigMapExpired(cm *corev1.ConfigMap) bool {
	raw := cm.Data[recreationBudgetTimestampsKey]
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
