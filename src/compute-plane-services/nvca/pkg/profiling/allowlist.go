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

// Package profiling holds the NVIDIA Nsight GPU-profiling opt-in state for NVCF
// functions. NVCA reads an operator-managed ConfigMap that lists which function IDs
// should be profiled and, at pod-creation time, labels only those functions' pods with
// the label the NVIDIA Nsight Operator watches for.
package profiling

import (
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ConfigMapName is the operator-managed ConfigMap NVCA reads from its system namespace
	// to decide which functions to profile. The nvca-operator creates it (from Helm values)
	// and mirrors it into the agent namespace at deploy/upgrade time.
	ConfigMapName = "nvca-gpu-profiling-config"

	// ConfigMapKey is the ConfigMap data key holding the function-ID allowlist. Its
	// value is either "*" (all functions) or a comma / whitespace / newline separated
	// list of function IDs.
	ConfigMapKey = "functionIds"

	// LabelKeyConfigKey / LabelValueConfigKey are optional ConfigMap keys that override the
	// profiling pod label. Empty falls back to nvcatypes.GpuProfilingLabelKey / LabelValue.
	LabelKeyConfigKey   = "labelKey"
	LabelValueConfigKey = "labelValue"

	// WildcardAll enables profiling for every function.
	WildcardAll = "*"
)

// Allowlist holds the set of function IDs for which Nsight GPU profiling is enabled.
// It is safe for concurrent use. A nil *Allowlist profiles nothing, so callers can
// use it without nil checks.
type Allowlist struct {
	mu  sync.RWMutex
	all bool
	ids map[string]struct{}
	// labelKey / labelValue optionally override the profiling pod label; empty uses defaults.
	labelKey   string
	labelValue string
}

// New returns an empty Allowlist that profiles nothing until populated.
func New() *Allowlist {
	return &Allowlist{ids: map[string]struct{}{}}
}

// ShouldProfile reports whether the function with the given ID should be profiled.
// It is safe to call on a nil receiver (returns false) and for an empty function ID.
func (a *Allowlist) ShouldProfile(functionID string) bool {
	if a == nil || functionID == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.all {
		return true
	}
	_, ok := a.ids[functionID]
	return ok
}

// IsWildcard reports whether every function is profiled ("*"). Nil-safe (returns false).
func (a *Allowlist) IsWildcard() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.all
}

// FunctionIDs returns a sorted copy of the allowlisted function IDs (empty for wildcard or
// none), for logging/diagnostics. Nil-safe.
func (a *Allowlist) FunctionIDs() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	ids := make([]string, 0, len(a.ids))
	for id := range a.ids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Set replaces the allowlist from a raw config value. "*" enables all functions;
// otherwise the value is parsed as a comma / whitespace / newline separated list of
// function IDs. An empty value clears the allowlist (profiles nothing).
func (a *Allowlist) Set(raw string) {
	if a == nil {
		return
	}
	all := false
	ids := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if field == WildcardAll {
			all = true
			continue
		}
		ids[field] = struct{}{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.all = all
	a.ids = ids
}

// LabelKey returns the configured profiling label key, or the default when unset. Nil-safe.
func (a *Allowlist) LabelKey() string {
	if a == nil {
		return nvcatypes.GpuProfilingLabelKey
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.labelKey == "" {
		return nvcatypes.GpuProfilingLabelKey
	}
	return a.labelKey
}

// LabelValue returns the configured profiling label value, or the default when unset. Nil-safe.
func (a *Allowlist) LabelValue() string {
	if a == nil {
		return nvcatypes.GpuProfilingLabelValue
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.labelValue == "" {
		return nvcatypes.GpuProfilingLabelValue
	}
	return a.labelValue
}

// setLabel stores the optional label override; empty values fall back to defaults at read time.
func (a *Allowlist) setLabel(key, value string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.labelKey = strings.TrimSpace(key)
	a.labelValue = strings.TrimSpace(value)
}

// LoadFromConfigMap replaces the allowlist and label override from a ConfigMap. A nil
// ConfigMap clears the allowlist and reverts the label to its default.
func (a *Allowlist) LoadFromConfigMap(cm *corev1.ConfigMap) {
	if a == nil {
		return
	}
	if cm == nil {
		a.Set("")
		a.setLabel("", "")
		return
	}
	a.Set(cm.Data[ConfigMapKey])
	a.setLabel(cm.Data[LabelKeyConfigKey], cm.Data[LabelValueConfigKey])
}
