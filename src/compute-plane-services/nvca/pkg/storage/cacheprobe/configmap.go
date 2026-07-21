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

package cacheprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ProbeConfigMapName is the ConfigMap that persists probe results in the
// control namespace.
const ProbeConfigMapName = "model-cache-probe-results"

// StateStore persists probe results in a ConfigMap so the strategy survives
// restarts and is reused within its TTL instead of re-probing every reconcile.
type StateStore struct {
	client    client.Client
	namespace string
}

// NewStateStore creates a StateStore backed by a ConfigMap in the given namespace.
func NewStateStore(c client.Client, namespace string) *StateStore {
	return &StateStore{client: c, namespace: namespace}
}

// Save writes probe results to the ConfigMap, creating it if necessary.
func (s *StateStore) Save(ctx context.Context, results map[string]Result) error {
	data := make(map[string]string, len(results))
	for key, result := range results {
		b, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal probe result for %s: %w", key, err)
		}
		data[key] = string(b)
	}

	cm := &corev1.ConfigMap{}
	err := s.client.Get(ctx, client.ObjectKey{Name: ProbeConfigMapName, Namespace: s.namespace}, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ProbeConfigMapName,
				Namespace: s.namespace,
				Labels:    map[string]string{managedByLabel: managedByValue, probeLabel: "true"},
			},
			Data: data,
		}
		if createErr := s.client.Create(ctx, cm); createErr != nil {
			return fmt.Errorf("create probe ConfigMap: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get probe ConfigMap: %w", err)
	}

	cm.Data = data
	if err := s.client.Update(ctx, cm); err != nil {
		return fmt.Errorf("update probe ConfigMap: %w", err)
	}
	return nil
}

// Load reads persisted probe results from the ConfigMap. Missing ConfigMap
// yields a nil map (no results yet).
func (s *StateStore) Load(ctx context.Context) (map[string]Result, error) {
	cm := &corev1.ConfigMap{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: ProbeConfigMapName, Namespace: s.namespace}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get probe ConfigMap: %w", err)
	}

	results := make(map[string]Result, len(cm.Data))
	for key, val := range cm.Data {
		var r Result
		if err := json.Unmarshal([]byte(val), &r); err != nil {
			return nil, fmt.Errorf("unmarshal probe result for %s: %w", key, err)
		}
		results[key] = r
	}
	return results, nil
}

// GetStrategy returns the best persisted, unexpired strategy for the storage
// class, or StrategyFallback when results are missing/expired (signalling the
// caller to re-probe).
func (s *StateStore) GetStrategy(ctx context.Context, storageClassName string) (AccessModeStrategy, error) {
	results, err := s.Load(ctx)
	if err != nil {
		return StrategyFallback, err
	}
	if results == nil {
		return StrategyFallback, nil
	}

	if r, ok := results[ResultKey(storageClassName, StrategyROX)]; ok && r.State == StateSupported && !isExpired(r) {
		return StrategyROX, nil
	}
	if r, ok := results[ResultKey(storageClassName, StrategyRWX)]; ok && r.State == StateSupported && !isExpired(r) {
		return StrategyRWX, nil
	}
	return StrategyFallback, nil
}

// HasFreshResult reports whether an unexpired probe result (of any state,
// supported or unsupported) exists for the storage class. Callers use this to
// honour the TTL of a negative (Unsupported) result: GetStrategy returns
// StrategyFallback both when results are missing/expired and when a fresh probe
// determined the class is unsupported, so a Fallback result alone does not mean
// "re-probe". A fresh negative result means the class was recently probed and
// found unusable, and should not be re-probed until its TTL elapses.
func (s *StateStore) HasFreshResult(ctx context.Context, storageClassName string) (bool, error) {
	results, err := s.Load(ctx)
	if err != nil {
		return false, err
	}
	for _, strategy := range []AccessModeStrategy{StrategyROX, StrategyRWX} {
		if r, ok := results[ResultKey(storageClassName, strategy)]; ok && !isExpired(r) {
			return true, nil
		}
	}
	return false, nil
}

func isExpired(r Result) bool {
	if r.TTL <= 0 {
		return false
	}
	return time.Since(r.CheckedAt) > time.Duration(r.TTL)*time.Second
}
