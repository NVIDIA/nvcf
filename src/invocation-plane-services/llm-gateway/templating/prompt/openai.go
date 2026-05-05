//go:build harmony

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
	"fmt"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/nvidia-lpu/harmony"
	zlog "github.com/rs/zerolog/log"
	"go.mway.dev/chrono/clock"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/token"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var (
	ErrInvalidUTF8 = errors.New("invalid utf-8")

	_ TokenizedTemplate = (*Fire)(nil)
)

type Fire struct {
	enc     harmony.Encoding
	clk     clock.Clock
	version string
}

func NewFire() *Fire {
	return NewFireWithClock(clock.NewWallClock())
}

func NewFireWithClock(clk clock.Clock) *Fire {
	return NewFireWithClockUniFFI(clk)
}

func NewFireWithClockRawFFI(clk clock.Clock) *Fire {
	return &Fire{
		// Raw is fast and unsafe.
		enc: must.Get(harmony.NewEncoding()),
		clk: clk,
	}
}

func NewFireWithClockUniFFI(clk clock.Clock) *Fire {
	version := "unknown"
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, mod := range info.Deps {
			if mod.Path == "github.com/nvidia-lpu/harmony" {
				version = mod.Version
				break
			}
		}
	}

	return &Fire{
		// UniFFI is slow and safe.
		enc:     must.Get(harmony.NewEncodingUniFFI()),
		clk:     clk,
		version: version,
	}
}

func (f *Fire) RenderTokens(
	msgs []models.ChatMessage,
	params tools.Params,
) (tokens token.Tokens, n int, err error) {
	convo, convoErr := newHarmonyConversation(msgs, &params, f.clk, f.enc)
	if convoErr != nil {
		return nil, 0, convoErr
	}

	// n.b. RenderConversationForCompletion may panic if the input contains
	//      invalid utf-8 sequences. Special case this behavior to return a
	//      special error. Re-panic anything else.
	defer func() {
		x := recover()
		if x == nil {
			return
		}

		if panickedErr, ok := x.(error); ok {
			if IsInvalidUTF8Error(panickedErr) {
				// For sanity, since we're already erroring, scan msgs to see
				// what has invalid utf8 and report it back to the user.
				err = newInvalidUTF8Error(msgs)
				return
			}
		}

		panic(x)
	}()

	x, err := f.enc.RenderConversationForCompletion(
		convo,
		harmony.RoleAssistant,
	)
	if err != nil {
		return nil, 0, fmt.Errorf(
			"failed to render tokens with harmony: %w",
			err,
		)
	}

	// n.b. This is a width-compatible conversion of Go-owned memory.
	tokens = token.Tokens{*(*token.ContentTokens)(unsafe.Pointer(&x))}
	return tokens, len(x), nil
}

func (f *Fire) RenderMessages(
	_ []uint32,
) ([]models.ChatMessage, error) {
	// TODO(mway): Implement (maybe)
	return nil, nil
}

// We need a reasoning config to inform validation code that this is a reasoning model.
// But harmony handler will handle reasoning formatting instead of model_gateway_sombrero,
// so we ensure that the impl functions always return false
type openAIReasoningConfig struct{}

func (*openAIReasoningConfig) IsBegin(_ string) bool {
	return false
}

func (*openAIReasoningConfig) IsEnd(_ string) bool {
	return false
}

func (*openAIReasoningConfig) Prefill() string {
	return ""
}

func (*openAIReasoningConfig) DropToken(_ string) bool {
	return false
}

var _ output.ReasoningConfig = (*openAIReasoningConfig)(nil)

func (*Fire) ReasoningConfig() output.ReasoningConfig { return &openAIReasoningConfig{} }

// Non-applicable methods for tokenized renderers.
func (*Fire) ToolParseConfig() tools.ParseConfig { return nil }

func (*Fire) GetForcedToolUsePrefix(
	*models.ChatCompletionToolChoiceField,
) string {
	return ""
}

func (*Fire) DropTokens() []string {
	return nil
}

func (*Fire) GetForcedUseInstruction(
	models.ChatCompletionToolChoiceField,
) (string, bool) {
	return "", false
}

func newToolNamespaceConfig(
	name string,
	desc string,
	chatTools []tools.Tool,
) harmony.ToolNamespaceConfig {
	cfg := harmony.ToolNamespaceConfig{
		Name:        name,
		Description: desc,
	}

	if len(chatTools) > 0 {
		cfg.Tools = make([]harmony.ToolDescription, len(chatTools))
		for i, tool := range chatTools {
			// n.b. The FFI bindings do not currently have support for discrete
			//      JSON types; for now, parameters must be passed through the
			//      FFI (across both the Go and Rust boundaries) as a JSON
			//      string, with Go marshaling and Rust unmarshaling.
			var params []byte
			if tool.Function.Parameters != nil {
				params = must.Get(json.Marshal(tool.Function.Parameters))
			}

			cfg.Tools[i] = harmony.ToolDescription{
				Name:           tool.Function.Name,
				Description:    ptr.Deref(tool.Function.Description),
				ParametersJSON: json.RawMessage(params),
			}
		}
	}

	return cfg
}

func newDeveloperMessage(
	msg harmony.DeveloperContent,
	userTools []tools.Tool,
	includeTools bool,
) harmony.Message {
	if includeTools && len(userTools) > 0 {
		const (
			namespace   = "functions"
			description = ""
		)
		msg.Tools = harmony.NamespacedTools{
			namespace: newToolNamespaceConfig(
				namespace,
				description,
				userTools,
			),
		}
	}

	return harmony.Message{
		Author: harmony.Author{
			Role: harmony.RoleDeveloper,
		},
		Content: []harmony.Content{msg},
	}
}

func toHarmonyReasoningEffort(effort string) harmony.ReasoningEffort {
	switch effort {
	case models.ReasoningEffortLow:
		return harmony.LowReasoningEffort
	case models.ReasoningEffortMedium:
		return harmony.MediumReasoningEffort
	case models.ReasoningEffortHigh:
		return harmony.HighReasoningEffort
	default:
		return harmony.InvalidReasoningEffort
	}
}

func newHarmonyConversation(
	msgs []models.ChatMessage,
	params *tools.Params,
	clk clock.Clock,
	enc harmony.Encoding,
) ([]harmony.Message, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	var (
		other = make([]harmony.Message, 0, len(msgs))
		dev   []harmony.Message

		browserEnabled bool
		pythonEnabled  bool
		userTools      = make([]tools.Tool, 0, len(params.Tools))
	)

	for _, tool := range params.Tools {
		switch tool.Type {
		case models.ToolTypeFunction:
			userTools = append(userTools, tool)
		case models.ToolTypeBrowserSearch:
			browserEnabled = true
		case models.ToolTypeCodeInterpreter:
			pythonEnabled = true
		}
	}

	for i := range msgs {
		conv, err := orionToHarmonyMessages(msgs[i], msgs[:i], userTools, len(dev) == 0)
		if err != nil {
			return nil, err
		}
		if len(conv) == 0 {
			zlog.Warn().Msg("orionToHarmonyMessages produced no output")
			continue
		}

		if conv[0].Author.Role == harmony.RoleDeveloper {
			dev = append(dev, conv...)
		} else {
			other = append(other, conv...)
		}
	}

	// If there have been no developer messages, add one with tools.
	if len(dev) == 0 {
		dev = append(dev, newDeveloperMessage(
			harmony.DeveloperContent{},
			userTools,
			true,
		))
	}

	convo := make([]harmony.Message, 0, 1+len(dev)+len(other))
	convo = append(
		convo,
		harmony.NewSystemMessage(func() harmony.SystemContent {
			system := harmony.DefaultSystemContent()
			system.Tools = harmony.NamespacedTools{}
			system.ConversationStartDate = clk.Now().UTC().Format(time.DateOnly)
			system.ChannelConfig.ChannelRequired = true

			if browserEnabled {
				system.Tools["browser"] = enc.BrowserToolNamespaceConfig()
			}

			if pythonEnabled {
				system.Tools["python"] = enc.PythonToolNamespaceConfig()
			}

			if params.ReasoningEffort != "" {
				system.ReasoningEffort = toHarmonyReasoningEffort(params.ReasoningEffort)
			}

			return system
		}()),
	)

	return append(append(convo, dev...), other...), nil
}

func orionToHarmonyMessages(
	in models.ChatMessage,
	prevMsgs []models.ChatMessage,
	userTools []tools.Tool,
	includeTools bool,
) ([]harmony.Message, error) {
	role, err := newHarmonyRoleFromString(in.Role)
	if err != nil {
		return nil, err
	}

	// We need to produce at least one message.
	var (
		calls       = ptr.Deref(in.ToolCalls)
		reasoning   = ptr.Deref(in.Reasoning)
		numMessages = max(1, len(calls))
	)

	// However if our input has reasoning, we will need an extra message - one
	// for the reasoning, followed by one or more for tools, content, etc.
	if len(reasoning) > 0 {
		numMessages++
	}

	msgs := make([]harmony.Message, 0, numMessages)
	if len(reasoning) > 0 {
		msg := harmony.NewAssistantMessage(reasoning)
		msg.Channel = harmony.ChannelAnalysis
		msgs = append(msgs, msg)
	}

	// There is a (potentially) 1:N relationship between common OAI and Harmony
	// messages, mostly due to the fact that a Harmony message may contain at
	// most one tool or function call, whereas an OAI message may contain N
	// tool calls (but only one function call).
	if len(calls) > 0 {
		for _, call := range calls {
			msgs = append(msgs, harmony.Message{
				Channel: harmony.ChannelCommentary,
				Author: harmony.Author{
					Role: role,
					Name: ptr.Deref(in.Name),
				},
				Recipient: call.Function.Name,
				Content: harmony.MultiContent{
					harmony.TextContent{
						Text: call.Function.Arguments,
					},
				},
			})
		}

		return msgs, nil
	}

	if in.FunctionCall != nil {
		return append(msgs, harmony.Message{
			Channel: in.Channel,
			Author: harmony.Author{
				Role: role,
				Name: ptr.Deref(in.Name),
			},
			Recipient: in.FunctionCall.Name,
			Content: harmony.MultiContent{
				harmony.TextContent{
					Text: in.FunctionCall.Arguments,
				},
			},
		}), nil
	}

	if role == harmony.RoleDeveloper {
		return append(msgs, newDeveloperMessage(
			harmony.DeveloperContent{
				Instructions: in.Content.MustSingleText(),
			},
			userTools,
			includeTools,
		)), nil
	}

	if role == harmony.RoleAssistant {
		if len(in.Content) > 0 {
			msg := harmony.NewAssistantMessage(in.Content.MustSingleText())
			msg.Author.Name = ptr.Deref(in.Name)
			msg.Channel = in.Channel
			if msg.Channel == "" {
				msg.Channel = harmony.ChannelFinal
			}
			msgs = append(msgs, msg)
		}
		return msgs, nil
	}

	if role == harmony.RoleTool {
		name := ptr.Deref(in.Name)
		if len(name) == 0 {
			name = findPreviousFunctionCallName(prevMsgs)
		}
		if len(name) > 0 {
			name = FixHarmonyRequestToolName(name)
		}

		return []harmony.Message{{
			Channel: in.Channel,
			Author: harmony.Author{
				Role: role,
				Name: name,
			},
			Recipient: "assistant",
			Content: harmony.MultiContent{
				harmony.TextContent{
					Text: in.Content.MustSingleText(),
				},
			},
		}}, nil
	}

	return []harmony.Message{{
		Channel: in.Channel,
		Author: harmony.Author{
			Name: ptr.Deref(in.Name),
			Role: role,
		},
		Content: harmony.MultiContent{
			harmony.TextContent{
				Text: in.Content.MustSingleText(),
			},
		},
	}}, nil
}

func FixHarmonyRequestToolName(toolName string) string {
	switch {
	case strings.HasPrefix(toolName, "browser."):
	case toolName == "python":
	default:
		// put user tool results in the `functions` namespace
		toolName = "functions." + toolName
	}
	return toolName
}

func FixHarmonyResponseToolName(toolName string) string {
	switch {
	case strings.HasPrefix(toolName, "functions."):
		// remove the `functions` namespace for user provided functions
		return strings.TrimPrefix(toolName, "functions.")
	case strings.HasPrefix(toolName, "browser."):
		// model likes to add some bogus suffixes to browser tool names, get rid of them
		switch {
		case strings.HasPrefix(toolName, "browser.search"):
			return "browser.search"
		case strings.HasPrefix(toolName, "browser.open"):
			return "browser.open"
		case strings.HasPrefix(toolName, "browser.find"):
			return "browser.find"
		}
	case strings.HasPrefix(toolName, "python"):
		return "python"
	}
	return toolName
}

func HarmonyDecodeTokens(tokens token.Tokens) string {
	var (
		enc           = must.Get(harmony.NewEncoding())
		harmonyTokens []harmony.Rank
	)
	for _, t := range tokens {
		tt := must.As[token.ContentTokens](t)
		for _, t := range tt {
			harmonyTokens = append(harmonyTokens, harmony.Rank(t))
		}
	}
	decode := must.Get(enc.Decode(harmonyTokens))
	return decode
}

func IsInvalidUTF8Error(err error) bool {
	if err == nil {
		return false
	}

	const (
		variantA = "Failed to convert arg 'messages': incomplete utf-8"
		variantB = "Failed to convert arg 'messages': invalid utf-8"
	)

	msg := err.Error()
	return strings.Contains(msg, variantA) || strings.Contains(msg, variantB)
}

func newHarmonyRoleFromString(str string) (harmony.Role, error) {
	switch str {
	case models.ChatCompletionRoleUser:
		return harmony.RoleUser, nil
	case models.ChatCompletionRoleAssistant:
		return harmony.RoleAssistant, nil
	case models.ChatCompletionRoleSystem, harmony.RoleDeveloper.String():
		return harmony.RoleDeveloper, nil
	case models.ChatCompletionRoleTool, models.ChatCompletionRoleFunction:
		return harmony.RoleTool, nil
	default:
		return harmony.InvalidRole, fmt.Errorf("invalid role: %q", str)
	}
}

func findPreviousFunctionCallName(prev []models.ChatMessage) string {
	for i := len(prev) - 1; i >= 0; i-- {
		msg := prev[i]

		if toolCalls := ptr.Deref(msg.ToolCalls); len(toolCalls) > 0 {
			return toolCalls[len(toolCalls)-1].Function.Name
		}

		if msg.FunctionCall != nil {
			return msg.FunctionCall.Name
		}
	}

	return ""
}

func newInvalidUTF8Error(msgs []models.ChatMessage) error {
	for i := range msgs {
		for j := range msgs[i].Content {
			switch content := msgs[i].Content[j].(type) {
			case models.ContentPartText:
				if !utf8.ValidString(content.String()) {
					return fmt.Errorf(
						"%w: message %d has invalid utf-8 sequence(s)",
						ErrInvalidUTF8,
						i+1,
					)
				}
			default:
				// Ignore other content types, we only care about
				// text input.
			}
		}
	}

	zlog.Warn().Msg("could not find invalid utf8 sequence")
	return ErrInvalidUTF8
}

func (f *Fire) Version() string { return f.version }
