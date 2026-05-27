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
