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

package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nvcf-cli/internal/logging"
)

// SelfHostedAuth caches the admin token and the control-plane fingerprint the
// token was minted for. Persisted alongside the rest of State so that re-runs
// of `nvcf self-hosted up` can skip the API-Keys init step when the
// fingerprint of the currently-running control plane matches the one stored
// here. omitempty means old state files (without this field) load cleanly with
// SelfHostedAuth == nil.
type SelfHostedAuth struct {
	Token       string         `json:"token,omitempty"`
	ExpiresAt   time.Time      `json:"expiresAt,omitempty"`
	Fingerprint *FingerprintRef `json:"fingerprint,omitempty"`
}

// FingerprintRef is the persistent form of auth.Fingerprint. It mirrors
// auth.Fingerprint field-for-field so that the state package does not import
// the auth package (avoiding a dependency inversion).
type FingerprintRef struct {
	IssuerURL       string `json:"issuerURL"`
	JWKSKid         string `json:"jwksKid"`
	APIKeysEndpoint string `json:"apiKeysEndpoint"`
}

// State represents the persistent state of the CLI
type State struct {
	// Configuration
	ConfigFile     string `json:"configFile,omitempty"`
	KubeconfigPath string `json:"kubeconfigPath,omitempty"`
	ClusterMode    bool   `json:"clusterMode,omitempty"`

	// Authentication
	Token            string    `json:"token,omitempty"`
	TokenExpiration  time.Time `json:"tokenExpiration,omitempty"`
	APIKey           string    `json:"apiKey,omitempty"`
	APIKeyExpiration time.Time `json:"apiKeyExpiration,omitempty"`

	// Current Function Context
	FunctionID   string `json:"functionId,omitempty"`
	VersionID    string `json:"versionId,omitempty"`
	FunctionName string `json:"functionName,omitempty"`

	// Current Task Context (NVCT)
	TaskID   string `json:"taskId,omitempty"`
	TaskName string `json:"taskName,omitempty"`

	// NVCT API key (separate from the NVCF API key — different issuer/scopes)
	NVCTAPIKey           string    `json:"nvctApiKey,omitempty"`
	NVCTAPIKeyExpiration time.Time `json:"nvctApiKeyExpiration,omitempty"`

	// Last used endpoints and settings
	LastBaseURL   string `json:"lastBaseUrl,omitempty"`
	LastInvokeURL string `json:"lastInvokeUrl,omitempty"`
	LastAccount   string `json:"lastAccount,omitempty"`

	// Self-hosted auth cache: admin token + control-plane fingerprint.
	// nil when no self-hosted installation has been run, or when the
	// state file was written by an older CLI that didn't have this field.
	SelfHostedAuth *SelfHostedAuth `json:"selfHostedAuth,omitempty"`

	// Metadata
	LastModified time.Time `json:"lastModified"`
	CLIVersion   string    `json:"cliVersion,omitempty"`
}

// StateManager handles loading and saving persistent state
type StateManager struct {
	statePath string
	state     *State
	logger    *logging.Logger
}

// NewStateManager creates a new state manager
func NewStateManager() *StateManager {
	return NewStateManagerForConfig("")
}

// NewStateManagerForConfig creates a state manager for a specific config context
func NewStateManagerForConfig(configName string) *StateManager {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logging.Warning("Could not determine home directory, using current directory for state file")
		homeDir = "."
	}

	// Create different state files for different config contexts
	var statePath string
	if configName == "" || configName == "default" {
		statePath = filepath.Join(homeDir, ".nvcf-cli.state")
	} else {
		// Remove extension and path from config name to create a clean context name
		contextName := filepath.Base(configName)
		if ext := filepath.Ext(contextName); ext != "" {
			contextName = contextName[:len(contextName)-len(ext)]
		}
		// ".nvcf-cli" is the default config name; map it back to the default state path
		if contextName == ".nvcf-cli" {
			statePath = filepath.Join(homeDir, ".nvcf-cli.state")
		} else {
			statePath = filepath.Join(homeDir, fmt.Sprintf(".nvcf-cli.%s.state", contextName))
		}
	}

	return &StateManager{
		statePath: statePath,
		state:     &State{},
		logger:    logging.NewLogger(),
	}
}

// Load reads the state from disk
func (sm *StateManager) Load() error {
	if _, err := os.Stat(sm.statePath); os.IsNotExist(err) {
		// State file doesn't exist, start with empty state
		sm.state = &State{
			LastModified: time.Now(),
		}
		return nil
	}

	data, err := os.ReadFile(sm.statePath)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, sm.state); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	return nil
}

// Save writes the state to disk
func (sm *StateManager) Save() error {
	sm.state.LastModified = time.Now()

	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Ensure the directory exists
	dir := filepath.Dir(sm.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to temporary file first, then rename for atomic operation
	tempPath := sm.statePath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tempPath, sm.statePath); err != nil {
		os.Remove(tempPath) // Clean up temp file
		return fmt.Errorf("failed to save state file: %w", err)
	}

	return nil
}

// GetState returns the current state
func (sm *StateManager) GetState() *State {
	return sm.state
}

// SetFunction updates the current function context
func (sm *StateManager) SetFunction(functionID, versionID, functionName string) {
	sm.state.FunctionID = functionID
	sm.state.VersionID = versionID
	sm.state.FunctionName = functionName
}

// ClearFunction clears the current function context
func (sm *StateManager) ClearFunction() {
	sm.state.FunctionID = ""
	sm.state.VersionID = ""
	sm.state.FunctionName = ""
}

// SetTokens updates authentication tokens
func (sm *StateManager) SetTokens(token, apiKey string, tokenExp, apiKeyExp time.Time) {
	sm.state.Token = token
	sm.state.APIKey = apiKey
	sm.state.TokenExpiration = tokenExp
	sm.state.APIKeyExpiration = apiKeyExp
}

// ClearTokens clears authentication tokens
func (sm *StateManager) ClearTokens() {
	sm.state.Token = ""
	sm.state.APIKey = ""
	sm.state.TokenExpiration = time.Time{}
	sm.state.APIKeyExpiration = time.Time{}
	sm.state.NVCTAPIKey = ""
	sm.state.NVCTAPIKeyExpiration = time.Time{}
}

// SetConfig updates configuration settings
func (sm *StateManager) SetConfig(configFile, kubeconfigPath string, clusterMode bool) {
	sm.state.ConfigFile = configFile
	sm.state.KubeconfigPath = kubeconfigPath
	sm.state.ClusterMode = clusterMode
}

// SetEndpoints updates API endpoints
func (sm *StateManager) SetEndpoints(baseURL, invokeURL, account string) {
	sm.state.LastBaseURL = baseURL
	sm.state.LastInvokeURL = invokeURL
	sm.state.LastAccount = account
}

// IsTokenValid checks if the current token is still valid
func (sm *StateManager) IsTokenValid() bool {
	if sm.state.Token == "" {
		return false
	}
	if sm.state.TokenExpiration.IsZero() {
		// If no expiration set, assume it's valid for backwards compatibility
		return true
	}
	return time.Now().Before(sm.state.TokenExpiration)
}

// IsAPIKeyValid checks if the current API key is still valid
func (sm *StateManager) IsAPIKeyValid() bool {
	if sm.state.APIKey == "" {
		return false
	}
	if sm.state.APIKeyExpiration.IsZero() {
		// If no expiration set, assume it's valid for backwards compatibility
		return true
	}
	return time.Now().Before(sm.state.APIKeyExpiration)
}

// HasFunction checks if there's a current function context
func (sm *StateManager) HasFunction() bool {
	return sm.state.FunctionID != "" && sm.state.VersionID != ""
}

// SetTask updates the current task context (NVCT)
func (sm *StateManager) SetTask(taskID, taskName string) {
	sm.state.TaskID = taskID
	sm.state.TaskName = taskName
}

// ClearTask clears the current task context
func (sm *StateManager) ClearTask() {
	sm.state.TaskID = ""
	sm.state.TaskName = ""
}

// HasTask checks if there's a current task context
func (sm *StateManager) HasTask() bool {
	return sm.state.TaskID != ""
}

// SetNVCTAPIKey stores an NVCT-scoped API key and its expiration.
func (sm *StateManager) SetNVCTAPIKey(key string, expiration time.Time) {
	sm.state.NVCTAPIKey = key
	sm.state.NVCTAPIKeyExpiration = expiration
}

// ClearNVCTAPIKey removes the stored NVCT API key.
func (sm *StateManager) ClearNVCTAPIKey() {
	sm.state.NVCTAPIKey = ""
	sm.state.NVCTAPIKeyExpiration = time.Time{}
}

// HasNVCTAPIKey reports whether an NVCT API key is stored.
func (sm *StateManager) HasNVCTAPIKey() bool {
	return sm.state.NVCTAPIKey != ""
}

// PrintStatus prints the current state in a human-readable format
func (sm *StateManager) PrintStatus() {
	state := sm.state

	sm.logger.Info("Current CLI State:")
	fmt.Printf("  Config File: %s\n", valueOrDefault(state.ConfigFile, "(default)"))
	fmt.Printf("  Cluster Mode: %v\n", state.ClusterMode)
	if state.ClusterMode {
		fmt.Printf("  Kubeconfig: %s\n", valueOrDefault(state.KubeconfigPath, "(default)"))
	}

	fmt.Printf("\n  Authentication:\n")
	if state.Token != "" {
		fmt.Printf("    Admin Token: %s (expires: %s)\n",
			maskToken(state.Token),
			formatTime(state.TokenExpiration))
	} else {
		fmt.Printf("    Admin Token: (not set)\n")
	}

	if state.APIKey != "" {
		fmt.Printf("    API Key: %s (expires: %s)\n",
			maskToken(state.APIKey),
			formatTime(state.APIKeyExpiration))
	} else {
		fmt.Printf("    API Key: (not set)\n")
	}

	fmt.Printf("\n  Current Function:\n")
	if sm.HasFunction() {
		fmt.Printf("    Function ID: %s\n", state.FunctionID)
		fmt.Printf("    Version ID: %s\n", state.VersionID)
		fmt.Printf("    Name: %s\n", valueOrDefault(state.FunctionName, "(unknown)"))
	} else {
		fmt.Printf("    (no function selected)\n")
	}

	fmt.Printf("\n  Current Task:\n")
	if sm.HasTask() {
		fmt.Printf("    Task ID: %s\n", state.TaskID)
		fmt.Printf("    Name: %s\n", valueOrDefault(state.TaskName, "(unknown)"))
	} else {
		fmt.Printf("    (no task selected)\n")
	}

	fmt.Printf("\n  Last Modified: %s\n", state.LastModified.Format(time.RFC3339))
}

// Helper functions
func valueOrDefault(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func maskToken(token string) string {
	if len(token) <= 20 {
		return token[:4] + "..." + token[len(token)-4:]
	}
	return token[:10] + "..." + token[len(token)-10:]
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}
	return t.Format("2006-01-02 15:04:05")
}

// Global state manager instance (default context)
var DefaultStateManager = NewStateManager()

// GetStateManagerForCurrentConfig returns a state manager for the current config context
// This function should be called from commands that need config-aware state management
func GetStateManagerForCurrentConfig() *StateManager {
	// This will be set by the command initialization
	// For now, we'll use a simple approach - commands should call this explicitly
	return DefaultStateManager
}

// GetStateManagerForConfig returns a state manager for a specific config context
func GetStateManagerForConfig(configName string) *StateManager {
	return NewStateManagerForConfig(configName)
}

// Convenience functions using the default state manager
// Note: For config-aware operations, commands should use GetStateManagerForCurrentConfig()
func Load() error                       { return DefaultStateManager.Load() }
func Save() error                       { return DefaultStateManager.Save() }
func GetState() *State                  { return DefaultStateManager.GetState() }
func SetFunction(fid, vid, name string) { DefaultStateManager.SetFunction(fid, vid, name) }
func ClearFunction()                    { DefaultStateManager.ClearFunction() }
func SetTokens(token, apiKey string, tExp, aExp time.Time) {
	DefaultStateManager.SetTokens(token, apiKey, tExp, aExp)
}
func ClearTokens() { DefaultStateManager.ClearTokens() }
func SetConfig(configFile, kubeconfigPath string, clusterMode bool) {
	DefaultStateManager.SetConfig(configFile, kubeconfigPath, clusterMode)
}
func SetEndpoints(baseURL, invokeURL, account string) {
	DefaultStateManager.SetEndpoints(baseURL, invokeURL, account)
}
func HasFunction() bool                  { return DefaultStateManager.HasFunction() }
func IsTokenValid() bool                  { return DefaultStateManager.IsTokenValid() }
func IsAPIKeyValid() bool                 { return DefaultStateManager.IsAPIKeyValid() }
func PrintStatus()                        { DefaultStateManager.PrintStatus() }
func SetTask(taskID, taskName string)                    { DefaultStateManager.SetTask(taskID, taskName) }
func ClearTask()                                         { DefaultStateManager.ClearTask() }
func HasTask() bool                                      { return DefaultStateManager.HasTask() }
func SetNVCTAPIKey(key string, exp time.Time)            { DefaultStateManager.SetNVCTAPIKey(key, exp) }
func ClearNVCTAPIKey()                                   { DefaultStateManager.ClearNVCTAPIKey() }
func HasNVCTAPIKey() bool                                { return DefaultStateManager.HasNVCTAPIKey() }
