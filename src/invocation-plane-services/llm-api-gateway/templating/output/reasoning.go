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

package output

import (
	"strings"
)

// ReasoningConfig is basic token configuration for model reasoning output.
type ReasoningConfig interface {
	IsBegin(token string) bool
	IsEnd(token string) bool
	Prefill() string
}

var _ ReasoningConfig = (*ReasoningConfigImpl)(nil)

type ReasoningConfigImpl struct {
	begin   string
	end     string
	prefill string
}

// XMLThinkReasoningConfig returns a new [ReasoningConfig] appropriate for the
// allam-2-34b and qwen3 models.
func XMLThinkReasoningConfig() ReasoningConfig {
	return &ReasoningConfigImpl{
		begin: "<think>",
		end:   "</think>",
	}
}

// XMLThinkWithPrefillReasoningConfig returns a new [ReasoningConfig] appropriate for
// the templates which prefill the reasoning token.
func XMLThinkWithPrefillReasoningConfig() ReasoningConfig {
	return &ReasoningConfigImpl{
		// The prefill value is used instead of this, but this needs to be
		// non-empty to enable reasoning parsing code.
		begin:   "\n<think>\n",
		end:     "</think>",
		prefill: "\n<think>\n",
	}
}

// IsBegin indicates whether the given token matches the configured reasoning
// begin token.
func (c *ReasoningConfigImpl) IsBegin(token string) bool {
	// XXX: We assume that reasoning transitions will be demarcated by a single
	//      token. We will need to buffer tokens if this is not true for future
	//      models.
	return strings.HasPrefix(token, c.begin)
}

// IsEnd indicates whether the given token matches the configured reasoning end
// token.
func (c *ReasoningConfigImpl) IsEnd(token string) bool {
	return token == c.end
}

func (c *ReasoningConfigImpl) Prefill() string {
	return c.prefill
}

func NewReasoningConfigImpl(begin string, end string, prefill string) ReasoningConfig {
	return &ReasoningConfigImpl{
		begin:   begin,
		end:     end,
		prefill: prefill,
	}
}

func NewCohereReasoningConfig() ReasoningConfig {
	return &ReasoningConfigImpl{
		begin: "<|START_THINKING|>",
		end:   "<|END_THINKING|>",
	}
}
