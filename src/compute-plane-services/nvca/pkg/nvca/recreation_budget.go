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
	"k8s.io/client-go/util/retry"
)

const (
	// Max force-purges per function+version in the window.
	recreationBudgetWindow        = 15 * time.Minute
	recreationBudgetMaxPurges     = 3
	recreationBudgetTimestampsKey = "purgeTimestamps"
	recreationBudgetPurposeLabel  = "nvca.nvcf.nvidia.io/purpose"
	recreationBudgetPurposeValue  = "recreation-budget"
)

func recreationBudgetConfigMapName(functionID, functionVersionID string) string {
	return fmt.Sprintf("nvca-recreation-budget-%s-%s", functionID, functionVersionID)
}

// tryReserveRecreationSlot checks the purge budget and reserves a slot in
// one read-modify-write cycle, retried on Conflict/AlreadyExists so
// concurrent callers can't all read the same pre-purge count and bypass the
// cap. State lives in a ConfigMap so it survives NVCA restarts. On
// allowed=true, reservedAt is the exact timestamp written; if the purge
// that follows doesn't actually succeed, pass it to releaseRecreationSlot
// so the slot isn't burned for nothing.
func (c K8sComputeBackend) tryReserveRecreationSlot(ctx context.Context, functionID, functionVersionID string) (allowed bool, reservedAt time.Time, err error) {
	name := recreationBudgetConfigMapName(functionID, functionVersionID)
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
						"nvca.nvcf.nvidia.io/function-id":         functionID,
						"nvca.nvcf.nvidia.io/function-version-id": functionVersionID,
						// Dedicated marker so the GC cleaner (internal/gc/recreationbudget)
						// scopes to exactly these ConfigMaps, not anything else that
						// happens to reuse the function-id label above.
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
func (c K8sComputeBackend) releaseRecreationSlot(ctx context.Context, functionID, functionVersionID string, reservedAt time.Time) error {
	name := recreationBudgetConfigMapName(functionID, functionVersionID)
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
