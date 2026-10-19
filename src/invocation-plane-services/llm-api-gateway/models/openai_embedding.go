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
	"bytes"
	"encoding/json"
	"errors"
)

const (
	EmbeddingEncodingFormatFloat  = "float"
	EmbeddingEncodingFormatBase64 = "base64"
	ObjectEmbedding               = "embedding"
)

type EmbeddingInputField []EmbeddingInput

type EmbeddingInput struct {
	Text   string
	Tokens []uint32
	// Raw holds an element that is not a string or a token array.
	Raw json.RawMessage
}

func (input EmbeddingInput) MarshalJSON() ([]byte, error) {
	if input.Raw != nil {
		return input.Raw, nil
	}
	if input.Tokens != nil {
		return json.Marshal(input.Tokens)
	}
	return json.Marshal(input.Text)
}

type CreateEmbeddingRequest struct {
	Input          EmbeddingInputField `json:"input"`
	Model          string              `json:"model"`
	EncodingFormat *string             `json:"encoding_format"`
	User           *string             `json:"user"`
}

func (*CreateEmbeddingRequest) DecodesMembers() {}

// UnmarshalJSON checks only model and input. Other members that fail to
// decode stay zero; the backend validates them.
func (r *CreateEmbeddingRequest) UnmarshalJSON(data []byte) error {
	_, err := DecodeMembers(data, r, "model", "input")
	return err
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
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		*ef = nil
		return nil
	}
	if !bytes.HasPrefix(trimmed, []byte("[")) {
		*ef = EmbeddingInputField{decodeEmbeddingInput(trimmed)}
		return nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return err
	}
	// An array that starts with a number or null is one token array.
	if len(items) > 0 && isNumberOrNull(items[0]) {
		*ef = EmbeddingInputField{decodeEmbeddingInput(trimmed)}
		return nil
	}
	batch := make(EmbeddingInputField, len(items))
	for i, item := range items {
		batch[i] = decodeEmbeddingInput(item)
	}
	*ef = batch
	return nil
}

func isNumberOrNull(value json.RawMessage) bool {
	switch c := value[0]; {
	case c == '-', c == 'n', c >= '0' && c <= '9':
		return true
	default:
		return false
	}
}

// decodeEmbeddingInput keeps an element that is not a string or a token array
// as raw JSON. The backend decides whether it is valid.
func decodeEmbeddingInput(data json.RawMessage) EmbeddingInput {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return EmbeddingInput{Text: text}
	}
	if tokens, err := decodeEmbeddingTokens(data); err == nil {
		return EmbeddingInput{Tokens: tokens}
	}
	return EmbeddingInput{Raw: data}
}

func decodeEmbeddingTokens(data []byte) ([]uint32, error) {
	var values []*uint32
	if err := json.Unmarshal(data, &values); err != nil || values == nil {
		return nil, &UnmarshalError{Field: "input", Msg: "token arrays must contain non-negative integers"}
	}
	tokens := make([]uint32, len(values))
	for i, value := range values {
		if value == nil {
			return nil, &UnmarshalError{Field: "input", Msg: "token arrays must not contain null"}
		}
		tokens[i] = *value
	}
	return tokens, nil
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
