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

package tools_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func TestToolEnvelopesQuattro4(t *testing.T) {
	t.Parallel()

	templater := prompt.NewQuattro4()

	envelopes := tools.ToolEnvelopes(
		templater.ToolParseConfig(),
		tools.Params{
			ParallelToolCalls: true,
		},
	)
	require.Len(t, envelopes, 1)
	require.Equal(t, "[", envelopes[0].BeginMarker)
	require.Equal(t, "]", envelopes[0].EndMarker)
	require.Equal(t, tools.BareArrayParametersCallSchema(), envelopes[0].Schema)
}
