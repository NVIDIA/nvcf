/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	config "ai-api-gateway-service/gateway_config"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"github.com/goccy/go-json"
	"github.com/valyala/bytebufferpool"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/exp/maps"
)

type contextKey int

const tooManyRequestsKey contextKey = iota

var errPrimaryProxyPanic = errors.New("primary proxy panicked")

const (
	contentTypeHeader            = "Content-Type"
	contentTypeApplicationJSON   = "application/json"
	contentTypeMultipartFormData = "multipart/form-data"
)

type OpenAIDirector struct {
	chatCompletions  ModelMapping
	completions      ModelMapping
	embeddings       ModelMapping
	responses        ModelMapping
	imageGenerations ModelMapping
	imageEdits       ModelMapping
	imageVariations  ModelMapping
	allPublicModels  []ModelInfo // list of models used for the /v1/models
	vanityDirector   *VanityDirector
	shadower         *TrafficShadower

	// Cache for filtered models
	filteredModelsCache        []ModelInfo
	filteredModelsLastComputed time.Time
	filteredModelsMutex        sync.RWMutex
}

type CustomOpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"` // required, but unused field
	Code    string `json:"code"`
}

// OpenAIError represents the structure of the custom error message.
type OpenAIError struct {
	Error CustomOpenAIError `json:"error"`
}

type FunctionInfo struct {
	functionId                     string
	functionVersionId              string
	pathOverride                   *string
	usePexec                       bool
	eol                            time.Time
	offlineMessage                 string
	tooManyRequestsMessage         string
	shadowModelNames               []string
	shadowPercentage               int
	shadowCancelOnClientDisconnect bool
}

type ModelMapping struct {
	modelNameToNVCFUrl   map[string]FunctionInfo
	modelNameToModelInfo map[string]ModelInfo // only contains public models
}

type ModelInfo struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

type ModelNameToFunctionIdVersionId struct {
	FunctionId                     string
	FunctionVersionId              string
	OutgoingPathOverride           string
	UsePexec                       bool
	EOL                            time.Time
	OfflineMessage                 string
	TooManyRequestsMessage         string
	ShadowModelNames               []string
	ShadowPercentage               *int
	ShadowCancelOnClientDisconnect bool
}

type openAIRequestBody struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type resolvedOpenAIRequest struct {
	request      *http.Request
	functionInfo FunctionInfo
}

type primaryProxyObserver struct {
	mu            sync.Mutex
	wroteResponse bool
	statusCode    int
	writeErr      error
}

func (o *primaryProxyObserver) wrap(writer http.ResponseWriter) http.ResponseWriter {
	return httpsnoop.Wrap(writer, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				o.markStatus(code)
				next(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(body []byte) (int, error) {
				o.markStatus(http.StatusOK)
				n, err := next(body)
				o.recordWriteError(err)
				return n, err
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(src io.Reader) (int64, error) {
				o.markStatus(http.StatusOK)
				n, err := next(src)
				o.recordWriteError(err)
				return n, err
			}
		},
	})
}

func (o *primaryProxyObserver) markStatus(code int) {
	if code >= 100 && code <= 199 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.wroteResponse {
		o.statusCode = code
	}
	o.wroteResponse = true
}

func (o *primaryProxyObserver) recordWriteError(err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.writeErr == nil {
		o.writeErr = err
	}
}

func (o *primaryProxyObserver) shadowFinalizationError(req *http.Request, proxyErr error) error {
	if proxyErr != nil {
		return proxyErr
	}

	o.mu.Lock()
	wroteResponse := o.wroteResponse
	writeErr := o.writeErr
	o.mu.Unlock()

	if writeErr != nil {
		return writeErr
	}
	if req.Context().Err() != nil && !wroteResponse {
		return req.Context().Err()
	}
	return nil
}

func buildModelMapping(
	modelNameToFunctionIdVersionId map[string]ModelNameToFunctionIdVersionId,
	privateModelMatcher *regexp.Regexp) (ModelMapping, error) {

	modelNameToModelInfo := make(map[string]ModelInfo, len(modelNameToFunctionIdVersionId))
	modelNameToNVCFUrl := make(map[string]FunctionInfo, len(modelNameToFunctionIdVersionId))
	// build the mapping for models to nvcf pexec url
	for modelName, entry := range modelNameToFunctionIdVersionId {
		var pathOverride *string
		if entry.OutgoingPathOverride != "" {
			pathOverride = &entry.OutgoingPathOverride
		}
		modelNameToNVCFUrl[modelName] = FunctionInfo{
			functionId:                     entry.FunctionId,
			functionVersionId:              entry.FunctionVersionId,
			pathOverride:                   pathOverride,
			usePexec:                       entry.UsePexec,
			eol:                            entry.EOL,
			offlineMessage:                 entry.OfflineMessage,
			tooManyRequestsMessage:         entry.TooManyRequestsMessage,
			shadowModelNames:               entry.ShadowModelNames,
			shadowPercentage:               defaultShadowPercentage(entry.ShadowPercentage),
			shadowCancelOnClientDisconnect: entry.ShadowCancelOnClientDisconnect,
		}

		// build the modelInfo list and modelName to modelInfo map
		if !privateModelMatcher.MatchString(modelName) {
			modelInfo := ModelInfo{
				Id:      modelName,
				Object:  "model",
				Created: 735790403, // filler (year NVIDIA was founded)
				OwnedBy: strings.Split(modelName, "/")[0],
			}
			modelNameToModelInfo[modelName] = modelInfo
		}
	}
	return ModelMapping{modelNameToNVCFUrl, modelNameToModelInfo}, nil
}

func NewOpenAIDirectorV2(mapping *config.GatewayConfig, privateModelMatcher *regexp.Regexp, vanityDirector *VanityDirector, shadower *TrafficShadower) (*OpenAIDirector, error) {
	chatCompletions, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.ChatCompletions), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	completions, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.Completions), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	embeddings, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.Embeddings), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	responses, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.Responses), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	imageGenerations, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.ImageGenerations), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	imageEdits, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.ImageEdits), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	imageVariations, err := buildModelMapping(convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping.OpenAI.ImageVariations), privateModelMatcher)
	if err != nil {
		return nil, err
	}

	// Merge model lists across all endpoint sections, dedup by model ID, sort alphabetically
	seen := make(map[string]ModelInfo)
	for _, models := range []map[string]ModelInfo{
		chatCompletions.modelNameToModelInfo,
		completions.modelNameToModelInfo,
		embeddings.modelNameToModelInfo,
		responses.modelNameToModelInfo,
		imageGenerations.modelNameToModelInfo,
		imageEdits.modelNameToModelInfo,
		imageVariations.modelNameToModelInfo,
	} {
		for id, info := range models {
			seen[id] = info
		}
	}
	allModels := maps.Values(seen)
	slices.SortFunc(allModels, func(a, b ModelInfo) int {
		return cmp.Compare(a.Id, b.Id)
	})

	return &OpenAIDirector{
		chatCompletions:  chatCompletions,
		completions:      completions,
		embeddings:       embeddings,
		responses:        responses,
		imageGenerations: imageGenerations,
		imageEdits:       imageEdits,
		imageVariations:  imageVariations,
		allPublicModels:  allModels,
		vanityDirector:   vanityDirector,
		shadower:         shadower,
	}, nil
}

// filterExpiredModels returns a new slice with expired models removed
func filterExpiredModels(models []ModelInfo, allMappings ...map[string]FunctionInfo) []ModelInfo {
	filtered := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		// Check if the model is expired in any of the mappings
		expired := false
		for _, mapping := range allMappings {
			if funcInfo, ok := mapping[model.Id]; ok {
				if isModelExpired(funcInfo.eol) {
					expired = true
					break
				}
			}
		}
		if !expired {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// getFilteredModels returns the filtered models list with lazy caching (1 minute TTL)
func (d *OpenAIDirector) getFilteredModels() []ModelInfo {
	const cacheTTL = time.Minute

	// Fast path: check if cache is fresh without acquiring write lock
	d.filteredModelsMutex.RLock()
	if time.Since(d.filteredModelsLastComputed) < cacheTTL {
		cachedModels := d.filteredModelsCache
		d.filteredModelsMutex.RUnlock()
		return cachedModels
	}
	d.filteredModelsMutex.RUnlock()

	// Cache is stale, acquire write lock
	d.filteredModelsMutex.Lock()
	defer d.filteredModelsMutex.Unlock()

	// Double-check: another goroutine might have updated the cache
	if time.Since(d.filteredModelsLastComputed) < cacheTTL {
		return d.filteredModelsCache
	}

	// Compute filtered models
	d.filteredModelsCache = filterExpiredModels(
		d.allPublicModels,
		d.chatCompletions.modelNameToNVCFUrl,
		d.completions.modelNameToNVCFUrl,
		d.embeddings.modelNameToNVCFUrl,
		d.responses.modelNameToNVCFUrl,
		d.imageGenerations.modelNameToNVCFUrl,
		d.imageEdits.modelNameToNVCFUrl,
		d.imageVariations.modelNameToNVCFUrl,
	)
	d.filteredModelsLastComputed = time.Now()

	return d.filteredModelsCache
}

func convertIntoModelNameToFunctionIdAndVersionIdMappingV2(mapping map[string]config.ModelFunctionDetails) map[string]ModelNameToFunctionIdVersionId {
	modelNameToFunctionIdVersionId := make(map[string]ModelNameToFunctionIdVersionId)
	for _, entry := range mapping {
		modelNameToFunctionIdVersionId[entry.ModelName] = ModelNameToFunctionIdVersionId{
			FunctionId:                     entry.FunctionID,
			FunctionVersionId:              entry.FunctionVersionID,
			OutgoingPathOverride:           entry.OutgoingPathOverride,
			UsePexec:                       entry.UsePexec,
			EOL:                            entry.EOL,
			OfflineMessage:                 entry.OfflineMessage,
			TooManyRequestsMessage:         entry.TooManyRequestsMessage,
			ShadowModelNames:               effectiveShadowModelNames(entry.ShadowModelName, entry.ShadowModelNames),
			ShadowPercentage:               entry.ShadowPercentage,
			ShadowCancelOnClientDisconnect: entry.ShadowCancelOnClientDisconnect,
		}
	}

	return modelNameToFunctionIdVersionId
}

func defaultShadowPercentage(shadowPercentage *int) int {
	if shadowPercentage == nil {
		return 100
	}
	return *shadowPercentage
}

func effectiveShadowModelNames(legacyModelName string, modelNames []string) []string {
	if legacyModelName == "" && len(modelNames) == 0 {
		return nil
	}
	shadowModelNames := make([]string, 0, len(modelNames)+1)
	if legacyModelName != "" {
		shadowModelNames = append(shadowModelNames, legacyModelName)
	}
	return append(shadowModelNames, modelNames...)
}

func (d *OpenAIDirector) ServeCompletions(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.completions.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeChatCompletions(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.chatCompletions.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeEmbeddings(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.embeddings.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeResponses(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.responses.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeImageGenerations(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.imageGenerations.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeImageEdits(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.imageEdits.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ServeImageVariations(writer http.ResponseWriter, request *http.Request) {
	d.proxyModelMappedRequest(writer, request, d.imageVariations.modelNameToNVCFUrl)
}

func (d *OpenAIDirector) ListModels(writer http.ResponseWriter, _ *http.Request) {
	// Get filtered models with lazy caching
	activeModels := d.getFilteredModels()

	m := ModelListResponse{
		Object: "list",
		Data:   activeModels,
	}

	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(http.StatusOK) // Use the appropriate status code
	if err := json.NewEncoder(writer).Encode(m); err != nil {
		// Headers already sent, can't change status code, but log/handle the error
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (d *OpenAIDirector) GetModel(writer http.ResponseWriter, request *http.Request) {
	// support "/" in model name to follow pre established format
	company := chi.URLParam(request, "company")
	model := chi.URLParam(request, "model")

	var modelName string
	if company != "" && model != "" {
		modelName = company + "/" + model
	} else if model != "" {
		modelName = model
	} else {
		http.Error(writer, "model name was undefined", http.StatusInternalServerError)
		return
	}

	modelInfo, ok := getFirstValue(modelName,
		d.completions.modelNameToModelInfo,
		d.embeddings.modelNameToModelInfo,
		d.chatCompletions.modelNameToModelInfo,
		d.responses.modelNameToModelInfo,
		d.imageGenerations.modelNameToModelInfo,
		d.imageEdits.modelNameToModelInfo,
		d.imageVariations.modelNameToModelInfo,
	)
	if !ok {
		modelMissingError := "Model does not exist by the name of " + modelName
		http.Error(writer, modelMissingError, http.StatusNotFound)
		return
	}

	// Check if the model is offline or expired
	funcInfo, ok := getFirstValue(modelName,
		d.completions.modelNameToNVCFUrl,
		d.embeddings.modelNameToNVCFUrl,
		d.chatCompletions.modelNameToNVCFUrl,
		d.responses.modelNameToNVCFUrl,
		d.imageGenerations.modelNameToNVCFUrl,
		d.imageEdits.modelNameToNVCFUrl,
		d.imageVariations.modelNameToNVCFUrl,
	)
	if ok && writeFunctionStatusError(writer, funcInfo.offlineMessage, funcInfo.eol, modelName) {
		return
	}

	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(http.StatusOK) // Use the appropriate status code
	if err := json.NewEncoder(writer).Encode(modelInfo); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (d *OpenAIDirector) proxyModelMappedRequest(writer http.ResponseWriter, request *http.Request, modelToNVCFUrl map[string]FunctionInfo) {
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(traceAttrEndpointType.String(traceAttrValueEndpointOpenAI))
	setShadowSpanAttribute(span, request)

	resolved, handled := d.resolveModelMappedRequest(writer, request, modelToNVCFUrl)
	if handled {
		return
	}

	finishShadowPrimary := d.dispatchShadowIfNeeded(resolved, modelToNVCFUrl)
	var proxyErr error
	proxyReturned := false
	defer func() {
		recovered := recover()
		if recovered != nil {
			// ReverseProxy uses ErrAbortHandler for client-aborted streaming copies.
			// Treat only that sentinel as a proxy error so shadow cancellation runs.
			if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				proxyErr = err
			} else {
				proxyErr = errPrimaryProxyPanic
			}
		}
		if !proxyReturned && proxyErr == nil {
			proxyErr = errPrimaryProxyPanic
		}
		finishShadowPrimary(proxyErr)
		if recovered != nil {
			if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}
		}
	}()
	proxyErr = d.proxyResolvedRequest(writer, resolved)
	proxyReturned = true
}

func (d *OpenAIDirector) resolveModelMappedRequest(writer http.ResponseWriter, request *http.Request, modelToNVCFUrl map[string]FunctionInfo) (resolvedOpenAIRequest, bool) {
	body, err := extractOpenAIRequestBody(request)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			// Handle the case where the body is too large
			writer.WriteHeader(http.StatusBadRequest)
			errorMessage := OpenAIError{
				Error: CustomOpenAIError{
					Message: fmt.Sprintf("Please make sure your payload is below %d bytes in size. "+
						"If larger assets are required please refer to our Assets API.", maxRequestSize),
					Type: "invalid_request_error",
					Code: "invalid_image_format",
				},
			}
			if err := json.NewEncoder(writer).Encode(errorMessage); err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
			}
			return resolvedOpenAIRequest{}, true
		}
		if errors.Is(err, errModelFieldMissing) {
			writer.WriteHeader(http.StatusBadRequest)
			errorMessage := OpenAIError{
				Error: CustomOpenAIError{
					Message: "model field is required",
					Type:    "invalid_request_error",
					Code:    "missing_required_field",
				},
			}
			if encodeErr := json.NewEncoder(writer).Encode(errorMessage); encodeErr != nil {
				http.Error(writer, encodeErr.Error(), http.StatusInternalServerError)
			}
			return resolvedOpenAIRequest{}, true
		}
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return resolvedOpenAIRequest{}, true
	}
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(
		traceAttrModelName.String(body.Model),
		traceAttrModelStream.Bool(body.Stream),
	)

	nvcfUrl, ok := modelToNVCFUrl[body.Model]
	if !ok {
		_ = request.Body.Close()
		http.NotFound(writer, request)
		return resolvedOpenAIRequest{}, true
	}

	if writeFunctionStatusError(writer, nvcfUrl.offlineMessage, nvcfUrl.eol, body.Model) {
		_ = request.Body.Close()
		return resolvedOpenAIRequest{}, true
	}

	if nvcfUrl.tooManyRequestsMessage != "" {
		ctx := context.WithValue(request.Context(), tooManyRequestsKey, nvcfUrl.tooManyRequestsMessage)
		request = request.WithContext(ctx)
	}

	// setPollingHeaderIfNotPresent checks the Accept header, so we have to override it first
	if body.Stream {
		request.Header.Set("Accept", "text/event-stream")
	}

	return resolvedOpenAIRequest{
		request:      request,
		functionInfo: nvcfUrl,
	}, false
}

func (d *OpenAIDirector) dispatchShadowIfNeeded(resolved resolvedOpenAIRequest, modelToNVCFUrl map[string]FunctionInfo) func(error) {
	if isShadowRequest(resolved.request) {
		return func(error) {}
	}
	shadowModelNames := resolved.functionInfo.shadowModelNames
	if len(shadowModelNames) == 0 || d.shadower == nil {
		return func(error) {}
	}
	pct := resolved.functionInfo.shadowPercentage
	if pct < 100 && rand.IntN(100) >= pct {
		return func(error) {}
	}

	shadowCtx, finishShadowPrimary := shadowContext(resolved.request, resolved.functionInfo.shadowCancelOnClientDisconnect)

	// Clone body only for shadowed requests — avoids allocation on the hot path.
	rawBody, err := io.ReadAll(resolved.request.Body)
	_ = resolved.request.Body.Close() // releases bytebufferpool buffer
	if err != nil {
		recordShadowDispatchSummary(
			resolved.request.Context(),
			shadowModelNames,
			0,
			len(shadowModelNames),
			repeatedStrings(shadowDroppedReasonBodyReadError, len(shadowModelNames)),
			shadowModelNames,
		)
		return finishShadowPrimary
	}
	// Reset primary request body (pool buffer released above).
	resolved.request.Body = io.NopCloser(bytes.NewReader(rawBody))
	resolved.request.ContentLength = int64(len(rawBody))

	// Replay through the same code path — proxyModelMappedRequest will resolve the
	// shadow model name from the same mapping and proxy it. The NVCF-Shadow header
	// prevents further shadowing on the recursive call.
	replayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.proxyModelMappedRequest(w, r, modelToNVCFUrl)
	})

	dispatchedCount := 0
	droppedCount := 0
	var droppedReasons []string
	var droppedTargetModels []string
	for _, shadowModelName := range shadowModelNames {
		// Rewrite model field in the shadow body.
		shadowBody, err := rewriteShadowRequestModel(rawBody, shadowModelName)
		if err != nil {
			recordShadowDispatchSummary(
				resolved.request.Context(),
				shadowModelNames,
				0,
				len(shadowModelNames),
				repeatedStrings(shadowDroppedReasonBodyRewriteError, len(shadowModelNames)),
				shadowModelNames,
			)
			return finishShadowPrimary
		}

		// Build shadow request with the rewritten body and recursion guard.
		shadowReq := newShadowRequest(resolved.request, shadowBody, shadowCtx)
		handler := newShadowReplayHandler(shadowModelName, replayHandler)

		// Shadow admission errors are logged by TrafficShadower and summarized below.
		// They must not affect the primary request.
		err = d.shadower.Shadow(shadowReq, handler)
		if err != nil {
			droppedCount++
			if reason := shadowDroppedReason(err); reason != "" {
				droppedReasons = append(droppedReasons, reason)
				droppedTargetModels = append(droppedTargetModels, shadowModelName)
			}
			continue
		}
		dispatchedCount++
	}
	recordShadowDispatchSummary(resolved.request.Context(), shadowModelNames, dispatchedCount, droppedCount, droppedReasons, droppedTargetModels)
	return finishShadowPrimary
}

func (d *OpenAIDirector) proxyResolvedRequest(writer http.ResponseWriter, resolved resolvedOpenAIRequest) error {
	observer := &primaryProxyObserver{}
	proxyErr := d.vanityDirector.ServeExec(
		VanityExecRequest{
			FunctionID:        resolved.functionInfo.functionId,
			FunctionVersionID: resolved.functionInfo.functionVersionId,
			PathOverride:      resolved.functionInfo.pathOverride,
			UsePexec:          resolved.functionInfo.usePexec,
			EOL:               resolved.functionInfo.eol,
			OfflineMessage:    resolved.functionInfo.offlineMessage,
		},
		observer.wrap(writer),
		resolved.request,
	)
	return observer.shadowFinalizationError(resolved.request, proxyErr)
}

func rewriteShadowRequestModel(body []byte, modelName string) ([]byte, error) {
	if modelName == "" {
		return body, nil
	}

	payload := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode shadow request body: %w", err)
	}

	modelValue, err := json.Marshal(modelName)
	if err != nil {
		return nil, fmt.Errorf("failed to encode shadow model name: %w", err)
	}
	payload["model"] = modelValue

	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode shadow request body: %w", err)
	}
	return rewritten, nil
}

// errModelFieldMissing signals that the client sent a valid request body that did
// not contain a non-empty "model" field. Callers render this as HTTP 400 rather
// than letting it fall through to a misleading 404 "model not found".
var errModelFieldMissing = errors.New("model field is required")

// extractOpenAIRequestBody dispatches on Content-Type: multipart/form-data requests
// (image edits, image variations) read the model from a form field; everything
// else parses the OpenAI JSON body.
func extractOpenAIRequestBody(request *http.Request) (openAIRequestBody, error) {
	contentType := parseOpenAIContentType(request.Header.Get(contentTypeHeader))
	if contentType.mediaType == contentTypeMultipartFormData {
		return extractOpenAIMultipartBody(request, contentType.boundary)
	}
	return extractOpenAIJSONBody(request)
}

type openAIContentType struct {
	mediaType string
	boundary  string
}

func parseOpenAIContentType(header string) openAIContentType {
	mediaType, params, err := mime.ParseMediaType(header)
	if err != nil {
		return openAIContentType{}
	}
	return openAIContentType{
		mediaType: mediaType,
		boundary:  params["boundary"],
	}
}

// extractOpenAIMultipartBody reads the full multipart body into a pooled
// ReadCloser, extracts the "model" form field, and leaves request.Body readable
// so the reverse proxy can forward the original bytes unchanged. Streaming is
// always false for multipart image requests. Returns errModelFieldMissing if the
// "model" field is absent or empty.
func extractOpenAIMultipartBody(request *http.Request, boundary string) (openAIRequestBody, error) {
	if boundary == "" {
		return openAIRequestBody{}, fmt.Errorf("missing multipart boundary")
	}

	bodyBytes, err := readOpenAIRequestBody(request, "multipart body")
	if err != nil {
		return openAIRequestBody{}, err
	}

	modelName, err := readMultipartModelField(bytes.NewReader(bodyBytes.peek()), boundary)
	if err != nil {
		_ = bodyBytes.Close()
		return openAIRequestBody{}, err
	}
	if modelName == "" {
		_ = bodyBytes.Close()
		return openAIRequestBody{}, errModelFieldMissing
	}

	return openAIRequestBody{Model: modelName, Stream: false}, nil
}

// readMultipartModelField scans the multipart body for the "model" form field
// and returns its value (empty string if absent). Callers still close
// part.Close()'s error: multipart.Part.Close only drains remaining bytes, and any
// drain error surfaces on the next NextPart call, so silencing it here does not
// hide information.
func readMultipartModelField(r io.Reader, boundary string) (string, error) {
	mr := multipart.NewReader(r, boundary)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("failed to parse multipart: %w", err)
		}
		if part.FormName() != "model" {
			_ = part.Close()
			continue
		}
		value, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			return "", fmt.Errorf("failed to read model field: %w", readErr)
		}
		return string(value), nil
	}
}

// extractOpenAIJSONBody reads the full request body into a pooled ReadCloser and
// replaces request.Body with it.
func extractOpenAIJSONBody(request *http.Request) (openAIRequestBody, error) {
	bodyBytes, err := readOpenAIRequestBody(request, "json body")
	if err != nil {
		return openAIRequestBody{}, err
	}

	body := openAIRequestBody{}
	err = json.UnmarshalWithOption(bodyBytes.peek(), &body, json.DecodeFieldPriorityFirstWin())
	if err != nil {
		_ = bodyBytes.Close()
		return openAIRequestBody{}, fmt.Errorf("failed to decode json body: %w", err)
	}
	if body.Model == "" {
		_ = bodyBytes.Close()
		return openAIRequestBody{}, errModelFieldMissing
	}

	return body, nil
}

type bufferedOpenAIRequestBody struct {
	*bytes.Reader
	buffer  *bytebufferpool.ByteBuffer
	release func()
}

func readOpenAIRequestBody(request *http.Request, bodyDescription string) (*bufferedOpenAIRequestBody, error) {
	if request.Body == nil {
		return nil, fmt.Errorf("expecting %s", bodyDescription)
	}

	bodyBytes := bytebufferpool.Get()
	releaseBuffer := sync.OnceFunc(func() {
		bodyBytes.Reset()
		bytebufferpool.Put(bodyBytes)
	})
	_, err := io.Copy(bodyBytes, request.Body)
	_ = request.Body.Close()
	if err != nil {
		releaseBuffer()
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	bufferedBody := &bufferedOpenAIRequestBody{
		Reader:  bytes.NewReader(bodyBytes.Bytes()),
		buffer:  bodyBytes,
		release: releaseBuffer,
	}
	request.Body = bufferedBody
	return bufferedBody, nil
}

func (b *bufferedOpenAIRequestBody) peek() []byte {
	return b.buffer.Bytes()
}

func (b *bufferedOpenAIRequestBody) Close() error {
	b.release()
	return nil
}

func getFirstValue[K comparable, V any](k K, maps ...map[K]V) (V, bool) {
	for _, m := range maps {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	var empty V
	return empty, false
}

// isModelExpired checks if the current date is after the EOL date
func isModelExpired(eol time.Time) bool {
	if eol.IsZero() {
		return false
	}

	// Model is expired if current time is after the EOL time
	return time.Now().After(eol)
}
