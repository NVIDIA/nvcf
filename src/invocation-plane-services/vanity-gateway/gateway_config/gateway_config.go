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

package config

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	rc "ai-api-gateway-service/internal/reloadableconfig"
)

type SessionTimeoutSeconds int

type ModelFunctionDetails struct {
	ModelName                      string                `json:"modelName"`
	FunctionID                     string                `json:"functionID"`
	FunctionVersionID              string                `json:"functionVersionID"`
	OutgoingPathOverride           string                `json:"outgoingPathOverride"`
	UsePexec                       bool                  `json:"usePexec"`
	SessionTimeout                 SessionTimeoutSeconds `json:"sessionTimeout,omitempty"`
	EOL                            time.Time             `json:"eol,omitempty"`            // RFC3339 timestamp (full ISO 8601)
	OfflineMessage                 string                `json:"offlineMessage,omitempty"` // non-empty = endpoint is offline
	TooManyRequestsMessage         string                `json:"tooManyRequestsMessage"`
	ShadowModelName                string                `json:"shadowModelName,omitempty"`
	ShadowModelNames               []string              `json:"shadowModelNames,omitempty"`
	ShadowPercentage               *int                  `json:"shadowPercentage,omitempty"`               // 1-100 when set; omitted defaults to 100
	ShadowCancelOnClientDisconnect bool                  `json:"shadowCancelOnClientDisconnect,omitempty"` // cancel shadow when primary completes; default false
}

type PathFunctionDetails struct {
	Path                    string                 `json:"path"` // incoming path
	OutgoingPathOverride    *string                `json:"outgoingPathOverride"`
	FunctionID              string                 `json:"functionID"`
	FunctionVersionID       string                 `json:"functionVersionID"`
	UsePexec                bool                   `json:"usePexec"`
	SessionTimeout          *SessionTimeoutSeconds `json:"sessionTimeout,omitempty"`
	EOL                     time.Time              `json:"eol,omitempty"`            // RFC3339 timestamp (full ISO 8601)
	OfflineMessage          string                 `json:"offlineMessage,omitempty"` // non-empty = endpoint is offline
	ShadowFunctionID        string                 `json:"shadowFunctionID,omitempty"`
	ShadowFunctionVersionID string                 `json:"shadowFunctionVersionID,omitempty"`
	ShadowPercentage        *int                   `json:"shadowPercentage,omitempty"` // unsupported on vanity routes; rejected during validation
	sessionTimeoutPresent   bool
}

func (p *PathFunctionDetails) UnmarshalJSON(data []byte) error {
	type pathFunctionDetailsAlias PathFunctionDetails

	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	var alias pathFunctionDetailsAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	*p = PathFunctionDetails(alias)
	_, p.sessionTimeoutPresent = fields["sessionTimeout"]
	return nil
}

type VanityEntry struct {
	Host  string                         `json:"host"`
	Paths map[string]PathFunctionDetails `json:"paths"`
}

type V2Config struct {
	OpenAI struct {
		Host             string                          `json:"host"`
		ChatCompletions  map[string]ModelFunctionDetails `json:"chatCompletions"`
		Completions      map[string]ModelFunctionDetails `json:"completions"`
		Embeddings       map[string]ModelFunctionDetails `json:"embeddings"`
		Responses        map[string]ModelFunctionDetails `json:"responses"`
		ImageGenerations map[string]ModelFunctionDetails `json:"imageGenerations"`
		ImageEdits       map[string]ModelFunctionDetails `json:"imageEdits"`
		ImageVariations  map[string]ModelFunctionDetails `json:"imageVariations"`
	} `json:"openai"`
	Vanity map[string]VanityEntry `json:"vanity"`
}

// sharedNotifications is a package-level channel shared across all config instances.
// This ensures that notifications sent after a config reload reach any waiting goroutine.
var sharedNotifications = make(chan struct{}, 1)

func notifySharedReload() {
	select {
	case sharedNotifications <- struct{}{}:
	default:
	}
}

type GatewayConfig struct {
	V2Config `json:"v2config"`
}

func uniqueShadowModelNames(legacyModelName string, modelNames []string) ([]string, error) {
	seen := make(map[string]struct{}, len(modelNames)+1)
	result := make([]string, 0, len(modelNames)+1)
	if legacyModelName != "" {
		seen[legacyModelName] = struct{}{}
		result = append(result, legacyModelName)
	}
	for _, modelName := range modelNames {
		if modelName == "" {
			return nil, fmt.Errorf("shadowModelNames cannot contain empty model names")
		}
		if _, ok := seen[modelName]; ok {
			return nil, fmt.Errorf("duplicate shadow target %q", modelName)
		}
		seen[modelName] = struct{}{}
		result = append(result, modelName)
	}
	return result, nil
}

func validateOpenAIShadowConfig(location string, entry ModelFunctionDetails) ([]string, error) {
	shadowTargets, err := uniqueShadowModelNames(entry.ShadowModelName, entry.ShadowModelNames)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}

	if entry.ShadowPercentage != nil {
		pct := *entry.ShadowPercentage
		if pct < 1 || pct > 100 {
			return nil, fmt.Errorf("%s: shadowPercentage must be between 1 and 100", location)
		}
	}

	if len(shadowTargets) == 0 {
		if entry.ShadowPercentage != nil {
			return nil, fmt.Errorf("%s: shadowPercentage requires at least one shadow target", location)
		}
		if entry.ShadowCancelOnClientDisconnect {
			return nil, fmt.Errorf("%s: shadowCancelOnClientDisconnect requires at least one shadow target", location)
		}
	}

	return shadowTargets, nil
}

func (c *GatewayConfig) Validate() error {
	for sectionName, entries := range c.openAISections() {
		if err := validateOpenAISection(sectionName, entries); err != nil {
			return err
		}
	}

	return c.validateVanityConfig()
}

func (c *GatewayConfig) openAISections() map[string]map[string]ModelFunctionDetails {
	return map[string]map[string]ModelFunctionDetails{
		"chatCompletions":  c.OpenAI.ChatCompletions,
		"completions":      c.OpenAI.Completions,
		"embeddings":       c.OpenAI.Embeddings,
		"responses":        c.OpenAI.Responses,
		"imageGenerations": c.OpenAI.ImageGenerations,
		"imageEdits":       c.OpenAI.ImageEdits,
		"imageVariations":  c.OpenAI.ImageVariations,
	}
}

func validateOpenAISection(sectionName string, entries map[string]ModelFunctionDetails) error {
	modelNames, err := collectOpenAIModelNames(sectionName, entries)
	if err != nil {
		return err
	}

	for modelKey, entry := range entries {
		if entry.SessionTimeout < 0 {
			return fmt.Errorf("openai.%s.%s: sessionTimeout must be greater than or equal to 0", sectionName, modelKey)
		}
	}

	if isMultipartOpenAISection(sectionName) {
		return validateMultipartOpenAISection(sectionName, entries)
	}
	return validateOpenAIShadowTargets(sectionName, entries, modelNames)
}

func collectOpenAIModelNames(sectionName string, entries map[string]ModelFunctionDetails) (map[string]struct{}, error) {
	modelNames := make(map[string]struct{}, len(entries))
	for entryKey, entry := range entries {
		if entry.ModelName == "" {
			return nil, fmt.Errorf("openai.%s.%s: modelName is required", sectionName, entryKey)
		}
		modelNames[entry.ModelName] = struct{}{}
	}
	return modelNames, nil
}

func isMultipartOpenAISection(sectionName string) bool {
	// Shadow replay rewrites JSON bodies, so it cannot support multipart image requests.
	return sectionName == "imageEdits" || sectionName == "imageVariations"
}

func validateMultipartOpenAISection(sectionName string, entries map[string]ModelFunctionDetails) error {
	for modelKey, entry := range entries {
		if entry.ShadowModelName != "" || len(entry.ShadowModelNames) > 0 || entry.ShadowPercentage != nil || entry.ShadowCancelOnClientDisconnect {
			return fmt.Errorf("openai.%s.%s: shadow config is unsupported for multipart image endpoints", sectionName, modelKey)
		}
	}
	return nil
}

func validateOpenAIShadowTargets(sectionName string, entries map[string]ModelFunctionDetails, modelNames map[string]struct{}) error {
	for modelKey, entry := range entries {
		location := "openai." + sectionName + "." + modelKey
		shadowTargets, err := validateOpenAIShadowConfig(location, entry)
		if err != nil {
			return err
		}
		if err := validateShadowTargetNames(location, sectionName, entry.ModelName, shadowTargets, modelNames); err != nil {
			return err
		}
	}
	return nil
}

func validateShadowTargetNames(location string, sectionName string, modelName string, shadowTargets []string, modelNames map[string]struct{}) error {
	for _, shadowTarget := range shadowTargets {
		if shadowTarget == modelName {
			return fmt.Errorf("%s: shadow target cannot reference the same model", location)
		}
		if _, ok := modelNames[shadowTarget]; !ok {
			return fmt.Errorf("%s: shadow target must reference another model in openai.%s", location, sectionName)
		}
	}
	return nil
}

func (c *GatewayConfig) validateVanityConfig() error {
	for vanityName, vanity := range c.Vanity {
		for pathKey, path := range vanity.Paths {
			if path.sessionTimeoutPresent || path.SessionTimeout != nil {
				return fmt.Errorf("vanity.%s.paths.%s: sessionTimeout is unsupported for vanity routes", vanityName, pathKey)
			}
			if path.ShadowFunctionID != "" || path.ShadowFunctionVersionID != "" || path.ShadowPercentage != nil {
				return fmt.Errorf("vanity.%s.paths.%s: shadow config is unsupported for vanity routes", vanityName, pathKey)
			}
		}
	}

	return nil
}

func SetupConfigWithConfigPath(path string) (rc.ReloadableConfig[GatewayConfig], error) {
	config, err := rc.SetupConfig[GatewayConfig](path,
		rc.WithValidateFunc(func(c *GatewayConfig) error {
			return c.Validate()
		}),
		rc.WithPostLoadFunc(func(c *GatewayConfig) error {
			notifySharedReload()
			return nil
		}))
	return config, err
}

func (c *GatewayConfig) WaitForNotification(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sharedNotifications:
		return nil
	}
}
