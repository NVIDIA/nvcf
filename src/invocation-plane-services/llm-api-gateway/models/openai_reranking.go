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

package models

type RerankingRequest struct {
	Model       string   `json:"model"`
	Query       string   `json:"query"`
	Docs        []string `json:"docs"`
	Instruction *string  `json:"instruction,omitempty"`
}

type RerankingResponse struct {
	Results []RerankingResult `json:"results"`
}

type RerankingResult struct {
	Doc   string  `json:"doc"`
	Score float64 `json:"score"`
}
