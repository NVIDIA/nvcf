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

package testhelpers

import (
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

func NewWeatherInfoTool() tools.Tool {
	return tools.Tool{
		Type: "function",
		Function: tools.ChatFunctionSpec{
			Name:        "weather_info",
			Description: ptr.To("Get the current weather in a given location"),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{
						"type":        "string",
						"description": "The city and state, e.g. San Francisco, CA",
					},
				},
				"required": []any{
					"location",
				},
			},
		},
	}
}

func NewStockPricesTool() tools.Tool {
	return tools.Tool{
		Type: "function",
		Function: tools.ChatFunctionSpec{
			Name: "stock_prices",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type":        "string",
						"description": "The ticket symbol",
					},
				},
				"required": []any{
					"symbol",
				},
			},
		},
	}
}
