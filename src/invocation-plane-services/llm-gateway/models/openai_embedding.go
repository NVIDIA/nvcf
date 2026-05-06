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

import (
	"encoding/json"
	"errors"
)

const (
	EmbeddingEncodingFormatFloat  = "float"
	EmbeddingEncodingFormatBase64 = "base64"
	ObjectEmbedding               = "embedding"
)

type EmbeddingInputField []string

type CreateEmbeddingRequest struct {
	Input          EmbeddingInputField `json:"input"`
	Model          string              `json:"model"`
	EncodingFormat *string             `json:"encoding_format"`
	User           *string             `json:"user"`
}

type EmbeddingEmbedding struct {
	Vector *[]float32
	Base64 *string
}

type Embedding struct {
	Object    string             `json:"object"`
	Embedding EmbeddingEmbedding `json:"embedding"`
	Index     uint32             `json:"index"`
}

type EmbeddingUsage struct {
	QueueTime    *float64 `json:"queue_time,omitempty"`
	PromptTokens uint32   `json:"prompt_tokens"`
	PromptTime   float64  `json:"prompt_time"`
	TotalTokens  uint32   `json:"total_tokens"`
	TotalTime    float64  `json:"total_time"`
}

type CreateEmbeddingResponse struct {
	Object string         `json:"object"`
	Data   []Embedding    `json:"data"`
	Model  string         `json:"model"`
	Usage  EmbeddingUsage `json:"usage"`
}

func (ef *EmbeddingInputField) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if len(single) > 0 {
			*ef = []string{single}
		}
		return nil
	}

	var multiple []string
	if err := json.Unmarshal(data, &multiple); err == nil {
		*ef = multiple
		return nil
	}

	return &UnmarshalError{
		Field: "input",
		Msg:   "Must be either a string or an array of string",
	}
}

func (ee *EmbeddingEmbedding) MarshalJSON() ([]byte, error) {
	if ee.Vector != nil {
		return json.Marshal(ee.Vector)
	}
	if ee.Base64 != nil {
		return json.Marshal(ee.Base64)
	}
	return nil, errors.New("EmbeddingEmbedding must have either Vector or Base64")
}
