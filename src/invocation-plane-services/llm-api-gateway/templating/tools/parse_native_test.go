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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/templating/tools"
)

func TestNativeParseConfig_GetForcedUseInstruction(t *testing.T) {
	var (
		cfg = tools.NativeParseConfig{
			ToolNoun: "nifty",
		}
		choice = models.ChatCompletionToolChoiceField{
			String: ptr.To(models.ChatToolSelectionRequired),
			ToolChoice: &models.ChatToolChoice{
				Type: models.ToolTypeFunction,
				Function: models.ChatFunctionChoice{
					Name: t.Name(),
				},
			},
		}
	)

	have, enabled := cfg.GetForcedUseInstruction(choice)
	require.True(t, enabled)

	want := fmt.Sprintf(
		"You must use the nifty %s to answer the user query.",
		t.Name(),
	)
	require.Equal(t, want, have)
}

func BenchmarkNativeParseConfig_GetForcedUseInstruction(b *testing.B) {
	var (
		cfg = tools.NativeParseConfig{
			ToolNoun: "nifty",
		}
		choice = models.ChatCompletionToolChoiceField{
			String: ptr.To(models.ChatToolSelectionRequired),
			ToolChoice: &models.ChatToolChoice{
				Type: models.ToolTypeFunction,
				Function: models.ChatFunctionChoice{
					Name: b.Name(),
				},
			},
		}
	)

	b.ResetTimer()
	for b.Loop() {
		cfg.GetForcedUseInstruction(choice)
	}
}
