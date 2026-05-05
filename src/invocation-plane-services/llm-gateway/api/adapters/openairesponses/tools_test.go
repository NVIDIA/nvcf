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

package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

func TestFunctionTool_MarshalJSON(t *testing.T) {
	tool := &FunctionTool{
		ToolBase:    ToolBase{Type: ToolTypeFunction},
		Name:        "get_weather",
		Description: ptr.To("Get the current weather"),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type":        "string",
					"description": "The location to get weather for",
				},
			},
			"required": []string{"location"},
		},
		Strict: ptr.To(true),
	}

	jsonData, err := json.Marshal(tool)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(jsonData, &result)
	require.NoError(t, err)

	// Verify all function tool fields are present
	require.Equal(t, "function", result["type"])
	require.Equal(t, "get_weather", result["name"])
	require.Equal(t, "Get the current weather", result["description"])
	require.NotNil(t, result["parameters"])
	require.Equal(t, true, result["strict"])

	// Verify no browser search or code interpreter fields are present
	require.Nil(t, result["user_location"])
	require.Nil(t, result["search_context_size"])
	require.Nil(t, result["container"])
}

func TestBrowserSearchTool_MarshalJSON(t *testing.T) {
	tool := &BrowserSearchTool{
		ToolBase: ToolBase{Type: ToolTypeBrowserSearch},
		UserLocation: &UserLocation{
			Type:    "location",
			Country: ptr.To("US"),
			City:    ptr.To("San Francisco"),
		},
		SearchContextSize: ptr.To("large"),
	}

	jsonData, err := json.Marshal(tool)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(jsonData, &result)
	require.NoError(t, err)

	// Verify all browser search tool fields are present
	require.Equal(t, "browser_search", result["type"])
	require.NotNil(t, result["user_location"])
	require.Equal(t, "large", result["search_context_size"])

	// Verify no function or code interpreter fields are present
	require.Nil(t, result["name"])
	require.Nil(t, result["description"])
	require.Nil(t, result["parameters"])
	require.Nil(t, result["strict"])
	require.Nil(t, result["container"])
}

func TestCodeInterpreterTool_MarshalJSON(t *testing.T) {
	tool := &CodeInterpreterTool{
		ToolBase: ToolBase{Type: ToolTypeCodeInterpreter},
		Container: CodeInterpreterContainer{
			Type:    "auto",
			FileIDs: []string{"file1", "file2"},
		},
	}

	jsonData, err := json.Marshal(tool)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(jsonData, &result)
	require.NoError(t, err)

	// Verify all code interpreter tool fields are present
	require.Equal(t, "code_interpreter", result["type"])
	require.NotNil(t, result["container"])

	// Verify no function or browser search fields are present
	require.Nil(t, result["name"])
	require.Nil(t, result["description"])
	require.Nil(t, result["parameters"])
	require.Nil(t, result["strict"])
	require.Nil(t, result["user_location"])
	require.Nil(t, result["search_context_size"])
}

func TestMCPTool_MarshalJSON_URLRedaction(t *testing.T) {
	tests := []struct {
		name        string
		tool        *MCPTool
		expectedURL string
	}{
		{
			name: "MCP tool with path - should redact path",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "hf",
				ServerURL:   "https://huggingface.co/mcp",
			},
			expectedURL: "https://huggingface.co/<redacted>",
		},
		{
			name: "MCP tool with deep path - should redact all path",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "example",
				ServerURL:   "https://api.example.com/v1/mcp/server",
			},
			expectedURL: "https://api.example.com/<redacted>",
		},
		{
			name: "MCP tool with query params - should redact everything after domain",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "test",
				ServerURL:   "https://test.com/api?key=secret&version=1",
			},
			expectedURL: "https://test.com/<redacted>",
		},
		{
			name: "MCP tool with port - should preserve port and redact path",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "localhost",
				ServerURL:   "http://localhost:8080/mcp",
			},
			expectedURL: "http://localhost:8080/<redacted>",
		},
		{
			name: "MCP tool with no path - should not change",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "simple",
				ServerURL:   "https://example.com",
			},
			expectedURL: "https://example.com",
		},
		{
			name: "MCP tool with only root slash - should not redact",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "root",
				ServerURL:   "https://example.com/",
			},
			expectedURL: "https://example.com/",
		},
		{
			name: "MCP tool with empty URL - should handle gracefully",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "empty",
				ServerURL:   "",
			},
			expectedURL: "",
		},
		{
			name: "MCP tool with invalid URL format - should not crash",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "invalid",
				ServerURL:   "not-a-url",
			},
			expectedURL: "not-a-url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonData, err := json.Marshal(tt.tool)
			require.NoError(t, err)

			// Unmarshal back to check the URL
			var unmarshaled map[string]any
			err = json.Unmarshal(jsonData, &unmarshaled)
			require.NoError(t, err)

			if tt.expectedURL == "" {
				// For empty URLs, the field might be omitted or empty
				serverURL, hasServerURL := unmarshaled["server_url"]
				if hasServerURL {
					require.Empty(t, serverURL)
				}
				// It's also valid for the field to be omitted entirely
			} else {
				require.Equal(t, tt.expectedURL, unmarshaled["server_url"])
			}

			// Verify other MCP fields are preserved
			require.Equal(t, ToolTypeMCP, unmarshaled["type"])
			require.Equal(t, tt.tool.ServerLabel, unmarshaled["server_label"])
		})
	}
}

func TestMCPTool_MarshalJSON_ConnectorIDNullsInternalFields(t *testing.T) {
	var (
		auth = "Bearer secret"
		desc = "Google Drive integration"
	)

	tool := &MCPTool{
		ToolBase:    ToolBase{Type: ToolTypeMCP},
		ServerLabel: "googledrive",
		ServerURL:   "http://localhost:8080/should_be_null",
		ConnectorID: "connector_googledrive",
		Headers: &map[string]string{
			"Authorization": auth,
			"X-Test":        "value",
		},
		Authorization:     &auth,
		ServerDescription: &desc,
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))

	require.Nil(t, out["server_url"])
	require.Nil(t, out["headers"])
	require.Nil(t, out["server_description"])
	require.Equal(t, "<redacted>", out["authorization"])
}

func TestMCPTool_MarshalJSON_InternalConnectorURLNullsFields(t *testing.T) {
	var (
		auth = "Bearer secret"
		desc = "Internal connector"
	)

	tool := &MCPTool{
		ToolBase:    ToolBase{Type: ToolTypeMCP},
		ServerLabel: "googledrive",
		ServerURL:   "http://mcp-connectors.orion.svc.cluster.local:8080/googledrive",
		Headers: &map[string]string{
			"Authorization": auth,
		},
		Authorization:     &auth,
		ServerDescription: &desc,
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))

	require.Nil(t, out["server_url"])
	require.Nil(t, out["headers"])
	require.Nil(t, out["server_description"])
	require.Equal(t, "<redacted>", out["authorization"])
}

func TestMCPTool_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    *MCPTool
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid MCP tool",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "hf",
				ServerURL:   "https://huggingface.co/mcp",
			},
			wantErr: false,
		},
		{
			name: "MCP tool with headers",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "test",
				ServerURL:   "https://api.example.com/mcp",
				Headers: &map[string]string{
					"Authorization": "Bearer token",
					"Content-Type":  "application/json",
				},
			},
			wantErr: false,
		},
		{
			name: "missing type",
			tool: &MCPTool{
				ServerLabel: "hf",
				ServerURL:   "https://huggingface.co/mcp",
			},
			wantErr: true,
			errMsg:  "type",
		},
		{
			name: "wrong type",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: "function"},
				ServerLabel: "hf",
				ServerURL:   "https://huggingface.co/mcp",
			},
			wantErr: true,
			errMsg:  "must be 'mcp'",
		},
		{
			name: "missing server_label",
			tool: &MCPTool{
				ToolBase:  ToolBase{Type: ToolTypeMCP},
				ServerURL: "https://huggingface.co/mcp",
			},
			wantErr: true,
			errMsg:  "server_label",
		},
		{
			name: "missing server_url",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "hf",
			},
			wantErr: true,
			errMsg:  "connector_id",
		},
		{
			name: "empty server_label",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "",
				ServerURL:   "https://huggingface.co/mcp",
			},
			wantErr: true,
			errMsg:  "server_label",
		},
		{
			name: "empty server_url",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "hf",
				ServerURL:   "",
			},
			wantErr: true,
			errMsg:  "connector_id",
		},
		{
			name: "connector id without server url",
			tool: &MCPTool{
				ToolBase:    ToolBase{Type: ToolTypeMCP},
				ServerLabel: "hf",
				ConnectorID: "connector_dropbox",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUnmarshalTool_MCPTool(t *testing.T) {
	tests := []struct {
		name     string
		jsonData string
		wantErr  bool
		validate func(*testing.T, Tool)
	}{
		{
			name: "basic MCP tool",
			jsonData: `{
				"type": "mcp",
				"server_label": "hf",
				"server_url": "https://huggingface.co/mcp"
			}`,
			wantErr: false,
			validate: func(t *testing.T, tool Tool) {
				mcpTool, ok := tool.(*MCPTool)
				require.True(t, ok, "Expected MCPTool type")
				require.Equal(t, ToolTypeMCP, mcpTool.Type)
				require.Equal(t, "hf", mcpTool.ServerLabel)
				require.Equal(t, "https://huggingface.co/mcp", mcpTool.ServerURL)
				require.Empty(t, mcpTool.Headers)
			},
		},
		{
			name: "MCP tool with headers",
			jsonData: `{
				"type": "mcp",
				"server_label": "test",
				"server_url": "https://api.example.com/mcp",
				"headers": {
					"Authorization": "Bearer token",
					"Content-Type": "application/json"
				}
			}`,
			wantErr: false,
			validate: func(t *testing.T, tool Tool) {
				mcpTool, ok := tool.(*MCPTool)
				require.True(t, ok, "Expected MCPTool type")
				require.Equal(t, ToolTypeMCP, mcpTool.Type)
				require.Equal(t, "test", mcpTool.ServerLabel)
				require.Equal(t, "https://api.example.com/mcp", mcpTool.ServerURL)
				require.NotEmpty(t, mcpTool.Headers)
				require.Equal(t, "Bearer token", (*mcpTool.Headers)["Authorization"])
				require.Equal(t, "application/json", (*mcpTool.Headers)["Content-Type"])
			},
		},
		{
			name: "MCP tool missing server_label",
			jsonData: `{
				"type": "mcp",
				"server_url": "https://huggingface.co/mcp"
			}`,
			wantErr: false, // Validation happens separately
			validate: func(t *testing.T, tool Tool) {
				mcpTool, ok := tool.(*MCPTool)
				require.True(t, ok, "Expected MCPTool type")
				require.Empty(t, mcpTool.ServerLabel)
				// Should fail validation
				err := mcpTool.Validate()
				require.Error(t, err)
				require.Contains(t, err.Error(), "server_label")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, err := UnmarshalTool([]byte(tt.jsonData))
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				tt.validate(t, tool)
			}
		})
	}
}

func TestMCPTool_MarshalJSON_Basic(t *testing.T) {
	tool := &MCPTool{
		ToolBase:      ToolBase{Type: ToolTypeMCP},
		ServerLabel:   "hf",
		ServerURL:     "https://huggingface.co/mcp",
		Authorization: ptr.To("super-secret-token"),
		Headers: &map[string]string{
			"Authorization": "Bearer secret",
			"X-Custom":      "value",
		},
	}

	jsonData, err := json.Marshal(tool)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(jsonData, &result)
	require.NoError(t, err)

	// Verify all MCP tool fields are present
	require.Equal(t, "mcp", result["type"])
	require.Equal(t, "hf", result["server_label"])
	require.Equal(
		t,
		"https://huggingface.co/<redacted>",
		result["server_url"],
	) // URL should be redacted
	require.NotNil(t, result["headers"])

	// Verify headers with redaction for Authorization
	headers, ok := result["headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(
		t,
		"<redacted>",
		headers["Authorization"],
	) // Authorization should be redacted for security
	require.Equal(t, "value", headers["X-Custom"]) // Non-auth headers should be preserved
	require.Equal(t, "<redacted>", result["authorization"])

	// Verify no other tool type fields are present
	require.Nil(t, result["name"])
	require.Nil(t, result["description"])
	require.Nil(t, result["parameters"])
	require.Nil(t, result["strict"])
	require.Nil(t, result["user_location"])
	require.Nil(t, result["search_context_size"])
	require.Nil(t, result["container"])
}

func TestToolSlice_UnmarshalJSON_WithMCP(t *testing.T) {
	jsonData := `[
		{
			"type": "function",
			"name": "get_weather",
			"description": "Get weather"
		},
		{
			"type": "mcp",
			"server_label": "hf",
			"server_url": "https://huggingface.co/mcp"
		},
		{
			"type": "mcp",
			"server_label": "custom",
			"server_url": "https://api.example.com/mcp",
			"headers": {
				"Authorization": "Bearer token"
			}
		}
	]`

	var (
		tools ToolSlice
		err   = json.Unmarshal([]byte(jsonData), &tools)
	)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	// Check first tool is FunctionTool
	funcTool, ok := tools[0].(*FunctionTool)
	require.True(t, ok)
	require.Equal(t, "get_weather", funcTool.Name)

	// Check second tool is MCPTool
	mcpTool1, ok := tools[1].(*MCPTool)
	require.True(t, ok)
	require.Equal(t, ToolTypeMCP, mcpTool1.Type)
	require.Equal(t, "hf", mcpTool1.ServerLabel)
	require.Equal(t, "https://huggingface.co/mcp", mcpTool1.ServerURL)
	require.Empty(t, mcpTool1.Headers)

	// Check third tool is MCPTool with headers
	mcpTool2, ok := tools[2].(*MCPTool)
	require.True(t, ok)
	require.Equal(t, "custom", mcpTool2.ServerLabel)
	require.Equal(t, "https://api.example.com/mcp", mcpTool2.ServerURL)
	require.Equal(t, "Bearer token", (*mcpTool2.Headers)["Authorization"])
}

// Additive tests for additional require_approval scenarios; do not modify existing tests.

func TestMCPRequireApproval_Unmarshal_InvalidTypes_Added(t *testing.T) {
	cases := []string{
		`{"type":"mcp","server_label":"deep","server_url":"u","require_approval": 123}`,
		`{"type":"mcp","server_label":"deep","server_url":"u","require_approval": true}`,
		`{"type":"mcp","server_label":"deep","server_url":"u","require_approval": ["always"]}`,
	}
	for _, js := range cases {
		var tool MCPTool
		err := json.Unmarshal([]byte(js), &tool)
		require.Error(t, err)
		_, ok := err.(*UnmarshalError)
		require.True(t, ok, "expected UnmarshalError, got %T", err)
	}
}

func TestMCPTool_RequiresApprovalForTool_NilAndEmptyObject_Added(t *testing.T) {
	// Nil require_approval => no approval required
	toolNil := MCPTool{
		ToolBase:        ToolBase{Type: ToolTypeMCP},
		ServerLabel:     "deepwiki",
		ServerURL:       "https://mcp.deepwiki.com/mcp",
		RequireApproval: nil,
	}
	require.False(t, toolNil.RequiresApprovalForTool("anything"))

	// Empty object {} => default to no approval
	var (
		toolEmpty MCPTool
		err       = json.Unmarshal([]byte(`{
        "type": "mcp",
        "server_label": "deepwiki",
        "server_url": "https://mcp.deepwiki.com/mcp",
        "require_approval": {}
    }`), &toolEmpty)
	)
	require.NoError(t, err)
	require.NotNil(t, toolEmpty.RequireApproval)
	require.NotNil(t, toolEmpty.RequireApproval.Object)
	require.False(t, toolEmpty.RequireApproval.Object.HasAlways())
	require.Nil(t, toolEmpty.RequireApproval.Object.Never)
	require.False(t, toolEmpty.RequiresApprovalForTool("ask_question"))
}

func TestMCPRequireApproval_Unmarshal_ObjectNeverEmptyList_Added(t *testing.T) {
	var (
		tool MCPTool
		err  = json.Unmarshal([]byte(`{
        "type": "mcp",
        "server_label": "deepwiki",
        "server_url": "https://mcp.deepwiki.com/mcp",
        "require_approval": {"never": {"tool_names": []}}
    }`), &tool)
	)
	require.NoError(t, err)
	require.NotNil(t, tool.RequireApproval)
	require.Nil(t, tool.RequireApproval.String)
	require.NotNil(t, tool.RequireApproval.Object)
	require.NotNil(t, tool.RequireApproval.Object.Never)
	require.Empty(t, tool.RequireApproval.Object.Never.ToolNames)
	// Not explicitly never => require approval due to presence of never
	require.True(t, tool.RequiresApprovalForTool("write_wiki_page"))
}

func TestMCPRequireApproval_Unmarshal_ObjectUnknownKeys_Added(t *testing.T) {
	var (
		tool MCPTool
		err  = json.Unmarshal([]byte(`{
        "type": "mcp",
        "server_label": "deepwiki",
        "server_url": "https://mcp.deepwiki.com/mcp",
        "require_approval": {"unknown": {}}
    }`), &tool)
	)
	require.NoError(t, err)
	require.NotNil(t, tool.RequireApproval)
	require.Nil(t, tool.RequireApproval.String)
	require.NotNil(t, tool.RequireApproval.Object)
	require.False(t, tool.RequireApproval.Object.HasAlways())
	require.Nil(t, tool.RequireApproval.Object.Never)
	require.False(t, tool.RequiresApprovalForTool("any"))
}

func TestMCPTool_GetApprovalStringValue_Added(t *testing.T) {
	var toolNil MCPTool
	require.Nil(t, toolNil.GetApprovalStringValue())

	toolStr := MCPTool{
		RequireApproval: &MCPRequireApproval{String: &[]string{RequireApprovalAlways}[0]},
	}
	require.NotNil(t, toolStr.GetApprovalStringValue())
	require.Equal(t, RequireApprovalAlways, ptr.Deref(toolStr.GetApprovalStringValue()))

	toolObj := MCPTool{RequireApproval: &MCPRequireApproval{Object: &MCPRequireApprovalObject{}}}
	// Object form returns "always" as a conservative string value
	require.NotNil(t, toolObj.GetApprovalStringValue())
	require.Equal(t, RequireApprovalAlways, ptr.Deref(toolObj.GetApprovalStringValue()))
}

func TestMCPTool_MarshalJSON_RequireApprovalEchoObjectShape_Added(t *testing.T) {
	t.Run("object always emits read_only and tool_names", func(t *testing.T) {
		tool := MCPTool{
			ToolBase:    ToolBase{Type: ToolTypeMCP},
			ServerLabel: "deepwiki",
			ServerURL:   "https://mcp.deepwiki.com/mcp",
			RequireApproval: &MCPRequireApproval{
				Object: &MCPRequireApprovalObject{Always: json.RawMessage(`{"ignored":true}`)},
			},
		}
		b, err := json.Marshal(&tool)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		ra, ok := m["require_approval"].(map[string]any)
		require.True(t, ok)
		always, ok := ra["always"].(map[string]any)
		require.True(t, ok)
		_, hasRO := always["read_only"]
		require.True(t, hasRO)
		require.Nil(t, always["read_only"]) // explicit null
		tn, ok := always["tool_names"].([]any)
		require.True(t, ok)
		require.Empty(t, tn)
	})

	t.Run("object never emits read_only and tool_names list", func(t *testing.T) {
		tool := MCPTool{
			ToolBase:    ToolBase{Type: ToolTypeMCP},
			ServerLabel: "deepwiki",
			ServerURL:   "https://mcp.deepwiki.com/mcp",
			RequireApproval: &MCPRequireApproval{
				Object: &MCPRequireApprovalObject{
					Never: &MCPRequireApprovalNever{
						ToolNames: []string{"ask_question", "read_wiki_structure"},
					},
				},
			},
		}
		b, err := json.Marshal(&tool)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		ra, ok := m["require_approval"].(map[string]any)
		require.True(t, ok)
		never, ok := ra["never"].(map[string]any)
		require.True(t, ok)
		_, hasRO := never["read_only"]
		require.True(t, hasRO)
		require.Nil(t, never["read_only"]) // explicit null
		tn, ok := never["tool_names"].([]any)
		require.True(t, ok)
		require.Len(t, tn, 2)
	})
}

func TestMCPTool_GetType(t *testing.T) {
	mcpTool := &MCPTool{
		ToolBase: ToolBase{Type: ToolTypeMCP},
	}
	require.Equal(t, ToolTypeMCP, mcpTool.GetType())
}

func TestUnmarshalTool_FunctionTool(t *testing.T) {
	jsonData := `{
		"type": "function",
		"name": "get_weather",
		"description": "Get the current weather",
		"parameters": {
			"type": "object",
			"properties": {
				"location": {
					"type": "string"
				}
			}
		},
		"strict": true
	}`

	tool, err := UnmarshalTool([]byte(jsonData))
	require.NoError(t, err)

	functionTool, ok := tool.(*FunctionTool)
	require.True(t, ok, "Expected FunctionTool type")
	require.Equal(t, ToolTypeFunction, functionTool.Type)
	require.Equal(t, "get_weather", functionTool.Name)
	require.NotNil(t, functionTool.Description)
	require.Equal(t, "Get the current weather", ptr.Deref(functionTool.Description))
	require.NotNil(t, functionTool.Parameters)
	require.NotNil(t, functionTool.Strict)
	require.True(t, ptr.Deref(functionTool.Strict))
}

func TestUnmarshalTool_BrowserSearchTool(t *testing.T) {
	jsonData := `{
		"type": "browser_search",
		"user_location": {
			"type": "location",
			"country": "US",
			"city": "San Francisco"
		},
		"search_context_size": "large"
	}`

	tool, err := UnmarshalTool([]byte(jsonData))
	require.NoError(t, err)

	browserTool, ok := tool.(*BrowserSearchTool)
	require.True(t, ok, "Expected BrowserSearchTool type")
	require.Equal(t, ToolTypeBrowserSearch, browserTool.Type)
	require.NotNil(t, browserTool.UserLocation)
	require.Equal(t, "US", ptr.Deref(browserTool.UserLocation.Country))
	require.Equal(t, "San Francisco", ptr.Deref(browserTool.UserLocation.City))
	require.NotNil(t, browserTool.SearchContextSize)
	require.Equal(t, "large", ptr.Deref(browserTool.SearchContextSize))
}

func TestUnmarshalTool_WebSearchPreview(t *testing.T) {
	jsonData := `{
		"type": "web_search_preview_v1",
		"user_location": {
			"type": "location",
			"country": "UK"
		}
	}`

	tool, err := UnmarshalTool([]byte(jsonData))
	require.NoError(t, err)

	browserTool, ok := tool.(*BrowserSearchTool)
	require.True(t, ok, "Expected BrowserSearchTool type for web_search_preview")
	require.Equal(t, "web_search_preview_v1", browserTool.Type)
	require.NotNil(t, browserTool.UserLocation)
	require.Equal(t, "UK", ptr.Deref(browserTool.UserLocation.Country))
}

func TestUnmarshalTool_CodeInterpreterTool(t *testing.T) {
	jsonData := `{
		"type": "code_interpreter",
		"container": {
			"type": "auto",
			"file_ids": ["file1", "file2"]
		}
	}`

	tool, err := UnmarshalTool([]byte(jsonData))
	require.NoError(t, err)

	codeTool, ok := tool.(*CodeInterpreterTool)
	require.True(t, ok, "Expected CodeInterpreterTool type")
	require.Equal(t, ToolTypeCodeInterpreter, codeTool.Type)
	require.NotNil(t, codeTool.Container)

	container, ok := codeTool.Container.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "auto", container["type"])
}

func TestUnmarshalTool_UnknownType(t *testing.T) {
	jsonData := `{
		"type": "unknown_type",
		"name": "test"
	}`

	_, err := UnmarshalTool([]byte(jsonData))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool type")
}

func TestToolSlice_UnmarshalJSON(t *testing.T) {
	jsonData := `[
		{
			"type": "function",
			"name": "func1",
			"description": "First function"
		},
		{
			"type": "browser_search",
			"search_context_size": "medium"
		},
		{
			"type": "code_interpreter",
			"container": "container123"
		}
	]`

	var (
		tools ToolSlice
		err   = json.Unmarshal([]byte(jsonData), &tools)
	)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	// Check first tool is FunctionTool
	funcTool, ok := tools[0].(*FunctionTool)
	require.True(t, ok)
	require.Equal(t, "func1", funcTool.Name)
	require.NotNil(t, funcTool.Description)
	require.Equal(t, "First function", ptr.Deref(funcTool.Description))

	// Check second tool is BrowserSearchTool
	browserTool, ok := tools[1].(*BrowserSearchTool)
	require.True(t, ok)
	require.NotNil(t, browserTool.SearchContextSize)
	require.Equal(t, "medium", ptr.Deref(browserTool.SearchContextSize))

	// Check third tool is CodeInterpreterTool
	codeTool, ok := tools[2].(*CodeInterpreterTool)
	require.True(t, ok)
	require.Equal(t, "container123", codeTool.Container)
}

func TestFunctionTool_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    *FunctionTool
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid function tool",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: ToolTypeFunction},
				Name:     "test_func",
			},
			wantErr: false,
		},
		{
			name: "missing type",
			tool: &FunctionTool{
				Name: "test_func",
			},
			wantErr: true,
			errMsg:  "type",
		},
		{
			name: "wrong type",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: "browser_search"},
				Name:     "test_func",
			},
			wantErr: true,
			errMsg:  "must be 'function'",
		},
		{
			name: "missing name",
			tool: &FunctionTool{
				ToolBase: ToolBase{Type: ToolTypeFunction},
			},
			wantErr: true,
			errMsg:  "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBrowserSearchTool_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    *BrowserSearchTool
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid browser_search tool",
			tool: &BrowserSearchTool{
				ToolBase: ToolBase{Type: ToolTypeBrowserSearch},
			},
			wantErr: false,
		},
		{
			name: "valid web_search_preview tool",
			tool: &BrowserSearchTool{
				ToolBase: ToolBase{Type: "web_search_preview_v2"},
			},
			wantErr: false,
		},
		{
			name: "invalid type",
			tool: &BrowserSearchTool{
				ToolBase: ToolBase{Type: "function"},
			},
			wantErr: true,
			errMsg:  "must be 'browser_search' or 'web_search_preview*'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCodeInterpreterTool_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    *CodeInterpreterTool
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid code_interpreter tool",
			tool: &CodeInterpreterTool{
				ToolBase: ToolBase{Type: ToolTypeCodeInterpreter},
			},
			wantErr: false,
		},
		{
			name: "valid with container",
			tool: &CodeInterpreterTool{
				ToolBase:  ToolBase{Type: ToolTypeCodeInterpreter},
				Container: "container123",
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			tool: &CodeInterpreterTool{
				ToolBase: ToolBase{Type: "function"},
			},
			wantErr: true,
			errMsg:  "must be 'code_interpreter'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestToolInterface_GetType(t *testing.T) {
	funcTool := &FunctionTool{
		ToolBase: ToolBase{Type: ToolTypeFunction},
	}
	require.Equal(t, ToolTypeFunction, funcTool.GetType())

	browserTool := &BrowserSearchTool{
		ToolBase: ToolBase{Type: ToolTypeBrowserSearch},
	}
	require.Equal(t, ToolTypeBrowserSearch, browserTool.GetType())

	codeTool := &CodeInterpreterTool{
		ToolBase: ToolBase{Type: ToolTypeCodeInterpreter},
	}
	require.Equal(t, ToolTypeCodeInterpreter, codeTool.GetType())
}

func TestRoundTripMarshaling(t *testing.T) {
	// Test that tools can be marshaled and unmarshaled without data loss
	originalTools := ToolSlice{
		&FunctionTool{
			ToolBase:    ToolBase{Type: ToolTypeFunction},
			Name:        "get_weather",
			Description: ptr.To("Get weather information"),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string"},
				},
			},
			Strict: ptr.To(false),
		},
		&BrowserSearchTool{
			ToolBase: ToolBase{Type: ToolTypeBrowserSearch},
			UserLocation: &UserLocation{
				Type:    "location",
				Country: ptr.To("US"),
			},
			SearchContextSize: ptr.To("medium"),
		},
		&CodeInterpreterTool{
			ToolBase:  ToolBase{Type: ToolTypeCodeInterpreter},
			Container: "test_container",
		},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(originalTools)
	require.NoError(t, err)

	// Unmarshal back to ToolSlice
	var unmarshaledTools ToolSlice
	err = json.Unmarshal(jsonData, &unmarshaledTools)
	require.NoError(t, err)

	// Verify tools match
	require.Len(t, unmarshaledTools, 3)

	// Check FunctionTool
	funcTool, ok := unmarshaledTools[0].(*FunctionTool)
	require.True(t, ok)
	require.Equal(t, "get_weather", funcTool.Name)
	require.Equal(t, "Get weather information", ptr.Deref(funcTool.Description))
	require.False(t, ptr.Deref(funcTool.Strict))

	// Check BrowserSearchTool
	browserTool, ok := unmarshaledTools[1].(*BrowserSearchTool)
	require.True(t, ok)
	require.Equal(t, "US", ptr.Deref(browserTool.UserLocation.Country))
	require.Equal(t, "medium", ptr.Deref(browserTool.SearchContextSize))

	// Check CodeInterpreterTool
	codeTool, ok := unmarshaledTools[2].(*CodeInterpreterTool)
	require.True(t, ok)
	require.Equal(t, "test_container", codeTool.Container)
}
