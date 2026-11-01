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

package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolEnvelopesDefaultParseConfigSingle(t *testing.T) {
	t.Parallel()

	envelopes := ToolEnvelopes(
		NewDefaultParseConfig(),
		Params{
			ParallelToolCalls: false,
		},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "<tool-use>", envelopes[0].BeginMarker)
	require.Equal(t, "</tool-use>", envelopes[0].EndMarker)
	require.Equal(t, _singleCallSchema, envelopes[0].Schema)
}

func TestToolEnvelopesDefaultParseConfigParallel(t *testing.T) {
	t.Parallel()

	envelopes := ToolEnvelopes(
		NewDefaultParseConfig(),
		Params{
			ParallelToolCalls: true,
		},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "<tool-use>", envelopes[0].BeginMarker)
	require.Equal(t, "</tool-use>", envelopes[0].EndMarker)
	require.Equal(t, _parallelCallSchema, envelopes[0].Schema)
}

func TestToolEnvelopesDeepSeekParseConfig(t *testing.T) {
	t.Parallel()

	envelopes := ToolEnvelopes(
		NewDeepSeekParseConfig(),
		Params{},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "<tool_call>", envelopes[0].BeginMarker)
	require.Equal(t, "</tool_call>", envelopes[0].EndMarker)
	require.Equal(t, _bareCallSchema, envelopes[0].Schema)
}

func TestToolEnvelopesArrayParseConfig(t *testing.T) {
	t.Parallel()

	envelopes := ToolEnvelopes(
		arrayParseConfig{},
		Params{},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "[", envelopes[0].BeginMarker)
	require.Equal(t, "]", envelopes[0].EndMarker)
	require.Equal(t, _bareArrayCallSchema, envelopes[0].Schema)
}

func TestToolEnvelopesGLMParseConfigMarkerOnly(t *testing.T) {
	t.Parallel()

	envelopes := ToolEnvelopes(
		NewGLMParseConfig(),
		Params{},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "<tool_call>", envelopes[0].BeginMarker)
	require.Equal(t, "</tool_call>", envelopes[0].EndMarker)
	require.Empty(t, envelopes[0].Schema)
}

func TestToolEnvelopesNilParseConfig(t *testing.T) {
	t.Parallel()

	require.Nil(t, ToolEnvelopes(nil, Params{}))
}
