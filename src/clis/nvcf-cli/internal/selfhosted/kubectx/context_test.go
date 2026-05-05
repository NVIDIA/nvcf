/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package kubectx

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectMode(t *testing.T) {
	cases := []struct {
		name    string
		cp, gpu string
		want    Mode
	}{
		{"both unset", "", "", ModeSingle},
		{"only cp set", "admin@cp", "", ModeError},
		{"only gpu set", "", "admin@gpu1", ModeError},
		{"both set different", "admin@cp", "admin@gpu1", ModeSplit},
		{"both set same", "admin@cp", "admin@cp", ModeError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SelectMode(tc.cp, tc.gpu))
		})
	}
}

func TestEnvForPhase_NoOverwrite(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/home/u"}
	out := EnvForPhase(env, "admin@cp")
	assert.Contains(t, out, "PATH=/usr/bin")
	assert.Contains(t, out, "HOME=/home/u")
	assert.Contains(t, out, "KUBE_CONTEXT=admin@cp")
}

func TestEnvForPhase_Overwrites(t *testing.T) {
	env := []string{"KUBE_CONTEXT=stale", "PATH=/usr/bin"}
	out := EnvForPhase(env, "admin@cp")
	var foundCP int
	for _, kv := range out {
		if strings.HasPrefix(kv, "KUBE_CONTEXT=") {
			foundCP++
		}
	}
	assert.Equal(t, 1, foundCP, "exactly one KUBE_CONTEXT entry")
	assert.Contains(t, out, "KUBE_CONTEXT=admin@cp")
}

func TestEnvForPhase_EmptyCtxIsNoop(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	out := EnvForPhase(env, "")
	assert.Equal(t, env, out)
}

func TestValidateFlags_BothEmpty(t *testing.T) { assert.NoError(t, ValidateFlags("", "")) }
func TestValidateFlags_BothSet(t *testing.T)   { assert.NoError(t, ValidateFlags("a", "b")) }
func TestValidateFlags_OnlyOne(t *testing.T) {
	err := ValidateFlags("a", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must both be set or both be empty")
}

func TestValidateFlags_BothSame(t *testing.T) {
	err := ValidateFlags("admin@cp", "admin@cp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot both be")
}
