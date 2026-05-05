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

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validSecretsJSON = `{
  "kv": {
    "nats": {
      "api-auth": {
        "user": "test-user-key",
        "user-seed": "test-user-seed"
      }
    },
    "tokens": {
      "icms": "test-jwt-token",
      "reval": "test-reval-token"
    }
  }
}`

const invalidSecretsJSON = `{
  "kv": {
    "nats": {
      "api-auth": {
        "user": "test-user-key"
        "user-seed": "test-user-seed"
      }
    }
  }
}`

const incompleteSecretsJSON = `{
  "kv": {
    "nats": {
      "api-auth": {
        "user": "test-user-key"
      }
    }
  }
}`

func TestNewSelfManagedSecretsFetcher(t *testing.T) {
	ctx := context.Background()

	t.Run("successful creation with valid file", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, validSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, healthCheck, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)

		assert.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.NotNil(t, healthCheck)
		assert.NotNil(t, fetcher.secretJSONFetcher)
	})

	t.Run("error with non-existent file", func(t *testing.T) {
		nonExistentPath := "/path/that/does/not/exist/secrets.json"

		fetcher, healthCheck, err := NewSelfManagedSecretsFetcher(ctx, "mock", nonExistentPath)

		assert.Error(t, err)
		assert.Nil(t, fetcher)
		assert.NotNil(t, healthCheck)
	})

	t.Run("error with empty file path", func(t *testing.T) {
		invalidPath := ""

		fetcher, healthCheck, err := NewSelfManagedSecretsFetcher(ctx, "mock", invalidPath)

		assert.Error(t, err)
		assert.Nil(t, fetcher)
		assert.NotNil(t, healthCheck)
	})
}

func TestSelfManagedSecretsFetcher_getSelfManagedSecrets(t *testing.T) {
	ctx := context.Background()

	t.Run("successful parsing of valid JSON", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, validSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		secrets, err := fetcher.getSelfManagedSecrets(ctx)

		assert.NoError(t, err)
		assert.NotNil(t, secrets)
		assert.Equal(t, "test-user-key", secrets.KV.NATS.APIAuth.User)
		assert.Equal(t, "test-user-seed", secrets.KV.NATS.APIAuth.UserSeed)
		assert.Equal(t, "test-jwt-token", secrets.KV.Tokens.ICMS)
	})

	t.Run("error with invalid JSON", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, invalidSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		secrets, err := fetcher.getSelfManagedSecrets(ctx)

		assert.Error(t, err)
		assert.Nil(t, secrets)
	})

	t.Run("successful parsing with incomplete JSON", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, incompleteSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		secrets, err := fetcher.getSelfManagedSecrets(ctx)

		assert.NoError(t, err)
		assert.NotNil(t, secrets)
		assert.Equal(t, "test-user-key", secrets.KV.NATS.APIAuth.User)
		assert.Empty(t, secrets.KV.NATS.APIAuth.UserSeed)
		assert.Empty(t, secrets.KV.Tokens.ICMS)
	})
}

func TestSelfManagedSecretsFetcher_FetchNATSSecrets(t *testing.T) {
	ctx := context.Background()

	t.Run("successful NATS secrets fetch", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, validSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		natsSecrets, err := fetcher.FetchNATSSecrets(ctx)

		assert.NoError(t, err)
		assert.Equal(t, "test-user-key", natsSecrets.APIAuth.User)
		assert.Equal(t, "test-user-seed", natsSecrets.APIAuth.UserSeed)
	})

	t.Run("error when JSON is invalid", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, invalidSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		natsSecrets, err := fetcher.FetchNATSSecrets(ctx)

		assert.Error(t, err)
		assert.Equal(t, NATSSecrets{}, natsSecrets)
	})

	t.Run("empty NATS secrets when section is incomplete", func(t *testing.T) {
		incompleteJSON := `{
		  "kv": {
		    "tokens": {
		      "icms": "test-jwt-token"
		    }
		  }
		}`

		tempFile := createTempSecretsFile(t, incompleteJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		natsSecrets, err := fetcher.FetchNATSSecrets(ctx)

		assert.NoError(t, err)
		assert.Empty(t, natsSecrets.APIAuth.User)
		assert.Empty(t, natsSecrets.APIAuth.UserSeed)
	})
}

func TestSelfManagedSecretsFetcher_FetchToken(t *testing.T) {
	ctx := context.Background()

	t.Run("successful token fetch", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, validSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		token, err := fetcher.FetchToken(ctx)

		assert.NoError(t, err)
		assert.Equal(t, "test-jwt-token", token)
	})

	t.Run("error when JSON is invalid", func(t *testing.T) {
		tempFile := createTempSecretsFile(t, invalidSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		token, err := fetcher.FetchToken(ctx)

		assert.Error(t, err)
		assert.Empty(t, token)
	})

	t.Run("empty token when ICMS section is missing", func(t *testing.T) {
		incompleteJSON := `{
		  "kv": {
		    "nats": {
		      "api-auth": {
		        "user": "test-user-key",
		        "user-seed": "test-user-seed"
		      }
		    }
		  }
		}`

		tempFile := createTempSecretsFile(t, incompleteJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		token, err := fetcher.FetchToken(ctx)

		assert.NoError(t, err)
		assert.Empty(t, token)
	})
}

func TestSecretStructures_JSONMarshaling(t *testing.T) {
	t.Run("marshal and unmarshal selfManagedSecrets", func(t *testing.T) {
		original := selfManagedSecrets{
			KV: KVSecrets{
				NATS: NATSSecrets{
					APIAuth: NATSAPIAuthSecrets{
						User:     "test-user",
						UserSeed: "test-seed",
					},
				},
				Tokens: TokensSecrets{
					ICMS:  "jwt-token",
					ReVal: "reval-token",
				},
			},
		}

		jsonData, err := json.Marshal(original)
		require.NoError(t, err)

		var unmarshaled selfManagedSecrets
		err = json.Unmarshal(jsonData, &unmarshaled)
		require.NoError(t, err)

		assert.Equal(t, original.KV.NATS.APIAuth.User, unmarshaled.KV.NATS.APIAuth.User)
		assert.Equal(t, original.KV.NATS.APIAuth.UserSeed, unmarshaled.KV.NATS.APIAuth.UserSeed)
		assert.Equal(t, original.KV.Tokens.ICMS, unmarshaled.KV.Tokens.ICMS)
		assert.Equal(t, original.KV.Tokens.ReVal, unmarshaled.KV.Tokens.ReVal)
	})

	t.Run("JSON field tags are correct", func(t *testing.T) {
		jsonData := []byte(validSecretsJSON)
		var secrets selfManagedSecrets

		err := json.Unmarshal(jsonData, &secrets)
		require.NoError(t, err)

		assert.Equal(t, "test-user-key", secrets.KV.NATS.APIAuth.User)
		assert.Equal(t, "test-user-seed", secrets.KV.NATS.APIAuth.UserSeed)
		assert.Equal(t, "test-jwt-token", secrets.KV.Tokens.ICMS)
	})
}

func TestSelfManagedSecretsFetcher_LargeSecretsFile(t *testing.T) {
	ctx := context.Background()

	t.Run("reads file larger than old 1KB default without truncation", func(t *testing.T) {
		// The old KeyFileFetcher had a 1024-byte default max size which silently
		// truncated the secrets JSON. This test verifies the new FileFetcher with
		// 1MB max reads the full content.
		largeToken := strings.Repeat("a", 2048) // push well past old 1KB limit
		largeSecretsJSON := fmt.Sprintf(`{
		  "kv": {
		    "nats": {
		      "api-auth": {
		        "user": "test-user-key",
		        "user-seed": "test-user-seed"
		      }
		    },
		    "tokens": {
		      "icms": "%s"
		    }
		  }
		}`, largeToken)

		assert.Greater(t, len(largeSecretsJSON), 1024, "test JSON must exceed old 1KB default")

		tempFile := createTempSecretsFile(t, largeSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		token, err := fetcher.FetchToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, largeToken, token, "token must not be truncated")

		natsSecrets, err := fetcher.FetchNATSSecrets(ctx)
		require.NoError(t, err)
		assert.Equal(t, "test-user-key", natsSecrets.APIAuth.User)
		assert.Equal(t, "test-user-seed", natsSecrets.APIAuth.UserSeed)
	})

	t.Run("reads file just under 1MB limit", func(t *testing.T) {
		// Generate a token that makes the total JSON just under 1MB
		overhead := len(`{"kv":{"nats":{"api-auth":{"user":"u","user-seed":"s"}},"tokens":{"icms":""}}}`)
		tokenSize := (1024 * 1024) - overhead - 128 // leave room for whitespace/formatting
		largeToken := strings.Repeat("x", tokenSize)

		largeSecretsJSON := fmt.Sprintf(`{"kv":{"nats":{"api-auth":{"user":"u","user-seed":"s"}},"tokens":{"icms":"%s"}}}`, largeToken)
		assert.Less(t, len(largeSecretsJSON), 1024*1024, "test JSON must be under 1MB")

		tempFile := createTempSecretsFile(t, largeSecretsJSON)
		defer os.Remove(tempFile)

		fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
		require.NoError(t, err)

		token, err := fetcher.FetchToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, largeToken, token, "token must not be truncated at near-1MB size")
	})
}

func TestSelfManagedSecretsFetcher_ImplementsTokenFetcher(t *testing.T) {
	ctx := context.Background()
	tempFile := createTempSecretsFile(t, validSecretsJSON)
	defer os.Remove(tempFile)

	fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
	require.NoError(t, err)

	// Verify it implements TokenFetcher interface
	var _ TokenFetcher = fetcher

	// Test that it can be used as TokenFetcher
	token, err := fetcher.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-jwt-token", token)
}

func TestKVSecrets_StructureValidation(t *testing.T) {
	t.Run("all required fields are present", func(t *testing.T) {
		kvSecrets := KVSecrets{
			NATS: NATSSecrets{
				APIAuth: NATSAPIAuthSecrets{
					User:     "user-value",
					UserSeed: "seed-value",
				},
			},
			Tokens: TokensSecrets{
				ICMS:  "icms-token-value",
				ReVal: "reval-token-value",
			},
		}

		assert.Equal(t, "user-value", kvSecrets.NATS.APIAuth.User)
		assert.Equal(t, "seed-value", kvSecrets.NATS.APIAuth.UserSeed)
		assert.Equal(t, "icms-token-value", kvSecrets.Tokens.ICMS)
		assert.Equal(t, "reval-token-value", kvSecrets.Tokens.ReVal)
	})

	t.Run("empty struct has zero values", func(t *testing.T) {
		var kvSecrets KVSecrets

		assert.Empty(t, kvSecrets.NATS.APIAuth.User)
		assert.Empty(t, kvSecrets.NATS.APIAuth.UserSeed)
		assert.Empty(t, kvSecrets.Tokens.ICMS)
	})
}

func createTempSecretsFile(t *testing.T, content string) string {
	t.Helper()

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "secrets.json")

	err := os.WriteFile(tempFile, []byte(content), 0644)
	require.NoError(t, err, "Failed to create temp secrets file")

	return tempFile
}

func BenchmarkSelfManagedSecretsFetcher_FetchToken(b *testing.B) {
	ctx := context.Background()
	tempFile := createTempSecretsFileForBench(b, validSecretsJSON)
	defer os.Remove(tempFile)

	fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fetcher.FetchToken(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelfManagedSecretsFetcher_FetchNATSSecrets(b *testing.B) {
	ctx := context.Background()
	tempFile := createTempSecretsFileForBench(b, validSecretsJSON)
	defer os.Remove(tempFile)

	fetcher, _, err := NewSelfManagedSecretsFetcher(ctx, "mock", tempFile)
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fetcher.FetchNATSSecrets(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func createTempSecretsFileForBench(b *testing.B, content string) string {
	b.Helper()

	tempDir := b.TempDir()
	tempFile := filepath.Join(tempDir, "secrets.json")

	err := os.WriteFile(tempFile, []byte(content), 0644)
	require.NoError(b, err, "Failed to create temp secrets file")

	return tempFile
}
