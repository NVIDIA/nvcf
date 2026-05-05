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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStateManager creates a StateManager backed by a file in a temp dir.
func newTestStateManager(t *testing.T) *StateManager {
	t.Helper()
	dir := t.TempDir()
	sm := &StateManager{
		statePath: filepath.Join(dir, "state.json"),
		state:     &State{},
		logger:    nil, // logging.NewLogger() calls os.UserHomeDir; skip for unit tests
	}
	return sm
}

// TestState_SelfHostedAuth_RoundTrip verifies that a State containing a
// non-nil SelfHostedAuth (with token, expiry, and fingerprint) survives a
// Save+Load round-trip with all fields intact.
func TestState_SelfHostedAuth_RoundTrip(t *testing.T) {
	sm := newTestStateManager(t)

	expiry := time.Now().Add(1 * time.Hour).UTC().Round(time.Second)
	sm.state = &State{
		SelfHostedAuth: &SelfHostedAuth{
			Token:     "tok-abc123",
			ExpiresAt: expiry,
			Fingerprint: &FingerprintRef{
				IssuerURL:       "https://cp.example/",
				JWKSKid:         "key-2026",
				APIKeysEndpoint: "https://cp.example/api-keys",
			},
		},
	}

	require.NoError(t, sm.Save())

	// Load into a fresh manager pointed at the same file.
	sm2 := &StateManager{
		statePath: sm.statePath,
		state:     &State{},
	}
	require.NoError(t, sm2.Load())

	got := sm2.GetState().SelfHostedAuth
	require.NotNil(t, got)
	assert.Equal(t, "tok-abc123", got.Token)
	assert.Equal(t, expiry, got.ExpiresAt.UTC().Round(time.Second))
	require.NotNil(t, got.Fingerprint)
	assert.Equal(t, "https://cp.example/", got.Fingerprint.IssuerURL)
	assert.Equal(t, "key-2026", got.Fingerprint.JWKSKid)
	assert.Equal(t, "https://cp.example/api-keys", got.Fingerprint.APIKeysEndpoint)
}

// TestState_LegacyFile_NoSelfHostedAuth verifies that loading a state file
// written by an older CLI (without the selfHostedAuth field) produces
// SelfHostedAuth == nil — no panic, no parse error.
func TestState_LegacyFile_NoSelfHostedAuth(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	// Write a minimal "old-format" state file without selfHostedAuth.
	legacy := map[string]any{
		"token":        "old-token",
		"lastModified": time.Now().UTC(),
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0600))

	sm := &StateManager{
		statePath: statePath,
		state:     &State{},
	}
	require.NoError(t, sm.Load())

	s := sm.GetState()
	assert.Nil(t, s.SelfHostedAuth, "legacy state file should produce nil SelfHostedAuth")
	assert.Equal(t, "old-token", s.Token, "other fields should still load correctly")
}

// TestState_SelfHostedAuth_NilFingerprint verifies that SelfHostedAuth with
// a nil Fingerprint (i.e., token cached without fingerprint) also round-trips
// cleanly — omitempty on the fingerprint field means it is absent in JSON.
func TestState_SelfHostedAuth_NilFingerprint(t *testing.T) {
	sm := newTestStateManager(t)
	sm.state = &State{
		SelfHostedAuth: &SelfHostedAuth{
			Token:       "tok-no-fp",
			ExpiresAt:   time.Now().Add(30 * time.Minute).UTC().Round(time.Second),
			Fingerprint: nil,
		},
	}

	require.NoError(t, sm.Save())

	sm2 := &StateManager{
		statePath: sm.statePath,
		state:     &State{},
	}
	require.NoError(t, sm2.Load())

	got := sm2.GetState().SelfHostedAuth
	require.NotNil(t, got)
	assert.Equal(t, "tok-no-fp", got.Token)
	assert.Nil(t, got.Fingerprint)
}

// TestState_Save_CreatesDirectoryIfNeeded verifies that Save creates
// intermediate directories (mirrors the production code path for first-run).
func TestState_Save_CreatesDirectoryIfNeeded(t *testing.T) {
	dir := t.TempDir()
	nestedPath := filepath.Join(dir, "nested", "deep", "state.json")

	sm := &StateManager{
		statePath: nestedPath,
		state:     &State{Token: "tok"},
	}
	require.NoError(t, sm.Save())

	_, err := os.Stat(nestedPath)
	require.NoError(t, err, "state file should exist after Save")
}

// TestState_Load_MissingFile verifies that Load on a non-existent path
// succeeds and initialises an empty State (no error).
func TestState_Load_MissingFile(t *testing.T) {
	sm := &StateManager{
		statePath: filepath.Join(t.TempDir(), "does-not-exist.json"),
		state:     &State{},
	}
	require.NoError(t, sm.Load())
	// State is non-nil after load
	assert.NotNil(t, sm.GetState())
}
