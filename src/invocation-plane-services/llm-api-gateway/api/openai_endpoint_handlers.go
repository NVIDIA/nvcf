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
	"net/http"
	"slices"
	"strings"
	"unicode"

	echo "github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	templatingprompt "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/templating/prompt"
)

const (
	MaxEmbeddingInputs = 2048
	MaxRerankingDocs   = 100
	ttsMaxInputLength  = 10000
	orpheusIdentifier  = "orpheus"
)

var (
	playAIArabicVoices = []string{
		"Nasser-PlayAI",
		"Khalid-PlayAI",
		"Amira-PlayAI",
		"Ahmad-PlayAI",
	}
	playAIEnglishVoices = []string{
		"Aaliyah-PlayAI",
		"Adelaide-PlayAI",
		"Angelo-PlayAI",
		"Arista-PlayAI",
		"Atlas-PlayAI",
		"Basil-PlayAI",
		"Briggs-PlayAI",
		"Calum-PlayAI",
		"Celeste-PlayAI",
		"Cheyenne-PlayAI",
		"Chip-PlayAI",
		"Cillian-PlayAI",
		"Deedee-PlayAI",
		"Eleanor-PlayAI",
		"Fritz-PlayAI",
		"Gail-PlayAI",
		"Indigo-PlayAI",
		"Jennifer-PlayAI",
		"Judy-PlayAI",
		"Mamaw-PlayAI",
		"Mason-PlayAI",
		"Mikail-PlayAI",
		"Mitch-PlayAI",
		"Nia-PlayAI",
		"Quinn-PlayAI",
		"Ruby-PlayAI",
		"Thunder-PlayAI",
	}
	orpheusArabicVoices = []string{
		"fahad",
		"sultan",
		"noura",
		"lulwa",
		"aisha",
	}
	orpheusEnglishVoices = []string{
		"autumn",
		"diana",
		"hannah",
		"austin",
		"daniel",
		"troy",
	}
	defaultTTSCapabilitiesByModelID = map[string]config.TextToSpeechCapabilities{
		"playai-tts": {
			SampleRates:        models.SupportedTTSSampleRates,
			Voices:             playAIEnglishVoices,
			ResponseFormats:    models.SupportedTTSResponseFormats,
			MinSpeed:           0.5,
			MaxSpeed:           5.0,
			DefaultTemperature: ptr.To(float32(0.95)),
			UnsupportedFormatsByRate: map[uint32][]string{
				8000: {models.AudioFormatOgg},
			},
			MaxInputLength: ttsMaxInputLength,
		},
		"playai-tts-arabic": {
			SampleRates:        models.SupportedTTSSampleRates,
			Voices:             playAIArabicVoices,
			ResponseFormats:    models.SupportedTTSResponseFormats,
			MinSpeed:           0.5,
			MaxSpeed:           5.0,
			DefaultTemperature: ptr.To(float32(1.05)),
			UnsupportedFormatsByRate: map[uint32][]string{
				8000: {models.AudioFormatOgg},
			},
			MaxInputLength: ttsMaxInputLength,
		},
		"canopylabs/orpheus-arabic-saudi": {
			SampleRates:        models.SupportedTTSSampleRates,
			Voices:             orpheusArabicVoices,
			ResponseFormats:    []string{models.AudioFormatWav},
			MinSpeed:           0.5,
			MaxSpeed:           5.0,
			DefaultSampleRate:  ptr.To(uint32(24000)),
			DefaultTemperature: ptr.To(float32(0.6)),
			MaxInputLength:     ttsMaxInputLength,
		},
		"canopylabs/orpheus-v1-english": {
			SampleRates:        models.SupportedTTSSampleRates,
			Voices:             orpheusEnglishVoices,
			ResponseFormats:    []string{models.AudioFormatWav},
			MinSpeed:           0.5,
			MaxSpeed:           5.0,
			DefaultSampleRate:  ptr.To(uint32(24000)),
			DefaultTemperature: ptr.To(float32(0.6)),
			MaxInputLength:     ttsMaxInputLength,
		},
		orpheusIdentifier: {
			SampleRates:        models.SupportedTTSSampleRates,
			ResponseFormats:    []string{models.AudioFormatWav},
			MinSpeed:           0.5,
			MaxSpeed:           5.0,
			DefaultSampleRate:  ptr.To(uint32(24000)),
			DefaultTemperature: ptr.To(float32(0.6)),
			MaxInputLength:     ttsMaxInputLength,
		},
	}
)

func (h *OpenAIProxyHandlers) Embeddings(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var request models.CreateEmbeddingRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requestModel, err := h.validateEmbeddingRequest(reqCtx, &request)
	if err != nil {
		return err
	}

	inputTokens := countEmbeddingTokens(&request)
	if err := h.admitProxyRequest(c, reqCtx, inputTokens, 0); err != nil {
		return err
	}

	span := trace.SpanFromContext(c.UserContext())
	span.AddEvent("handlers_embeddings.go:Embeddings()")
	span.SetAttributes(
		attribute.String("gateway.openai.endpoint", "embeddings"),
		attribute.String("gen_ai.request.model", requestModel),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.Int("gen_ai.request.input_count", len(request.Input)),
		attribute.Int("gen_ai.usage.input_tokens", inputTokens),
	)

	return h.forwardJSONRequest(c, reqCtx, rawBody, inputTokens)
}

func (h *OpenAIProxyHandlers) Reranking(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var request models.RerankingRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requestModel, err := h.validateRerankingRequest(reqCtx, &request)
	if err != nil {
		return err
	}

	inputTokens := countRerankingTokens(&request)
	if err := h.admitProxyRequest(c, reqCtx, inputTokens, 0); err != nil {
		return err
	}

	span := trace.SpanFromContext(c.UserContext())
	span.AddEvent("handlers_reranking.go:Reranking()")
	span.SetAttributes(
		attribute.String("gateway.openai.endpoint", "reranking"),
		attribute.String("gen_ai.request.model", requestModel),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.Int("gen_ai.request.input_count", len(request.Docs)),
		attribute.Int("gen_ai.usage.input_tokens", inputTokens),
		attribute.Int("gateway.reranking.query_length", len(request.Query)),
		attribute.Int("gateway.reranking.instruction_length", len(ptr.Deref(request.Instruction))),
	)

	return h.forwardJSONRequest(c, reqCtx, rawBody, inputTokens)
}

func (h *OpenAIProxyHandlers) TextToSpeech(ec echo.Context) error {
	c := must.As[*GatewayContext](ec)
	reqCtx, err := h.requireFunctionRequestContext(c)
	if err != nil {
		return err
	}

	rawBody, err := captureRequestBody(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var request models.TextToSpeechRequest
	if err := c.Bind(&request); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	requestModel, err := h.validateTextToSpeechRequest(reqCtx, &request)
	if err != nil {
		return err
	}

	inputTokens := max(request.InputTokens(), estimatedTokenCountForText(request.Input))
	if err := h.admitProxyRequest(c, reqCtx, inputTokens, 0); err != nil {
		return err
	}

	span := trace.SpanFromContext(c.UserContext())
	span.AddEvent("handlers_tts.go:TextToSpeech()")
	span.SetAttributes(
		attribute.String("gateway.openai.endpoint", "audio.speech"),
		attribute.String("gen_ai.request.model", requestModel),
		attribute.String("gateway.routed_model", reqCtx.Model),
		attribute.Int("gen_ai.request.prompt_length", request.InputTokens()),
		attribute.String("gateway.audio.voice", request.Voice),
		attribute.String("gateway.audio.response_format", request.GetResponseFormat()),
		attribute.Float64("gateway.audio.speed", float64(request.GetSpeed())),
		attribute.Int("gateway.audio.sample_rate", int(request.GetSampleRate())),
	)

	outboundBody, err := rewriteTextToSpeechRequestBody(rawBody, &request, reqCtx.Model)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	resp, err := h.dispatchProxyRequest(
		c,
		reqCtx,
		c.Request().Header.Clone(),
		io.NopCloser(bytes.NewReader(outboundBody)),
		int64(len(outboundBody)),
		inputTokens,
	)
	if err != nil {
		return err
	}
	if resp == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "proxy provider returned no response")
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	upstreamContentType := strings.ToLower(resp.Header.Get(echo.HeaderContentType))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices &&
		(upstreamContentType == "" || strings.HasPrefix(upstreamContentType, "text/plain")) {
		if contentType := models.ContentTypeMap[request.GetResponseFormat()]; contentType != "" {
			resp.Header.Set(echo.HeaderContentType, contentType)
		}
	}
	return writeProxyResponse(c, resp)
}

func (h *OpenAIProxyHandlers) validateEmbeddingRequest(
	reqCtx *requestctx.RequestContext,
	request *models.CreateEmbeddingRequest,
) (string, error) {
	requestModel, err := h.validateAndSetInvocationModel(reqCtx, request.Model)
	if err != nil {
		return "", err
	}
	request.Model = reqCtx.Model
	if !h.modelCapabilities(reqCtx.Model).SupportsEmbeddings() {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("the model `%s` does not support embeddings", requestModel),
		)
	}
	if len(request.Input) == 0 {
		return "", echo.NewHTTPError(http.StatusBadRequest, "'input' array must not be empty")
	}
	if len(request.Input) > MaxEmbeddingInputs {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("'input' array is too large, must be less than %d elements", MaxEmbeddingInputs),
		)
	}
	for idx, input := range request.Input {
		if len(input) == 0 {
			return "", echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("'input.%d' must not be empty", idx),
			)
		}
	}
	return requestModel, nil
}

func (h *OpenAIProxyHandlers) validateRerankingRequest(
	reqCtx *requestctx.RequestContext,
	request *models.RerankingRequest,
) (string, error) {
	requestModel, err := h.validateAndSetModel(reqCtx, request.Model)
	if err != nil {
		return "", err
	}
	request.Model = reqCtx.Model
	if !h.modelCapabilities(reqCtx.Model).SupportsReranking() {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("the model `%s` does not support reranking", requestModel),
		)
	}
	switch {
	case request.Query == "":
		return "", echo.NewHTTPError(http.StatusBadRequest, "'query' must not be empty")
	case len(request.Docs) == 0:
		return "", echo.NewHTTPError(http.StatusBadRequest, "'docs' array must not be empty")
	case len(request.Docs) > MaxRerankingDocs:
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("'docs' array is too large, must be less than %d elements", MaxRerankingDocs),
		)
	}
	for idx, doc := range request.Docs {
		if len(doc) == 0 {
			return "", echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("'docs[%d]' must not be empty", idx),
			)
		}
	}
	return requestModel, nil
}

func (h *OpenAIProxyHandlers) validateTextToSpeechRequest(
	reqCtx *requestctx.RequestContext,
	request *models.TextToSpeechRequest,
) (string, error) {
	requestModel, err := h.validateAndSetModel(reqCtx, request.Model)
	if err != nil {
		return "", err
	}
	request.Model = reqCtx.Model
	if !h.modelCapabilities(reqCtx.Model).SupportsTextToSpeech() {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("the model `%s` does not support text to speech", requestModel),
		)
	}

	ttsCfg := effectiveTTSCapabilities(reqCtx.Model)
	if request.Input == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "input is required")
	}
	if request.Input == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "Input must contain non-whitespace characters")
	}

	var hasLetterOrDigit bool
	for _, r := range request.Input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasLetterOrDigit = true
			break
		}
	}
	if !hasLetterOrDigit {
		return "", echo.NewHTTPError(http.StatusBadRequest, "Input must contain at least one letter or digit")
	}

	if request.Voice == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "voice is required")
	}

	if request.SampleRate == nil && ttsCfg.DefaultSampleRate != nil {
		request.SampleRate = ptr.To(ptr.Deref(ttsCfg.DefaultSampleRate))
	}
	if request.Temperature == nil && ttsCfg.DefaultTemperature != nil {
		request.Temperature = ptr.To(ptr.Deref(ttsCfg.DefaultTemperature))
	}

	inputLength := request.InputTokens()
	maxInputLength := ttsCfg.MaxInputLength
	if maxInputLength <= 0 {
		maxInputLength = ttsMaxInputLength
	}
	if inputLength > maxInputLength {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("Input must be less than %d characters", maxInputLength),
		)
	}

	if len(ttsCfg.Voices) > 0 && !slices.Contains(ttsCfg.Voices, request.Voice) {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("voice must be one of the following voices: %v", ttsCfg.Voices),
		)
	}

	sampleRates := ttsCfg.SampleRates
	if len(sampleRates) == 0 {
		sampleRates = models.SupportedTTSSampleRates
	}
	if !slices.Contains(sampleRates, request.GetSampleRate()) {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("sample_rate must be one of %v", sampleRates),
		)
	}

	minSpeed := ttsCfg.MinSpeed
	if minSpeed == 0 {
		minSpeed = 0.5
	}
	maxSpeed := ttsCfg.MaxSpeed
	if maxSpeed == 0 {
		maxSpeed = 5.0
	}
	switch speed := request.GetSpeed(); {
	case speed > maxSpeed:
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("speed must be at most %.1f", maxSpeed),
		)
	case speed < minSpeed:
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("speed must be at least %.1f", minSpeed),
		)
	}

	responseFormats := ttsCfg.ResponseFormats
	if len(responseFormats) == 0 {
		responseFormats = models.SupportedTTSResponseFormats
	}
	if !slices.Contains(responseFormats, request.GetResponseFormat()) {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("response_format must be one of %v", responseFormats),
		)
	}
	if unsupported := ttsCfg.UnsupportedFormatsByRate[request.GetSampleRate()]; len(unsupported) > 0 &&
		slices.Contains(unsupported, request.GetResponseFormat()) {
		return "", echo.NewHTTPError(
			http.StatusBadRequest,
			"Unsupported combination of sample_rate and response_format",
		)
	}

	return requestModel, nil
}

// TODO: we probably don't need this anymore because we are not supporting models endpoints
func (h *OpenAIProxyHandlers) validateAndSetModel(
	reqCtx *requestctx.RequestContext,
	model string,
) (string, error) {
	requestModel := model
	if requestModel == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "model is required")
	}
	reqCtx.Model = requestModel
	return requestModel, nil
}

func (h *OpenAIProxyHandlers) validateAndSetInvocationModel(
	reqCtx *requestctx.RequestContext,
	model string,
) (string, error) {
	requestModel := model
	routedModel, err := normalizeOpenAIRequestModel(reqCtx, requestModel)
	if err != nil {
		return "", err
	}
	reqCtx.Model = routedModel
	setRoutingMethodForModel(reqCtx, routedModel)
	return requestModel, nil
}

func (h *OpenAIProxyHandlers) modelCapabilities(model string) config.ModelCapabilities {
	if h == nil || h.handlers == nil || h.handlers.config == nil {
		return config.ModelCapabilities{}
	}
	if caps, ok := h.handlers.config.ModelCapabilities[model]; ok {
		return caps
	}
	return config.ModelCapabilities{}
}

func effectiveTTSCapabilities(model string) config.TextToSpeechCapabilities {
	if cfg, ok := defaultTTSCapabilitiesByModelID[model]; ok {
		return cfg
	}
	if strings.Contains(model, orpheusIdentifier) {
		return defaultTTSCapabilitiesByModelID[orpheusIdentifier]
	}
	return config.TextToSpeechCapabilities{
		SampleRates:     models.SupportedTTSSampleRates,
		ResponseFormats: models.SupportedTTSResponseFormats,
		MinSpeed:        0.5,
		MaxSpeed:        5.0,
		MaxInputLength:  ttsMaxInputLength,
	}
}

func countEmbeddingTokens(
	request *models.CreateEmbeddingRequest,
) int {
	total := 0
	for _, input := range request.Input {
		total += estimatedTokenCountForText(input)
	}
	return total
}

func countRerankingTokens(
	request *models.RerankingRequest,
) int {
	total := estimatedTokenCountForText(
		templatingprompt.TemplateQuery(ptr.Deref(request.Instruction), request.Query),
	)
	for _, doc := range request.Docs {
		total += estimatedTokenCountForText(templatingprompt.TemplateDocument(doc))
	}
	return total
}

func (h *OpenAIProxyHandlers) admitProxyRequest(
	c *GatewayContext,
	reqCtx *requestctx.RequestContext,
	inputTokens int,
	outputTokens int,
) error {
	checkRequest := ratelimit.ResourceRequest{
		Requests:     1,
		InputTokens:  int64(max(0, inputTokens)),
		OutputTokens: int64(max(0, outputTokens)),
	}

	plan, err := NewAdmissionPlan(
		c,
		reqCtx,
		c.Request().URL.Path,
		h.handlers.limitResolver,
		h.handlers.rateLimiter,
		checkRequest,
		checkRequest,
	)
	if err != nil {
		return err
	}
	if plan == nil {
		return nil
	}
	defer plan.Close()

	if err := plan.CheckRequests(c.UserContext()); err != nil {
		return err
	}
	_, err = plan.CheckTokensAndFinalize(c.UserContext())
	return err
}

func captureRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return body, nil
}

func rewriteTextToSpeechRequestBody(
	body []byte,
	request *models.TextToSpeechRequest,
	model string,
) ([]byte, error) {
	payload := map[string]any{}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("unmarshal tts request: %w", err)
		}
	}

	payload["model"] = model
	if request != nil {
		payload["input"] = request.Input
		payload["voice"] = request.Voice
		payload["speed"] = request.GetSpeed()
		payload["response_format"] = request.GetResponseFormat()
		payload["sample_rate"] = request.GetSampleRate()
		if request.Temperature != nil {
			payload["temperature"] = request.GetTemperature()
		}
	}

	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal tts request: %w", err)
	}
	return rewritten, nil
}
