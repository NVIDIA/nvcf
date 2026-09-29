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

package api

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func TestSplitOpenAIModelIDCanonicalizesUUIDRoutingKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		modelID        string
		wantRoutingKey string
		wantModel      string
	}{
		{
			name:           "uppercase uuid lowercased",
			modelID:        "3F2B1C4D-5E6A-4B7C-8D9E-0A1B2C3D4E5F/company-name/model-name",
			wantRoutingKey: "3f2b1c4d-5e6a-4b7c-8d9e-0a1b2c3d4e5f",
			wantModel:      "company-name/model-name",
		},
		{
			name:           "mixed case uuid lowercased",
			modelID:        "3f2b1c4d-5E6A-4b7c-8D9E-0a1b2c3d4e5f/company-name/model-name",
			wantRoutingKey: "3f2b1c4d-5e6a-4b7c-8d9e-0a1b2c3d4e5f",
			wantModel:      "company-name/model-name",
		},
		{
			name:           "lowercase uuid unchanged",
			modelID:        "3f2b1c4d-5e6a-4b7c-8d9e-0a1b2c3d4e5f/company-name/model-name",
			wantRoutingKey: "3f2b1c4d-5e6a-4b7c-8d9e-0a1b2c3d4e5f",
			wantModel:      "company-name/model-name",
		},
		{
			name:           "opaque routing key passes through",
			modelID:        "Fn-Alpha/company-name/model-name",
			wantRoutingKey: "Fn-Alpha",
			wantModel:      "company-name/model-name",
		},
		{
			name:           "36-char non-uuid key passes through",
			modelID:        "abcdefghijklmnopqrstuvwxyz0123456789/model",
			wantRoutingKey: "abcdefghijklmnopqrstuvwxyz0123456789",
			wantModel:      "model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			routingKey, routedModel, hasPrefix := splitOpenAIModelID(tc.modelID)
			if !hasPrefix {
				t.Fatalf("hasPrefix = false, want true")
			}
			if routingKey != tc.wantRoutingKey {
				t.Fatalf("routing key = %q, want %q", routingKey, tc.wantRoutingKey)
			}
			if routedModel != tc.wantModel {
				t.Fatalf("routed model = %q, want %q", routedModel, tc.wantModel)
			}
		})
	}
}

func TestSetRoutingMethodForModelForwardsTrimmedAuthValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "underscore alias", in: "round_robin", want: "round_robin"},
		{name: "hyphen spelling", in: "power-of-two", want: "power-of-two"},
		{name: "unknown method", in: "least_loaded", want: "least_loaded"},
		{name: "trimmed method", in: "  experimental_method  ", want: "experimental_method"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqCtx := &requestctx.RequestContext{
				ModelSpecs: map[string]nvcf.ModelSpec{
					"company-name/model-name": {
						RoutingMethod: tt.in,
					},
				},
			}

			setRoutingMethodForModel(reqCtx, "company-name/model-name")

			if reqCtx.RoutingMethod != tt.want {
				t.Fatalf("routing method = %q, want %q", reqCtx.RoutingMethod, tt.want)
			}
		})
	}
}

func TestSetRoutingMethodForModelOmitsEmptyAuthValue(t *testing.T) {
	t.Parallel()

	reqCtx := &requestctx.RequestContext{
		RoutingMethod: "round-robin",
		ModelSpecs: map[string]nvcf.ModelSpec{
			"company-name/model-name": {
				RoutingMethod: "   ",
			},
		},
	}

	setRoutingMethodForModel(reqCtx, "company-name/model-name")

	if reqCtx.RoutingMethod != "" {
		t.Fatalf("routing method = %q, want empty", reqCtx.RoutingMethod)
	}
}
