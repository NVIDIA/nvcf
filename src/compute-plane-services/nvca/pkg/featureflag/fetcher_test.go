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

package featureflag

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultFetcher(t *testing.T) {
	tests := []struct {
		name        string
		featureFlag *FeatureFlag
		attribute   *Attribute
		wantFF      bool
		wantAttr    bool
	}{
		{
			name:        "nil feature flag",
			featureFlag: nil,
			wantFF:      false,
		},
		{
			name:        "disabled feature flag",
			featureFlag: &FeatureFlag{enabled: newBool(false)},
			wantFF:      false,
		},
		{
			name:        "enabled feature flag",
			featureFlag: &FeatureFlag{enabled: newBool(true)},
			wantFF:      true,
		},
		{
			name:      "nil attribute",
			attribute: nil,
			wantAttr:  false,
		},
		{
			name:      "disabled attribute",
			attribute: &Attribute{enabled: newBool(false)},
			wantAttr:  false,
		},
		{
			name:      "enabled attribute",
			attribute: &Attribute{enabled: newBool(true)},
			wantAttr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &defaultFetcher{}
			gotFF := f.IsFeatureFlagEnabled(tt.featureFlag)
			gotAttr := f.IsAttributeEnabled(tt.attribute)
			assert.Equal(t, tt.wantFF, gotFF)
			assert.Equal(t, tt.wantAttr, gotAttr)
		})
	}
}

func TestNewListFetcher(t *testing.T) {
	tests := []struct {
		name       string
		flags      []string
		attributes []string
		wantFFs    map[*FeatureFlag]bool
		wantAttrs  map[*Attribute]bool
	}{
		{
			name: "unlisted entries keep their defaults",
			wantFFs: map[*FeatureFlag]bool{
				DynamicGPUDiscovery:   true,
				InfraResourceOverhead: false,
			},
			wantAttrs: map[*Attribute]bool{AttrKataRuntimeIsolation: false},
		},
		{
			name:  "plus and minus prefixes",
			flags: []string{"+BYOObservability", "-DynamicGPUDiscovery", "BYOOFluentBit"},
			wantFFs: map[*FeatureFlag]bool{
				BYOObservability:    true,
				BYOOFluentBit:       true,
				DynamicGPUDiscovery: false,
			},
		},
		{
			name:    "resource limit enforcement implies infrastructure overhead",
			flags:   []string{EnforceContainerFunctionResourceLimits.Key},
			wantFFs: map[*FeatureFlag]bool{InfraResourceOverhead: true},
		},
		{
			name:    "deprecated flags keep their defaults",
			flags:   []string{"-" + UseFunctionTranslator.Key},
			wantFFs: map[*FeatureFlag]bool{UseFunctionTranslator: true},
		},
		{
			name:    "unknown flags are ignored",
			flags:   []string{"NoSuchFlag"},
			wantFFs: map[*FeatureFlag]bool{InfraResourceOverhead: false},
		},
		{
			name:       "attributes",
			attributes: []string{"KataRuntimeIsolation", "PassthroughGPUEnabled=false"},
			wantAttrs: map[*Attribute]bool{
				AttrKataRuntimeIsolation:  true,
				AttrPassthroughGPUEnabled: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := NewListFetcher(tt.flags, tt.attributes)
			require.NoError(t, err)
			for ff, want := range tt.wantFFs {
				assert.Equal(t, want, f.IsFeatureFlagEnabled(ff), ff.Key)
			}
			for attr, want := range tt.wantAttrs {
				assert.Equal(t, want, f.IsAttributeEnabled(attr), attr.Key)
			}
			assert.False(t, f.IsFeatureFlagEnabled(nil))
			assert.False(t, f.IsAttributeEnabled(nil))
		})
	}
}

func TestNewListFetcher_RejectsInvalidCombination(t *testing.T) {
	_, err := NewListFetcher([]string{HelmAllowCPUNodes.Key, HelmResourceConstraints.Key}, nil)
	require.Error(t, err)
}

func TestNewListFetcher_LeavesProcessFlagsUnchanged(t *testing.T) {
	enforce, overhead, kata := EnforceHelmTaskResourceLimits.enabled, InfraResourceOverhead.enabled,
		AttrKataRuntimeIsolation.enabled

	_, err := NewListFetcher([]string{EnforceHelmTaskResourceLimits.Key}, []string{"KataRuntimeIsolation"})
	require.NoError(t, err)

	assert.True(t, enforce == EnforceHelmTaskResourceLimits.enabled)
	assert.True(t, overhead == InfraResourceOverhead.enabled)
	assert.True(t, kata == AttrKataRuntimeIsolation.enabled)
}
