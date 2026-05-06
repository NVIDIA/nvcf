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

package mcp

import (
	"encoding/json"
	"strings"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

const ServerSeparator = "."

type RequireApproval struct {
	String *string                `json:"-"`
	Object *RequireApprovalObject `json:"-"`
}

type RequireApprovalObject struct {
	Always json.RawMessage       `json:"always,omitempty"`
	Never  *RequireApprovalNever `json:"never,omitempty"`
}

type RequireApprovalNever struct {
	ToolNames []string `json:"tool_names"`
}

func (obj *RequireApprovalObject) HasAlways() bool {
	return len(obj.Always) > 0
}

type ApprovalTool interface {
	GetRequireApproval() *RequireApproval
	RequiresApprovalForTool(string) bool
}

func RequiresApprovalAlways(tools []ApprovalTool) bool {
	for _, tool := range tools {
		approval := tool.GetRequireApproval()
		if approval == nil {
			continue
		}

		if approval.String != nil {
			if ptr.Deref(approval.String) == "always" {
				return true
			}
			continue
		}

		if approval.Object != nil && approval.Object.HasAlways() {
			return true
		}
	}

	return false
}

func RequiresApprovalForTool(tool ApprovalTool, toolName string) bool {
	if tool == nil {
		return false
	}
	return tool.RequiresApprovalForTool(toolName)
}

func SanitizeServerLabel(serverLabel string) string {
	var result strings.Builder

	for _, r := range serverLabel {
		switch {
		case r >= 'A' && r <= 'Z':
			result.WriteRune(r - 'A' + 'a')
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			result.WriteRune(r)
		default:
			result.WriteRune('_')
		}
	}

	sanitized := result.String()
	for strings.Contains(sanitized, "__") {
		sanitized = strings.ReplaceAll(sanitized, "__", "_")
	}
	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		sanitized = "server"
	}
	return sanitized
}

func CreateToolNameWithLabel(serverLabel string, toolName string) string {
	return SanitizeServerLabel(serverLabel) + ServerSeparator + toolName
}
