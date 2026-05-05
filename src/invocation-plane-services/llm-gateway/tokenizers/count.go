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

package tokenizers

import (
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

func numTokensForString(tokenizer *CachingTokenizer, s string) (int, error) {
	enc, err := tokenizer.Tokenize(s)
	if err != nil {
		return 0, err
	}

	return len(enc), nil
}

const (
	tokensPerMessage = 3
	tokensPerName    = 1

	tokensPerRole = 2
)

var tokenExtraForModel = map[string]int{
	"llama2-70b-4096":    15,
	"mixtral-8x7b-32768": 1,
	"gemma-7b-it":        2,
}

func (ts *TokenizerStore) TokenCountForText(model string, text string) (int, error) {
	if ts == nil {
		return 0, ErrTokenizerNotFound
	}
	if text == "" {
		return 0, nil
	}

	tokenizer, err := ts.CachingTokenizerForModel(model)
	if err != nil {
		return 0, err
	}

	return numTokensForString(tokenizer, text)
}

func (ts *TokenizerStore) TokenCountForRequest(model string, messages *[]models.ChatMessage) (int, error) {
	if messages == nil {
		return 0, nil
	}
	if ts == nil {
		return 0, ErrTokenizerNotFound
	}

	tokenizer, err := ts.CachingTokenizerForModel(model)
	if err != nil {
		return 0, err
	}

	totalTokens := tokenExtraForModel[model]

	for _, message := range *messages {
		totalTokens += tokensPerMessage

		roleTokens, err := numTokensForString(tokenizer, message.Role)
		if err != nil {
			return 0, err
		}
		totalTokens += roleTokens

		for _, content := range message.Content {
			text, ok := content.(models.ContentPartText)
			if !ok {
				continue
			}

			contentTokens, err := numTokensForString(tokenizer, text.String())
			if err != nil {
				return 0, err
			}
			totalTokens += contentTokens
		}

		if message.Name != nil {
			nameTokens, err := numTokensForString(tokenizer, *message.Name)
			if err != nil {
				return 0, err
			}

			totalTokens += nameTokens
			totalTokens += tokensPerName
		}
	}

	return totalTokens, nil
}

func EstimatedTokenCountForRequest(model string, messages *[]models.ChatMessage) int {
	if messages == nil {
		return 0
	}
	totalTokens := tokenExtraForModel[model]

	for _, message := range *messages {
		totalTokens += tokensPerMessage
		totalTokens += tokensPerRole

		for _, content := range message.Content {
			if c, ok := content.(models.ContentPartText); ok {
				totalTokens += len(c.String()) / 4
			}
		}

		if message.Name != nil {
			nameTokens := len(*message.Name) / 4
			totalTokens += nameTokens
			totalTokens += tokensPerName
		}
	}

	return totalTokens
}

func EstimatedTokenCountForEmbeddingRequest(_ string, input []string) int {
	var totalTokens int

	for _, s := range input {
		totalTokens += len(s) / 4
	}

	return totalTokens
}
