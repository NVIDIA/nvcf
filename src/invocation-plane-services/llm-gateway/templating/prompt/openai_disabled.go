//go:build !harmony

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

package prompt

import (
	"errors"

	"go.mway.dev/chrono/clock"

	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/token"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var ErrHarmonyUnavailable = errors.New("harmony-backed templates are unavailable in this build")

var _ TokenizedTemplate = (*Fire)(nil)

type Fire struct{}

func NewFire() *Fire {
	panic(ErrHarmonyUnavailable)
}

func NewFireWithClock(clock.Clock) *Fire {
	panic(ErrHarmonyUnavailable)
}

func NewFireWithClockRawFFI(clock.Clock) *Fire {
	panic(ErrHarmonyUnavailable)
}

func NewFireWithClockUniFFI(clock.Clock) *Fire {
	panic(ErrHarmonyUnavailable)
}

func (*Fire) RenderTokens(
	[]models.ChatMessage,
	tools.Params,
) (token.Tokens, int, error) {
	return nil, 0, ErrHarmonyUnavailable
}

func (*Fire) RenderMessages([]uint32) ([]models.ChatMessage, error) {
	return nil, ErrHarmonyUnavailable
}

func (*Fire) ReasoningConfig() output.ReasoningConfig {
	return nil
}

func (*Fire) ToolParseConfig() tools.ParseConfig {
	return nil
}

func (*Fire) GetForcedToolUsePrefix(*models.ChatCompletionToolChoiceField) string {
	return ""
}

func (*Fire) DropTokens() []string {
	return nil
}

func (*Fire) Version() string {
	return "harmony-disabled"
}

func HarmonyDecodeTokens(token.Tokens) string {
	return ""
}

func IsInvalidUTF8Error(error) bool {
	return false
}
