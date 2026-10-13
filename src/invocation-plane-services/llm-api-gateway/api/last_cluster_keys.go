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

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

func (h *Handlers) lastClusterEnabled() bool {
	return h != nil && h.config != nil && h.config.Stargate.LastCluster.Enabled
}

func (h *Handlers) payloadLastClusterEligible(reqCtx *requestctx.RequestContext) bool {
	return h.lastClusterEnabled() && reqCtx != nil && reqCtx.SessionSource == requestctx.SessionSourcePayload
}

func (h *Handlers) setChatLastClusterKeys(reqCtx *requestctx.RequestContext, rawBody []byte) {
	if !h.payloadLastClusterEligible(reqCtx) {
		return
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return
	}
	output, err := assistantMessageFlags(body.Messages)
	if err != nil {
		return
	}
	setPayloadLastClusterKeys(reqCtx, lastcluster.PayloadInput{
		Endpoint: lastcluster.EndpointChatCompletions,
		Items:    body.Messages,
		Output:   output,
	})
}

func (h *Handlers) setResponsesLastClusterKeys(reqCtx *requestctx.RequestContext, req *openairesponses.CreateRequest, rawBody []byte) {
	if !h.payloadLastClusterEligible(reqCtx) || req == nil || stringPtrValue(req.PreviousResponseID) != "" {
		return
	}
	for _, item := range req.Input.Items {
		if item.Type == openairesponses.ItemTypeReference {
			return
		}
	}
	var body struct {
		Input        json.RawMessage `json:"input"`
		Instructions json.RawMessage `json:"instructions"`
		Prompt       json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil || len(body.Input) == 0 || bytes.Equal(bytes.TrimSpace(body.Input), []byte("null")) {
		return
	}
	var items []json.RawMessage
	var text string
	if err := json.Unmarshal(body.Input, &text); err == nil {
		item, err := json.Marshal(map[string]string{"content": text, "role": "user"})
		if err != nil {
			return
		}
		items = []json.RawMessage{item}
	} else if err := json.Unmarshal(body.Input, &items); err != nil {
		return
	}
	// Input.UnmarshalJSON decodes arrays directly into []InputItem, preserving indices.
	if len(items) != len(req.Input.Items) {
		return
	}
	output := make([]bool, len(items))
	for i, item := range req.Input.Items {
		switch item.Type {
		case "", openairesponses.ItemTypeMessage:
			output[i] = item.Role == openairesponses.RoleAssistant
		case openairesponses.ItemTypeReasoning, openairesponses.ItemTypeFunctionCall,
			openairesponses.ItemTypeCustomToolCall, openairesponses.ItemTypeWebSearchCall,
			openairesponses.ItemTypeCodeInterpreterCall, openairesponses.ItemTypeMCPCall,
			openairesponses.ItemTypeMCPListTools, openairesponses.ItemTypeMCPApprovalRequest:
			output[i] = true
		}
	}
	setPayloadLastClusterKeys(reqCtx, lastcluster.PayloadInput{
		Endpoint: lastcluster.EndpointResponses,
		Leading:  []json.RawMessage{lastClusterLeadingPart(body.Instructions), lastClusterLeadingPart(body.Prompt)},
		Items:    items,
		Output:   output,
	})
}

func (h *Handlers) setMessagesLastClusterKeys(reqCtx *requestctx.RequestContext, messages []json.RawMessage, system json.RawMessage) {
	if !h.payloadLastClusterEligible(reqCtx) {
		return
	}
	output, err := assistantMessageFlags(messages)
	if err != nil {
		return
	}
	setPayloadLastClusterKeys(reqCtx, lastcluster.PayloadInput{
		Endpoint: lastcluster.EndpointMessages,
		Leading:  []json.RawMessage{lastClusterLeadingPart(system)},
		Items:    messages,
		Output:   output,
	})
}

func assistantMessageFlags(items []json.RawMessage) ([]bool, error) {
	output := make([]bool, len(items))
	for i, item := range items {
		var message struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(item, &message); err != nil {
			return nil, err
		}
		output[i] = message.Role == "assistant"
	}
	return output, nil
}

func lastClusterLeadingPart(part json.RawMessage) json.RawMessage {
	if len(part) == 0 || bytes.Equal(bytes.TrimSpace(part), []byte("null")) {
		return nil
	}
	return part
}

func setPayloadLastClusterKeys(reqCtx *requestctx.RequestContext, input lastcluster.PayloadInput) {
	if len(input.Items) == 0 {
		return
	}
	input.OrgID = reqCtx.OrgID
	input.ProjectID = reqCtx.ProjectID
	lookup, write, err := lastcluster.PayloadSessionValues(input)
	if err != nil {
		return
	}
	reqCtx.LastClusterLookupKey = lookup
	reqCtx.LastClusterWriteKey = write
}
