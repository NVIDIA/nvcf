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

package templating

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/nvidia-lpu/minijinja"
	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/prompt"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var (
	// ErrTemplate indicates that an error is template-based.
	ErrTemplate = errors.New("template")
	// ErrTemplatNotExist indicates that the requested template does not exist.
	ErrTemplateNotExist = fmt.Errorf(
		"%w: template does not exist",
		ErrTemplate,
	)
	// ErrTemplateUnexpectedType indicates that the requested template was of
	// an unexpected type.
	ErrTemplateUnexpectedType = fmt.Errorf(
		"%w: unexpected template type",
		ErrTemplate,
	)
)

// Engine is a templating engine that holds all templates (jinja or otherwise).
type Engine struct {
	env       *minijinja.Environment
	templates map[string]prompt.Template
	mu        sync.RWMutex
}

// NewEngine creates a new [Engine] that is ready for use.
func NewEngine() *Engine {
	engine := &Engine{
		env: minijinja.NewEnvironment(
			minijinja.WithLstripBlocks(true),
			minijinja.WithTrimBlocks(true),
		),
		templates: make(map[string]prompt.Template),
	}
	return engine
}

// Close stops the engine and frees its resources.
func (e *Engine) Close() error {
	clear(e.templates)
	return e.env.Close()
}

// GetTemplate returns the [prompt.Template] corresponding to the given
// name.
func (e *Engine) GetTemplate(name string) (prompt.Template, error) {
	return e.GetTemplateBytes(unsafe.Slice(unsafe.StringData(name), len(name)))
}

// GetTemplateBytes returns the [prompt.Template] corresponding to the
// given name.
func (e *Engine) GetTemplateBytes(name []byte) (prompt.Template, error) {
	return getTemplateBytes[prompt.Template](e, name)
}

// GetTextTemplate returns the [prompt.TextTemplate] corresponding to the given
// name.
func (e *Engine) GetTextTemplate(name string) (prompt.TextTemplate, error) {
	return e.GetTextTemplateBytes(
		unsafe.Slice(unsafe.StringData(name), len(name)),
	)
}

// GetTextTemplateBytes returns the [prompt.TextTemplate] corresponding to the
// given name.
func (e *Engine) GetTextTemplateBytes(
	name []byte,
) (prompt.TextTemplate, error) {
	return getTemplateBytes[prompt.TextTemplate](e, name)
}

// GetTokenizedTemplate returns the [prompt.TokenizedTemplate] corresponding to the given
// name.
func (e *Engine) GetTokenizedTemplate(
	name string,
) (prompt.TokenizedTemplate, error) {
	return e.GetTokenizedTemplateBytes(
		unsafe.Slice(unsafe.StringData(name), len(name)),
	)
}

// GetTokenizedTemplateBytes returns the [prompt.TokenizedTemplate] corresponding to the
// given name.
func (e *Engine) GetTokenizedTemplateBytes(name []byte) (prompt.TokenizedTemplate, error) {
	return getTemplateBytes[prompt.TokenizedTemplate](e, name)
}

// HasTemplate indicates if the engine holds a template with the given name.
func (e *Engine) HasTemplate(name string) bool {
	return e.HasTemplateBytes(unsafe.Slice(unsafe.StringData(name), len(name)))
}

// HasTemplateBytes indicates if the engine holds a template with the given
// name.
func (e *Engine) HasTemplateBytes(name []byte) bool {
	_, err := e.getTemplateBytes(name)
	return err == nil
}

// TemplatesIter returns an [iter.Seq2] that iterates over all held templates
// by name.
func (e *Engine) TemplatesIter() iter.Seq2[string, prompt.Template] {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return maps.All(maps.Clone(e.templates))
}

// RegisterCustomJinjaTemplates registers all custom Jinja templates.
func (e *Engine) RegisterCustomJinjaTemplates() error {
	return nil
}

// RegisterCustomTemplates registers all custom template implementations.
func (e *Engine) RegisterCustomTemplates() error {
	return errors.Join(
		e.registerTemplate("llama3", prompt.NewLlama3()),
		e.registerTemplate("llama31", prompt.NewLlama31()),
		e.registerTemplate("llama32", prompt.NewLlama32()),
		e.registerTemplate("text-classification", prompt.NewClassification()),
		e.registerTemplate("no-op", prompt.NewClassification()),
		e.registerTemplate("quattro", prompt.NewQuattro()),
		e.registerTemplate("quattro-v2", prompt.NewQuattro4()),
		e.registerOptionalTemplate("openai/gpt-oss-20b", func() prompt.Template {
			return prompt.NewFire()
		}),
		e.registerOptionalTemplate("openai/gpt-oss-120b", func() prompt.Template {
			return prompt.NewFire()
		}),
	)
}

// RegisterHFTemplates registers all Hugging Face templates within the given
// base directory.
func (e *Engine) RegisterHFTemplates(basedir string) error {
	files, err := os.ReadDir(basedir)
	if err != nil {
		return fmt.Errorf("failed to read tokenizer files: %w", err)
	}

	for _, d := range files {
		if !d.IsDir() {
			continue
		}

		var (
			path = filepath.Join(basedir, d.Name(), "tokenizer_config.json")
			log  = zlog.With().
				Str("tokenizer", d.Name()).
				Str("config", path).
				Logger()
		)

		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Warn().Msg("no config found for tokenizer")
				continue
			}
			return fmt.Errorf(
				"failed to read tokenizer config for %q: %w",
				d.Name(),
				err,
			)
		}

		var parsed map[string]any
		if err = json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf(
				"failed to unmarshal tokenizer config for %q: %w",
				d.Name(),
				err,
			)
		}

		var (
			templateString  string
			toolUseTemplate *string
		)

		// Prefer an override file if present next to tokenizer_config.json
		// This allows us to more easily edit and diff templates if necessary
		overridePath := filepath.Join(basedir, d.Name(), "chat_template.jinja")
		if b, err := os.ReadFile(overridePath); err == nil {
			templateString = string(b)
			log.Info().
				Str("path", overridePath).
				Msg("using chat_template override from file")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read chat_template override: %w", err)
		}

		// Fallback to tokenizer_config.json content when no override exists
		if templateString == "" {
			switch chatTemplate := parsed["chat_template"].(type) {
			case string:
				templateString = chatTemplate
			case []any:
				// Handle list format - directly extract default and tool_use
				for _, item := range chatTemplate {
					if itemDict, ok := item.(map[string]any); ok {
						name, nameOk := itemDict["name"].(string)
						if !nameOk {
							log.Warn().
								Interface("value", itemDict).
								Msg("chat_template name field missing or unexpected type")
							continue
						}

						template, templateOk := itemDict["template"].(string)
						if !templateOk {
							log.Warn().
								Str("name", name).
								Interface("value", itemDict).
								Msg("chat_template template field missing or unexpected type")
							continue
						}

						switch name {
						case "default":
							templateString = template
						case "tool_use":
							toolUseTemplate = &template
						case "rag":
							log.Info().Msg("skipping rag template - not currently supported")
						default:
							log.Warn().
								Str("key", name).
								Msg("unsupported chat_template name found")
						}
					}
				}

				if templateString == "" {
					log.Warn().Msg(`chat_template list missing "default" template, skipping`)
					continue
				}
			default:
				log.Warn().Msg("tokenizer config contains no valid template, skipping")
				continue
			}
		}

		var (
			toolParseConfig            = tools.NewDefaultParseConfig()
			reasoningConfig            output.ReasoningConfig
			bosToken                   string
			eosToken                   string
			finalRole                  string
			relaxedMessageOrdering     bool
			preserveSystemMessageOrder bool
			preserveSystemContentArray bool
			orderToolParameters        bool
			imageToken                 string
			supportsToolsNatively      bool
			preProcessMessages         func([]models.ChatMessage)
			dropTokens                 []string
			preserveToolContentArray   bool
		)

		if d.Name() != "llama-guard-3-8b" {
			finalRole = models.ChatCompletionRoleUser
		}

		eosTokenRaw, ok := parsed["eos_token"]
		if ok {
			switch eos := eosTokenRaw.(type) {
			case string:
				eosToken = eos
			case map[string]any:
				eosToken = must.As[string](eos["content"])
			}
		}

		// If it looks like the template uses <tool_call> tags,
		// then we can probably use the default tool config which expects tool calls
		// between <tool_call> and </tool_call> tags.
		if strings.Contains(templateString, "<tool_call>") {
			supportsToolsNatively = true
			toolParseConfig = tools.NewDeepSeekParseConfig()
		}

		// TODO(mway): https://github.com/NVIDIA/nvcf/llm-api-gateway/pull/3988
		switch d.Name() {
		case "minimax-m2":
			reasoningConfig = output.XMLThinkWithPrefillReasoningConfig()
			supportsToolsNatively = true
			toolParseConfig = tools.NewMinimaxM2ParseConfig()
		case "qwen3-235b-a22b":
			reasoningConfig = output.XMLThinkReasoningConfig()
		case "glm-4.5":
			supportsToolsNatively = true
			toolParseConfig = tools.NewGLMParseConfig()
			reasoningConfig = output.XMLThinkReasoningConfig()
		case "pixtral-12b":
			// TODO(mway): Const this and related ad hoc tokens
			imageToken = "[IMG]"
		case "qwen-2.5-vl":
			imageToken = "<|image_pad|>"
		case "kimi-k2-instruct":
			supportsToolsNatively = true
			toolParseConfig = tools.NewKimiParseConfig()
			preProcessMessages = tools.ConvertToolIDsToKimiID
		case "kimi-k2-thinking":
			supportsToolsNatively = true
			toolParseConfig = tools.NewKimiParseConfig()
			reasoningConfig = output.XMLThinkReasoningConfig()
			preProcessMessages = tools.ConvertToolIDsToKimiID
		default:
			// No specialization
		}

		err = e.registerJinjaTemplate(
			d.Name(),
			templateString,
			toolUseTemplate,
			prompt.JinjaParams{
				ReasoningConfig:            reasoningConfig,
				ToolParseConfig:            toolParseConfig,
				FinalRole:                  finalRole,
				RelaxedMessageOrdering:     relaxedMessageOrdering,
				PreserveSystemMessageOrder: preserveSystemMessageOrder,
				BOSToken:                   bosToken,
				EOSToken:                   eosToken,
				ImageToken:                 imageToken,
				SupportsSystemPrompts:      true,
				SupportsToolsNatively:      supportsToolsNatively,
				PreProcessMessages:         preProcessMessages,
				DropTokens:                 dropTokens,
				PreserveToolContentArray:   preserveToolContentArray,
				PreserveSystemContentArray: preserveSystemContentArray,
				OrderToolParameters:        orderToolParameters,
			},
		)
		if err != nil {
			return fmt.Errorf(
				"failed to create contextualized template: %w",
				err,
			)
		}

		x, ok := e.templates[d.Name()]
		if !ok {
			panic(fmt.Sprintf(
				"bug: template %q not present after registering",
				d.Name(),
			))
		}
		tmpl := must.As[prompt.TextTemplate](x)

		// Probe the newly-added template for system prompt support. First, we
		// attempt to execute the template as-is (with system prompts enabled).
		_, err = tmpl.RenderText([]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("foobar"),
			},
			prompt.NewEmptyUserMessage(),
		}, tools.Params{})
		if err != nil {
			log.Info().
				Str("template", d.Name()).
				Msg("template does not support system prompts")

			// We failed to execute the template with system prompts enabled,
			// so now we disable it to see if template execution succeeds.
			must.As[*prompt.Jinja](tmpl).Params().SupportsSystemPrompts = false
			_, err = tmpl.RenderText([]models.ChatMessage{
				{
					Role:    models.ChatCompletionRoleSystem,
					Content: models.SingleTextContent("foobar"),
				},
				prompt.NewEmptyUserMessage(),
			}, tools.Params{})
			if err != nil {
				return fmt.Errorf("failed to execute jinja template: %w", err)
			}

			log.Info().
				Str("template", d.Name()).
				Msg("successfully executed template with system prompt disabled")
		}
	}

	return nil
}

func (e *Engine) getTemplateBytes(name []byte) (prompt.Template, error) {
	// For backwards compatibility, trim quotes from the name.
	name = bytes.Trim(name, `"`)

	e.mu.RLock()
	defer e.mu.RUnlock()

	if x, ok := e.templates[string(name)]; ok {
		return x, nil
	}

	return nil, ErrTemplateNotExist
}

func (e *Engine) registerJinjaTemplate(
	name string,
	tmpl string,
	toolUseTmpl *string,
	params prompt.JinjaParams,
) error {
	if err := e.env.AddTemplate(name, tmpl); err != nil {
		return fmt.Errorf(
			"failed to register new minijinja template %q: %w",
			name,
			err,
		)
	}

	params.Hash = prompt.HashTemplate(tmpl)

	var toolUseTemplate *minijinja.Template
	if toolUseTmpl != nil {
		toolUseTemplateName := name + "_tool_use"
		if err := e.env.AddTemplate(toolUseTemplateName, ptr.Deref(toolUseTmpl)); err != nil {
			return fmt.Errorf(
				"failed to register new minijinja tool_use template %q: %w",
				toolUseTemplateName,
				err,
			)
		}
		tmpl := must.True(e.env.Template(toolUseTemplateName))
		toolUseTemplate = &tmpl
	}

	x, err := prompt.NewJinja(
		must.True(e.env.Template(name)),
		toolUseTemplate,
		params,
	)
	if err != nil {
		return fmt.Errorf(
			"failed to wrap minijinja template %q: %w",
			name,
			err,
		)
	}

	return e.registerTemplate(name, x)
}

func (e *Engine) registerTemplate(
	name string,
	tmpl prompt.Template,
) error {
	// We want to abstract the *type* of template (string- or token-based) but
	// have distinct interfaces (with a shared base). So, we accept the base,
	// but assert that it also satisfies one of the broader interfaces that all
	// templates are expected to implement.
	if _, ok := tmpl.(prompt.TextTemplate); !ok {
		if _, ok = tmpl.(prompt.TokenizedTemplate); !ok {
			return fmt.Errorf(
				"bug: %w: unsupported %T implementation %T",
				ErrTemplateUnexpectedType,
				prompt.Template(nil),
				tmpl,
			)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	zlog.Info().
		Str("name", name).
		Str("type", fmt.Sprintf("%T", tmpl)).
		Msg("registering template")
	e.templates[name] = tmpl

	return nil
}

func (e *Engine) registerOptionalTemplate(
	name string,
	factory func() prompt.Template,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			zlog.Warn().
				Str("name", name).
				Interface("panic", recovered).
				Msg("skipping optional template registration")
			err = nil
		}
	}()

	return e.registerTemplate(name, factory())
}

func getTemplateBytes[T prompt.Template](e *Engine, name []byte) (T, error) {
	var zero T

	x, err := e.getTemplateBytes(name)
	if err != nil {
		return zero, err
	}

	if tmpl, ok := x.(T); ok {
		return tmpl, nil
	}

	return zero, fmt.Errorf(
		"%w: wanted %T, got %T",
		ErrTemplateUnexpectedType,
		zero,
		x,
	)
}
