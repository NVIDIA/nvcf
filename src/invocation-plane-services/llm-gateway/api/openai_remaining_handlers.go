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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/tokenizers"
)

const (
	chatCompletionsEndpoint     = "/v1/chat/completions"
	audioTranscriptionsEndpoint = "/v1/audio/transcriptions"
	audioTranslationsEndpoint   = "/v1/audio/translations"
)

type speechToSpeechRequest struct {
	STT models.TranscriptionParams   `json:"stt"`
	LLM models.ChatCompletionRequest `json:"llm"`
	TTS models.TextToSpeechRequest   `json:"tts"`
}

func (h *OpenAIProxyHandlers) ChatCompletionsTemplate(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	var request models.ChatCompletionRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requestModel, err := h.validateAndSetModel(reqCtx, request.Model)
	if err != nil {
		return err
	}
	request.Model = reqCtx.Model
	if !h.modelCapabilities(reqCtx.Model).SupportsChatTemplate() {
		return echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("the model `%s` does not support template rendering", requestModel),
		)
	}

	if request.Messages == nil || len(*request.Messages) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "messages is required")
	}

	renderedPrompt, err := h.handlers.renderPromptFromContext(reqCtx, &request)
	if err != nil {
		return err
	}
	if ptr.Deref(renderedPrompt) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "template is not configured for this function")
	}

	tokenizerModel := h.resolveTokenizer(reqCtx.Model, reqCtx)

	var (
		tokenIDs []uint32
		tokens   []string
	)
	if h.handlers.tokenizers == nil || tokenizerModel == "" {
		return echo.NewHTTPError(http.StatusNotImplemented, "tokenizer store is not configured")
	}

	tokenizer, err := h.handlers.tokenizers.TokenizerForModel(tokenizerModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	tokenIDs, err = tokenizer.Encode(ptr.Deref(renderedPrompt))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	tokens = make([]string, 0, len(tokenIDs))
	for _, tokenID := range tokenIDs {
		decoded, err := tokenizer.Decode([]uint32{tokenID}, false)
		switch {
		case err == nil:
			tokens = append(tokens, decoded)
		case err == tokenizers.ErrIncompleteUTF8Character:
			tokens = append(tokens, "�")
		default:
			return echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("failed to decode token %d: %s", tokenID, err.Error()),
			)
		}
	}

	span := trace.SpanFromContext(c.UserContext())
	span.AddEvent("handlers_chat.go:ChatCompletionsTemplate()")
	span.SetAttributes(
		attribute.String("gateway.openai.endpoint", "chat.completions.template"),
		attribute.String("gen_ai.request.model", requestModel),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.Int("gen_ai.usage.input_tokens", len(tokenIDs)),
	)

	if err := h.admitProxyRequest(c, reqCtx, len(tokenIDs), 0); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, map[string]any{
		"prompt":    ptr.Deref(renderedPrompt),
		"tokens":    tokens,
		"token_ids": tokenIDs,
	})
}

func (h *OpenAIProxyHandlers) Transcriptions(ec echo.Context) error {
	return h.handleAudioToTextProxy(ec, false)
}

func (h *OpenAIProxyHandlers) Translations(ec echo.Context) error {
	return h.handleAudioToTextProxy(ec, true)
}

func (h *OpenAIProxyHandlers) handleAudioToTextProxy(ec echo.Context, translation bool) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	form, err := c.MultipartForm()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	defer cleanupMultipartForm(form)

	var requestModel string

	if translation {
		var params models.TranslationParams
		if err := params.Parse(form); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		requestModel, err = h.validateAndSetModel(reqCtx, ptr.Deref(params.Model))
		if err != nil {
			return err
		}
		if !h.modelCapabilities(reqCtx.Model).SupportsTranslation() {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("the model `%s` does not support audio translations", requestModel),
			)
		}

		span := trace.SpanFromContext(c.UserContext())
		span.AddEvent("handlers_audio.go:Translations()")
		span.SetAttributes(
			attribute.String("gateway.openai.endpoint", "audio.translations"),
			attribute.String("gen_ai.request.model", requestModel),
			attribute.String("gateway.routed_model", reqCtx.Model),
			attribute.String("gateway.audio.response_format", string(params.GetResponseFormatOrDefault())),
			attribute.Bool("gateway.audio.has_file", params.HasFile()),
			attribute.Bool("gateway.audio.has_url", params.URL != nil),
		)
	} else {
		var params models.TranscriptionParams
		if err := params.Parse(form); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		requestModel, err = h.validateAndSetModel(reqCtx, ptr.Deref(params.Model))
		if err != nil {
			return err
		}
		if !h.modelCapabilities(reqCtx.Model).SupportsTranscription() {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("the model `%s` does not support audio transcriptions", requestModel),
			)
		}

		span := trace.SpanFromContext(c.UserContext())
		span.AddEvent("handlers_audio.go:Transcriptions()")
		span.SetAttributes(
			attribute.String("gateway.openai.endpoint", "audio.transcriptions"),
			attribute.String("gen_ai.request.model", requestModel),
			attribute.String("gateway.routed_model", reqCtx.Model),
			attribute.String("gateway.audio.response_format", string(params.GetResponseFormatOrDefault())),
			attribute.Bool("gateway.audio.word_timestamps", params.WordTimestamps),
			attribute.Bool("gateway.audio.segment_timestamps", params.SegmentTimestamps),
			attribute.Bool("gateway.audio.diarize", params.Diarize),
			attribute.Bool("gateway.audio.has_file", params.HasFile()),
			attribute.Bool("gateway.audio.has_url", params.URL != nil),
		)
	}

	if err := h.admitProxyRequest(c, reqCtx, 0, 0); err != nil {
		return err
	}

	outboundBody := rawBody
	outboundContentType := c.Request().Header.Get(echo.HeaderContentType)
	if reqCtx.Model != "" {
		outboundBody, outboundContentType, err = rewriteMultipartRequestModel(
			rawBody,
			outboundContentType,
			reqCtx.Model,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}

	headers := c.Request().Header.Clone()
	if outboundContentType != "" {
		headers.Set(echo.HeaderContentType, outboundContentType)
	}

	resp, err := h.dispatchProxyRequest(
		c,
		reqCtx,
		headers,
		io.NopCloser(bytes.NewReader(outboundBody)),
		int64(len(outboundBody)),
	)
	if err != nil {
		return err
	}
	return writeProxyResponse(c, resp)
}

func (h *OpenAIProxyHandlers) SpeechToSpeech(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	form, err := c.MultipartForm()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	defer cleanupMultipartForm(form)

	if len(form.File[models.TranscriptionRequestFile]) != 1 {
		return echo.NewHTTPError(
			http.StatusBadRequest,
			"`file` must contain exactly one multipart file",
		)
	}

	params, err := parseSpeechToSpeechRequest(form)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requestModel := params.LLM.Model
	routedModel, err := normalizeOpenAIRequestModel(reqCtx, requestModel)
	if err != nil {
		return err
	}
	reqCtx.Model = routedModel
	params.LLM.Model = routedModel
	if !h.modelCapabilities(reqCtx.Model).SupportsSpeechToSpeech() {
		return echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("the model `%s` does not support speech to speech", reqCtx.Model),
		)
	}

	span := trace.SpanFromContext(c.UserContext())
	span.AddEvent("handlers_sts.go:SpeechToSpeech()")
	span.SetAttributes(
		attribute.String("gateway.openai.endpoint", "audio.x.sts"),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.String("gateway.sts.llm_model", requestModel),
		attribute.String("gateway.sts.stt_model", ptr.Deref(params.STT.Model)),
		attribute.String("gateway.sts.tts_model", params.TTS.Model),
		attribute.String("gateway.audio.voice", params.TTS.Voice),
	)

	if err := h.admitProxyRequest(c, reqCtx, 0, 0); err != nil {
		return err
	}

	outboundBody, outboundContentType, err := rewriteMultipartRequestModel(
		rawBody,
		c.Request().Header.Get(echo.HeaderContentType),
		reqCtx.Model,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	headers := c.Request().Header.Clone()
	if outboundContentType != "" {
		headers.Set(echo.HeaderContentType, outboundContentType)
	}

	resp, err := h.dispatchProxyRequest(
		c,
		reqCtx,
		headers,
		io.NopCloser(bytes.NewReader(outboundBody)),
		int64(len(outboundBody)),
	)
	if err != nil {
		return err
	}
	return writeProxyResponse(c, resp)
}

func cleanupMultipartForm(form *multipart.Form) {
	if form != nil {
		_ = form.RemoveAll()
	}
}

func defaultSpeechToSpeechRequest() speechToSpeechRequest {
	return speechToSpeechRequest{
		STT: models.TranscriptionParams{
			AudioToTextBaseParams: models.AudioToTextBaseParams{
				Model:       ptr.To("whisper-large-v3-turbo"),
				Language:    ptr.To("en"),
				Temperature: ptr.To(float32(1.0)),
			},
			SegmentTimestamps: true,
			WordTimestamps:    true,
		},
		LLM: models.ChatCompletionRequest{
			Model:       "compound-beta",
			Temperature: ptr.To(float32(1.0)),
		},
		TTS: models.TextToSpeechRequest{
			Model: "playai-tts",
			Voice: "Aaliyah-PlayAI",
		},
	}
}

func parseSpeechToSpeechRequest(form *multipart.Form) (*speechToSpeechRequest, error) {
	params := defaultSpeechToSpeechRequest()
	if form == nil {
		return &params, nil
	}

	for _, key := range []string{"stt", "llm", "tts"} {
		values := form.Value[key]
		if len(values) > 1 {
			return nil, fmt.Errorf("only one %s field is allowed", key)
		}
	}

	if value := form.Value["stt"]; len(value) == 1 && value[0] != "" {
		if err := json.Unmarshal([]byte(value[0]), &params.STT); err != nil {
			return nil, fmt.Errorf("failed to unmarshal stt params: %w", err)
		}
	}
	if value := form.Value["llm"]; len(value) == 1 && value[0] != "" {
		if err := json.Unmarshal([]byte(value[0]), &params.LLM); err != nil {
			return nil, fmt.Errorf("failed to unmarshal llm params: %w", err)
		}
	}
	if value := form.Value["tts"]; len(value) == 1 && value[0] != "" {
		if err := json.Unmarshal([]byte(value[0]), &params.TTS); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tts params: %w", err)
		}
	}

	params.STT.SetFile(form.File[models.TranscriptionRequestFile][0])
	return &params, nil
}
