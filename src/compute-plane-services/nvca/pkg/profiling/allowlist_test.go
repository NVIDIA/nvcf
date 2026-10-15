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

package profiling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAllowlist_NilAndEmpty(t *testing.T) {
	var nilList *Allowlist
	assert.False(t, nilList.ShouldProfile("fn-1"), "nil allowlist should profile nothing")

	empty := New()
	assert.False(t, empty.ShouldProfile("fn-1"), "empty allowlist should profile nothing")
	assert.False(t, empty.ShouldProfile(""), "empty function ID is never profiled")
}

func TestAllowlist_Set(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		profiled   []string
		unprofiled []string
	}{
		{
			name:       "wildcard profiles all",
			raw:        "*",
			profiled:   []string{"fn-1", "fn-2", "anything"},
			unprofiled: []string{""}, // empty ID still never matches
		},
		{
			name:       "single id",
			raw:        "fn-abc",
			profiled:   []string{"fn-abc"},
			unprofiled: []string{"fn-xyz", "fn-ab"},
		},
		{
			name:       "comma separated list",
			raw:        "fn-a,fn-b,fn-c",
			profiled:   []string{"fn-a", "fn-b", "fn-c"},
			unprofiled: []string{"fn-d"},
		},
		{
			name:       "whitespace and newline separated with padding",
			raw:        "  fn-a , fn-b \n fn-c\t",
			profiled:   []string{"fn-a", "fn-b", "fn-c"},
			unprofiled: []string{"fn-d", "fn-"},
		},
		{
			name:       "empty clears",
			raw:        "",
			unprofiled: []string{"fn-a"},
		},
		{
			name:       "wildcard mixed with ids still profiles all",
			raw:        "fn-a,*",
			profiled:   []string{"fn-a", "fn-z"},
			unprofiled: []string{""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			a.Set(tc.raw)
			for _, id := range tc.profiled {
				assert.Truef(t, a.ShouldProfile(id), "expected %q to be profiled", id)
			}
			for _, id := range tc.unprofiled {
				assert.Falsef(t, a.ShouldProfile(id), "expected %q to NOT be profiled", id)
			}
		})
	}
}

func TestAllowlist_SetReplaces(t *testing.T) {
	a := New()
	a.Set("fn-a,fn-b")
	assert.True(t, a.ShouldProfile("fn-a"))

	// A subsequent Set fully replaces the previous contents.
	a.Set("fn-c")
	assert.False(t, a.ShouldProfile("fn-a"), "old entries should be cleared on reload")
	assert.True(t, a.ShouldProfile("fn-c"))

	// Reload to wildcard, then back to a narrow list.
	a.Set("*")
	assert.True(t, a.ShouldProfile("fn-a"))
	a.Set("fn-b")
	assert.False(t, a.ShouldProfile("fn-a"), "wildcard should be cleared on reload")
	assert.True(t, a.ShouldProfile("fn-b"))
}

func TestAllowlist_LoadFromConfigMap(t *testing.T) {
	a := New()

	// nil ConfigMap clears / profiles nothing.
	a.Set("fn-a")
	a.LoadFromConfigMap(nil)
	assert.False(t, a.ShouldProfile("fn-a"))

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName},
		Data:       map[string]string{ConfigMapKey: "fn-a,fn-b"},
	}
	a.LoadFromConfigMap(cm)
	assert.True(t, a.ShouldProfile("fn-a"))
	assert.True(t, a.ShouldProfile("fn-b"))
	assert.False(t, a.ShouldProfile("fn-c"))

	// Missing key behaves like empty.
	a.LoadFromConfigMap(&corev1.ConfigMap{Data: map[string]string{"other": "x"}})
	assert.False(t, a.ShouldProfile("fn-a"))
}

func TestAllowlist_LabelDefaults(t *testing.T) {
	// nil and empty allowlists report the built-in defaults.
	var nilList *Allowlist
	assert.Equal(t, "nvidia-nsight-profile", nilList.LabelKey())
	assert.Equal(t, "enabled", nilList.LabelValue())

	a := New()
	assert.Equal(t, "nvidia-nsight-profile", a.LabelKey())
	assert.Equal(t, "enabled", a.LabelValue())

	// A ConfigMap without label keys keeps the defaults.
	a.LoadFromConfigMap(&corev1.ConfigMap{Data: map[string]string{ConfigMapKey: "fn-a"}})
	assert.Equal(t, "nvidia-nsight-profile", a.LabelKey())
	assert.Equal(t, "enabled", a.LabelValue())
}

func TestAllowlist_LabelOverride(t *testing.T) {
	a := New()
	a.LoadFromConfigMap(&corev1.ConfigMap{
		Data: map[string]string{
			ConfigMapKey:        "fn-a",
			LabelKeyConfigKey:   " custom.io/profile ",
			LabelValueConfigKey: " on ",
		},
	})
	assert.True(t, a.ShouldProfile("fn-a"))
	assert.Equal(t, "custom.io/profile", a.LabelKey(), "override should be trimmed and applied")
	assert.Equal(t, "on", a.LabelValue(), "override should be trimmed and applied")

	// Reloading without the override reverts to the defaults.
	a.LoadFromConfigMap(&corev1.ConfigMap{Data: map[string]string{ConfigMapKey: "fn-a"}})
	assert.Equal(t, "nvidia-nsight-profile", a.LabelKey())
	assert.Equal(t, "enabled", a.LabelValue())

	// A nil ConfigMap also reverts to the defaults.
	a.LoadFromConfigMap(&corev1.ConfigMap{Data: map[string]string{LabelKeyConfigKey: "x", LabelValueConfigKey: "y"}})
	a.LoadFromConfigMap(nil)
	assert.Equal(t, "nvidia-nsight-profile", a.LabelKey())
	assert.Equal(t, "enabled", a.LabelValue())
}
