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

package tools

// ToolEnvelope describes the concrete tool-call envelope contract used for a
// request. It includes both the begin/end markers and the JSON schema that
// model output is expected to match.
type ToolEnvelope struct {
	BeginMarker string `json:"begin_marker,omitempty"`
	EndMarker   string `json:"end_marker,omitempty"`
	Schema      string `json:"schema,omitempty"`
}

// ToolEnvelopeProvider provides a concrete tool-call envelope schema for a
// ParseConfig.
//
// This is intentionally optional: not all ParseConfig implementations validate
// against a JSON schema, and some tool formats are defined without a single
// concrete schema.
type ToolEnvelopeProvider interface {
	ToolEnvelope(params Params) (ToolEnvelope, bool)
}

// ToolEnvelopes returns the concrete tool-call envelope schema(s) associated
// with a ParseConfig.
//
// This is best-effort: some ParseConfig implementations do not expose a
// concrete schema. For those, this returns a marker-only envelope (empty schema).
func ToolEnvelopes(cfg ParseConfig, params Params) []ToolEnvelope {
	if cfg == nil {
		return nil
	}

	if p, ok := cfg.(ToolEnvelopeProvider); ok {
		if env, ok := p.ToolEnvelope(params); ok {
			return []ToolEnvelope{env}
		}
	}

	return []ToolEnvelope{
		{
			BeginMarker: cfg.ToolUseBeginMarker(),
			EndMarker:   cfg.ToolUseEndMarker(),
		},
	}
}
