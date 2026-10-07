// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvcaconfig

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyAndChartLogChunking is a merge config where a legacy uppercase
// override sits next to the lowercase block generated from chart defaults.
const legacyAndChartLogChunking = `agent:
  BYOOLogChunking:
    dryRun: false
    exporterBatchMaxSizeBytes: 1000000
    maxBodyBytes: 262144
  byooLogChunking:
    dryRun: false
    maxPayloadBytes: 0
`

func keyConflicts(t *testing.T, err error) []*KeyConflictError {
	t.Helper()
	require.Error(t, err)
	var conflicts []*KeyConflictError
	var walk func(error)
	walk = func(err error) {
		switch e := err.(type) {
		case *KeyConflictError:
			conflicts = append(conflicts, e)
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)
	require.NotEmpty(t, conflicts, "expected a KeyConflictError in %v", err)
	return conflicts
}

func TestDecodeConfigRejectsCaseVariantKeys(t *testing.T) {
	_, err := DecodeConfig([]byte(legacyAndChartLogChunking))

	conflicts := keyConflicts(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, &KeyConflictError{
		Path:  "agent",
		Keys:  []string{"BYOOLogChunking", "byooLogChunking"},
		Lines: []int{2, 6},
	}, conflicts[0])
	assert.EqualError(t, err,
		`read config: config keys "BYOOLogChunking", "byooLogChunking" under "agent" differ only by case (lines 2, 6); keep one spelling`)
}

func TestDecodeConfigRejectsCaseVariantKeysDeterministically(t *testing.T) {
	for range 50 {
		_, err := DecodeConfig([]byte(legacyAndChartLogChunking))
		keyConflicts(t, err)
	}
}

func TestValidateYAMLKeys(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []*KeyConflictError
	}{
		{
			name: "canonical config",
			data: `agent:
  byooLogChunking:
    maxPayloadBytes: 262144
  BYOOResources:
    cpu: "1"
`,
		},
		{
			name: "same key name in different mappings",
			data: `agent:
  byooLogChunking:
    dryRun: true
  byooOtelCollector:
    dryRun: true
`,
		},
		{
			name: "exact duplicate at the top level",
			data: `agent: {}
agent: {}
`,
			want: []*KeyConflictError{{Keys: []string{"agent", "agent"}, Lines: []int{1, 2}}},
		},
		{
			name: "exact duplicate in a nested mapping",
			data: `agent:
  byooLogChunking:
    maxPayloadBytes: 1
    maxPayloadBytes: 2
`,
			want: []*KeyConflictError{{
				Path:  "agent.byooLogChunking",
				Keys:  []string{"maxPayloadBytes", "maxPayloadBytes"},
				Lines: []int{3, 4},
			}},
		},
		{
			name: "case variants in a nested mapping",
			data: `agent:
  byooLogChunking:
    maxPayloadBytes: 1
    MaxPayloadBytes: 2
`,
			want: []*KeyConflictError{{
				Path:  "agent.byooLogChunking",
				Keys:  []string{"maxPayloadBytes", "MaxPayloadBytes"},
				Lines: []int{3, 4},
			}},
		},
		{
			name: "case variants in a mapping inside a sequence",
			data: `agent:
  items:
    - name: a
      Name: b
`,
			want: []*KeyConflictError{{
				Path:  "agent.items[0]",
				Keys:  []string{"name", "Name"},
				Lines: []int{3, 4},
			}},
		},
		{
			name: "three spellings",
			data: `agent:
  byooLogChunking: {}
  BYOOLogChunking: {}
  ByooLogChunking: {}
`,
			want: []*KeyConflictError{{
				Path:  "agent",
				Keys:  []string{"byooLogChunking", "BYOOLogChunking", "ByooLogChunking"},
				Lines: []int{2, 3, 4},
			}},
		},
		{
			name: "every conflict is reported",
			data: `agent:
  BYOOLogChunking: {}
  byooLogChunking: {}
  byooOtelCollector:
    image: a
    Image: b
`,
			want: []*KeyConflictError{
				{Path: "agent", Keys: []string{"BYOOLogChunking", "byooLogChunking"}, Lines: []int{2, 3}},
				{Path: "agent.byooOtelCollector", Keys: []string{"image", "Image"}, Lines: []int{5, 6}},
			},
		},
		{
			name: "merge key overridden by the same spelling",
			data: `base: &base
  maxPayloadBytes: 1
agent:
  byooLogChunking:
    <<: *base
    maxPayloadBytes: 2
`,
		},
		{
			name: "merge key overridden by a different spelling",
			data: `base: &base
  maxPayloadBytes: 1
agent:
  byooLogChunking:
    <<: *base
    MaxPayloadBytes: 2
`,
			want: []*KeyConflictError{{
				Path:  "agent.byooLogChunking",
				Keys:  []string{"MaxPayloadBytes", "maxPayloadBytes"},
				Lines: []int{6, 5},
			}},
		},
		{
			name: "invalid YAML is left to the decoder",
			data: "agent: [",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYAMLKeys([]byte(tt.data))
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			assert.Equal(t, tt.want, keyConflicts(t, err))
		})
	}
}

func TestKeyConflictErrorMessage(t *testing.T) {
	assert.EqualError(t,
		&KeyConflictError{Keys: []string{"agent", "agent"}, Lines: []int{1, 2}},
		`config key "agent" at the top level is defined more than once (lines 1, 2)`)
	assert.EqualError(t,
		&KeyConflictError{Path: "agent", Keys: []string{"BYOOLogChunking", "byooLogChunking"}, Lines: []int{2, 6}},
		`config keys "BYOOLogChunking", "byooLogChunking" under "agent" differ only by case (lines 2, 6); keep one spelling`)
}

func TestDecodeConfigAcceptsSingleLegacySpelling(t *testing.T) {
	cfg, err := DecodeConfig([]byte(`agent:
  BYOOLogChunking:
    dryRun: false
    exporterBatchMaxSizeBytes: 1000000
    maxBodyBytes: 262144
`))
	require.NoError(t, err)
	assert.Equal(t, int64(262144), cfg.Agent.BYOOLogChunking.MaxBodyBytes)
	assert.Equal(t, int64(262144), cfg.Agent.BYOOLogChunking.Complete().MaxPayloadBytes)
}

func TestDecodeConfigCaseVariantsAcrossInputsUseInputOrder(t *testing.T) {
	base := []byte(`agent:
  byooLogChunking:
    maxPayloadBytes: 100
`)
	override := []byte(`agent:
  BYOOLogChunking:
    maxPayloadBytes: 200
`)
	for range 50 {
		cfg, err := DecodeConfig(base, override)
		require.NoError(t, err)
		require.Equal(t, int64(200), cfg.Agent.BYOOLogChunking.MaxPayloadBytes)
	}
}

func TestDecodeConfigRejectsCaseVariantKeysInExtraInput(t *testing.T) {
	_, err := DecodeConfig([]byte("agent: {}\n"), []byte(legacyAndChartLogChunking))

	conflicts := keyConflicts(t, err)
	assert.Equal(t, "agent", conflicts[0].Path)
	assert.ErrorContains(t, err, "merge extra config: ")
}

func TestDecodeConfigReportsInvalidYAMLFromDecoder(t *testing.T) {
	_, err := DecodeConfig([]byte("agent: ["))
	require.Error(t, err)
	var conflict *KeyConflictError
	assert.False(t, errors.As(err, &conflict))
	assert.ErrorContains(t, err, "read config: ")
}
