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
	"ai-api-gateway-service/middleware"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/goccy/go-json"
)

const (
	anthropicModelsDefaultLimit = 20
	anthropicModelsMaxLimit     = 1000
)

// AnthropicModelInfo is one entry of the Anthropic Models API.
type AnthropicModelInfo struct {
	Type        string `json:"type"`
	Id          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// AnthropicModelListResponse is one page of the Anthropic Models API.
type AnthropicModelListResponse struct {
	Data    []AnthropicModelInfo `json:"data"`
	HasMore bool                 `json:"has_more"`
	FirstId *string              `json:"first_id"`
	LastId  *string              `json:"last_id"`
}

func toAnthropicModelInfo(info ModelInfo) AnthropicModelInfo {
	return AnthropicModelInfo{
		Type:        "model",
		Id:          info.Id,
		DisplayName: info.Id,
		CreatedAt:   time.Unix(info.Created, 0).UTC().Format(time.RFC3339),
	}
}

// listAnthropicModels lists the Messages models with Anthropic cursor paging.
// Models are ordered by ID, so a cursor keeps its position even when the model
// it names is removed between pages.
func (d *ModelDirector) listAnthropicModels(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	limit := anthropicModelsDefaultLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > anthropicModelsMaxLimit {
			markGatewayRejected(writer, request)
			writeAnthropicError(writer, http.StatusBadRequest, "limit must be an integer from 1 to 1000")
			return
		}
		limit = parsed
	}
	afterID, beforeID := query.Get("after_id"), query.Get("before_id")
	if afterID != "" && beforeID != "" {
		markGatewayRejected(writer, request)
		writeAnthropicError(writer, http.StatusBadRequest, "after_id and before_id cannot be used together")
		return
	}

	models := make([]ModelInfo, 0, len(d.messages.modelNameToModelInfo))
	for _, info := range d.getFilteredModels() {
		if _, ok := d.messages.modelNameToModelInfo[info.Id]; ok {
			models = append(models, info)
		}
	}

	start, end := 0, min(limit, len(models))
	hasMore := end < len(models)
	switch {
	case afterID != "":
		start = sort.Search(len(models), func(i int) bool { return models[i].Id > afterID })
		end = min(start+limit, len(models))
		hasMore = end < len(models)
	case beforeID != "":
		end = sort.Search(len(models), func(i int) bool { return models[i].Id >= beforeID })
		start = max(end-limit, 0)
		hasMore = start > 0
	}

	page := AnthropicModelListResponse{Data: make([]AnthropicModelInfo, 0, end-start), HasMore: hasMore}
	for _, info := range models[start:end] {
		page.Data = append(page.Data, toAnthropicModelInfo(info))
	}
	if len(page.Data) > 0 {
		page.FirstId = &page.Data[0].Id
		page.LastId = &page.Data[len(page.Data)-1].Id
	}
	writeAnthropicJSON(writer, page)
}

// getAnthropicModel answers a model lookup for an Anthropic client. Only
// models served on Messages exist for these clients.
func (d *ModelDirector) getAnthropicModel(writer http.ResponseWriter, request *http.Request, modelName string) {
	info, ok := d.messages.modelNameToModelInfo[modelName]
	if !ok {
		markGatewayRejected(writer, request)
		writeAnthropicError(writer, http.StatusNotFound, "model not found")
		return
	}
	funcInfo := d.messages.modelNameToNVCFUrl[modelName]
	middleware.AddOpenAIRequestMetricAttributes(request.Context(), modelName, funcInfo.functionId)
	if funcInfo.offlineMessage != "" {
		markGatewayRejected(writer, request)
		writer.Header().Set("Retry-After", "10800")
		writeAnthropicError(writer, http.StatusServiceUnavailable, funcInfo.offlineMessage)
		return
	}
	if isModelExpired(funcInfo.eol) {
		markGatewayRejected(writer, request)
		writeAnthropicError(writer, http.StatusGone, "model has reached end of life")
		return
	}
	writeAnthropicJSON(writer, toAnthropicModelInfo(info))
}

func writeAnthropicJSON(writer http.ResponseWriter, body any) {
	writer.Header().Set(contentTypeHeader, contentTypeApplicationJSON)
	writer.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}
