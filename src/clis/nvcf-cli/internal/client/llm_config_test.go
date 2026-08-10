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

package client

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactDtoMarshalsLLMConfig(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(ArtifactDto{
		Name: "dummy-model",
		LLMConfig: &LLMConfigDto{
			URIs:           []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"},
			RoutingMethod:  stringPtr("round_robin"),
			TokenRateLimit: stringPtr("1000-M"),
		},
	})
	if err != nil {
		t.Fatalf("marshal artifact dto: %v", err)
	}

	body := string(payload)
	for _, want := range []string{
		`"name":"dummy-model"`,
		`"llmConfig"`,
		`"uris":["/v1/chat/completions","/v1/responses","/v1/embeddings"]`,
		`"routingMethod":"round_robin"`,
		`"tokenRateLimit":"1000-M"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("payload %s missing %s", body, want)
		}
	}
}
