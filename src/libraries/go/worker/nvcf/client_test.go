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

package nvcf

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
)

func TestNatsConnectionLogging(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	t.Cleanup(zap.ReplaceGlobals(zap.New(core)))
	tp := auth.NewSettableTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	conn, err := newNatsConnection(startEmbeddedNats(t), nil, "", tp, "inst-1", "fnv-1")
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	opts := conn.Opts

	t.Run("clean shutdown", func(t *testing.T) {
		conn.Close()
		require.Eventually(t, func() bool {
			return logs.FilterMessage("nats connection closed.").Len() == 1
		}, 5*time.Second, time.Millisecond)
		entry := logs.FilterMessage("nats connection closed.").All()[0]
		assert.Equal(t, zap.InfoLevel, entry.Level)
		assert.Equal(t, "fnv-1", entry.ContextMap()["function version id"])
		assert.Equal(t, "inst-1", entry.ContextMap()["instance id"])
		assert.NotContains(t, entry.ContextMap(), "error")
		assert.Zero(t, logs.FilterLevelExact(zap.ErrorLevel).Len())
	})

	t.Run("reconnect delay", func(t *testing.T) {
		delay := opts.CustomReconnectDelayCB(2)
		require.Positive(t, delay)
		entries := logs.FilterMessage("waiting to reconnect to nats").All()
		require.Len(t, entries, 1)
		assert.Equal(t, zap.WarnLevel, entries[0].Level)
		assert.Equal(t, int64(2), entries[0].ContextMap()["reconnect_round"])
		assert.Equal(t, delay, entries[0].ContextMap()["delay"])
	})

	t.Run("reconnect failure and terminal closure", func(t *testing.T) {
		closed := make(chan struct{})
		closedCB := opts.ClosedCB
		opts.ClosedCB = func(conn *nats.Conn) {
			closedCB(conn)
			close(closed)
		}
		opts.Url = "nats://127.0.0.1:0"
		opts.Servers = nil
		opts.MaxReconnect = 1
		opts.CustomReconnectDelayCB = func(int) time.Duration { return 0 }
		failedConn, err := opts.Connect()
		require.NoError(t, err)
		t.Cleanup(failedConn.Close)
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("NATS connection did not close after exhausting reconnects")
		}
		failures := logs.FilterMessage("nats reconnect attempt failed").All()
		require.Len(t, failures, 1)
		assert.Equal(t, zap.WarnLevel, failures[0].Level)
		assert.NotEmpty(t, failures[0].ContextMap()["error"])
		assert.Equal(t, "fnv-1", failures[0].ContextMap()["function version id"])
		assert.Equal(t, "inst-1", failures[0].ContextMap()["instance id"])
		entries := logs.FilterMessage("nats connection permanently closed").All()
		require.Len(t, entries, 1)
		assert.Equal(t, zap.ErrorLevel, entries[0].Level)
		require.Error(t, failedConn.LastError())
		assert.Equal(t, failedConn.LastError().Error(), entries[0].ContextMap()["error"])
	})
}

// mockTokenSource is a mock implementation of oauth2.TokenSource
type mockTokenSource struct {
	token *oauth2.Token
	err   error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	return m.token, m.err
}

func TestNatsAuthOption_WithTokenHandler(t *testing.T) {
	tests := []struct {
		name        string
		token       *oauth2.Token
		tokenErr    error
		expectToken string
		expectError bool
	}{
		{
			name: "successful token encoding",
			token: &oauth2.Token{
				AccessToken: "test-access-token",
			},
			tokenErr:    nil,
			expectError: false,
		},
		{
			name:        "token provider error returns empty token",
			token:       nil,
			tokenErr:    errors.New("token provider error"),
			expectToken: "",
			expectError: false, // The function doesn't return error, just empty token
		},
		{
			name: "empty access token",
			token: &oauth2.Token{
				AccessToken: "",
			},
			tokenErr:    nil,
			expectError: false,
		},
		{
			name: "special characters in token",
			token: &oauth2.Token{
				AccessToken: "test-token-with-special-chars!@#$%^&*()",
			},
			tokenErr:    nil,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock token source
			mockSource := &mockTokenSource{
				token: tt.token,
				err:   tt.tokenErr,
			}

			// Create SettableTokenSource with mock
			tokenProvider := auth.NewSettableTokenSource(mockSource)

			// Call function with nil nkeySeed to trigger token handler path
			option, err := natsAuthOption(nil, "", tokenProvider)

			// Should not return error for token handler path
			assert.NoError(t, err)
			assert.NotNil(t, option)

			// Apply the option to a nats.Options struct
			opts := &nats.Options{}
			err = option(opts)
			assert.NoError(t, err)

			// Verify that TokenHandler is set
			assert.NotNil(t, opts.TokenHandler)

			// Call the token handler and verify the result
			actualToken := opts.TokenHandler()

			if tt.tokenErr != nil {
				// When token provider has error, should return empty string
				assert.Equal(t, "", actualToken)
			} else if tt.token != nil {
				// Verify the token is properly encoded
				if tt.token.AccessToken != "" {
					assert.NotEmpty(t, actualToken)

					// Decode and verify the token structure
					decodedJSON, err := base64.RawURLEncoding.DecodeString(actualToken)
					require.NoError(t, err)

					var tokenData struct {
						Account    string `json:"account"`
						PluginName string `json:"pluginName"`
						Payload    string `json:"payload"`
					}

					err = json.Unmarshal(decodedJSON, &tokenData)
					require.NoError(t, err)

					assert.Equal(t, "Worker", tokenData.Account)
					assert.Equal(t, "webhook", tokenData.PluginName)
					assert.Equal(t, tt.token.AccessToken, tokenData.Payload)
				} else {
					// Empty access token should still produce a valid token structure
					assert.NotEmpty(t, actualToken)
				}
			}
		})
	}
}

func TestNatsAuthOption_WithNKey(t *testing.T) {
	tests := []struct {
		name        string
		nkeySeed    string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid user nkey seed",
			nkeySeed:    generateValidUserNKeySeed(t),
			expectError: false,
		},
		{
			name:        "invalid nkey seed format",
			nkeySeed:    "invalid-seed",
			expectError: true,
			errorMsg:    "illegal base32 data",
		},
		{
			name:        "empty nkey seed",
			nkeySeed:    "",
			expectError: true,
			errorMsg:    "invalid encoded key",
		},
		{
			name:        "malformed nkey seed",
			nkeySeed:    "SU" + strings.Repeat("A", 60), // Invalid checksum
			expectError: true,
			errorMsg:    "invalid checksum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a dummy token provider (won't be used in nkey path)
			tokenProvider := auth.NewSettableTokenSource(&mockTokenSource{
				token: &oauth2.Token{AccessToken: "dummy"},
			})

			option, err := natsAuthOption(&tt.nkeySeed, "", tokenProvider)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, option)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, option)

				// Apply the option to a nats.Options struct
				opts := &nats.Options{}
				err = option(opts)
				assert.NoError(t, err)

				// Verify that Nkey and SignatureCB are set
				assert.NotEmpty(t, opts.Nkey)
				assert.NotNil(t, opts.SignatureCB)

				// Verify the public key matches what we expect
				kp, err := nkeys.FromSeed([]byte(tt.nkeySeed))
				require.NoError(t, err)
				expectedPubKey, err := kp.PublicKey()
				require.NoError(t, err)
				assert.Equal(t, expectedPubKey, opts.Nkey)

				// Test the signature callback
				testNonce := []byte("test-nonce-data")
				signature, err := opts.SignatureCB(testNonce)
				assert.NoError(t, err)
				assert.NotEmpty(t, signature)

				// Verify the signature is valid
				err = kp.Verify(testNonce, signature)
				assert.NoError(t, err)
			}
		})
	}
}

func TestNatsAuthOption_TokenHandlerWithSpecialCases(t *testing.T) {
	t.Run("token with special JSON characters", func(t *testing.T) {
		specialToken := &oauth2.Token{
			AccessToken: `{"test": "value with quotes and \"escapes\""}`,
		}

		mockSource := &mockTokenSource{
			token: specialToken,
			err:   nil,
		}

		tokenProvider := auth.NewSettableTokenSource(mockSource)
		option, err := natsAuthOption(nil, "", tokenProvider)

		assert.NoError(t, err)
		assert.NotNil(t, option)

		// Apply the option and test the token handler
		opts := &nats.Options{}
		err = option(opts)
		assert.NoError(t, err)

		actualToken := opts.TokenHandler()
		assert.NotEmpty(t, actualToken)

		// Verify it can be decoded properly
		decodedJSON, err := base64.RawURLEncoding.DecodeString(actualToken)
		require.NoError(t, err)

		var tokenData struct {
			Account    string `json:"account"`
			PluginName string `json:"pluginName"`
			Payload    string `json:"payload"`
		}

		err = json.Unmarshal(decodedJSON, &tokenData)
		require.NoError(t, err)

		assert.Equal(t, "Worker", tokenData.Account)
		assert.Equal(t, "webhook", tokenData.PluginName)
		assert.Equal(t, specialToken.AccessToken, tokenData.Payload)
	})

	t.Run("multiple calls to token handler", func(t *testing.T) {
		token := &oauth2.Token{
			AccessToken: "consistent-token",
		}

		mockSource := &mockTokenSource{
			token: token,
			err:   nil,
		}

		tokenProvider := auth.NewSettableTokenSource(mockSource)
		option, err := natsAuthOption(nil, "", tokenProvider)

		assert.NoError(t, err)
		assert.NotNil(t, option)

		opts := &nats.Options{}
		err = option(opts)
		assert.NoError(t, err)

		// Call token handler multiple times
		token1 := opts.TokenHandler()
		token2 := opts.TokenHandler()
		token3 := opts.TokenHandler()

		// Should return the same token each time
		assert.Equal(t, token1, token2)
		assert.Equal(t, token2, token3)
		assert.NotEmpty(t, token1)
	})
}

func TestNatsAuthOption_TokenNamesTheAccount(t *testing.T) {
	tests := []struct {
		name    string
		account string
		want    string
	}{
		{name: "named account", account: "APP", want: "APP"},
		{name: "no account named", account: "", want: DefaultNatsAccount},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenProvider := auth.NewSettableTokenSource(&mockTokenSource{token: &oauth2.Token{AccessToken: "worker-token"}})
			option, err := natsAuthOption(nil, tt.account, tokenProvider)
			require.NoError(t, err)

			opts := &nats.Options{}
			require.NoError(t, option(opts))

			decodedJSON, err := base64.RawURLEncoding.DecodeString(opts.TokenHandler())
			require.NoError(t, err)
			var tokenData struct {
				Account    string `json:"account"`
				PluginName string `json:"pluginName"`
				Payload    string `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(decodedJSON, &tokenData))
			assert.Equal(t, tt.want, tokenData.Account)
			assert.Equal(t, "webhook", tokenData.PluginName)
			assert.Equal(t, "worker-token", tokenData.Payload)
		})
	}
}

func TestNatsAuthOption_NkeyIgnoresTheAccount(t *testing.T) {
	tokenProvider := auth.NewSettableTokenSource(&mockTokenSource{token: &oauth2.Token{AccessToken: "worker-token"}})
	nkeySeed := generateValidUserNKeySeed(t)

	option, err := natsAuthOption(&nkeySeed, "APP", tokenProvider)
	require.NoError(t, err)

	opts := &nats.Options{}
	require.NoError(t, option(opts))
	assert.NotEmpty(t, opts.Nkey)
	assert.Nil(t, opts.TokenHandler)
}

func TestNatsAuthOption_BothPathsComparison(t *testing.T) {
	// Test that both paths produce different but valid options
	token := &oauth2.Token{AccessToken: "test-token"}
	mockSource := &mockTokenSource{token: token, err: nil}
	tokenProvider := auth.NewSettableTokenSource(mockSource)

	// Test token handler path
	tokenOption, err := natsAuthOption(nil, "", tokenProvider)
	assert.NoError(t, err)
	assert.NotNil(t, tokenOption)

	tokenOpts := &nats.Options{}
	err = tokenOption(tokenOpts)
	assert.NoError(t, err)
	assert.NotNil(t, tokenOpts.TokenHandler)
	assert.Empty(t, tokenOpts.Nkey) // Should not have Nkey set

	// Test nkey path
	nkeySeed := generateValidUserNKeySeed(t)
	nkeyOption, err := natsAuthOption(&nkeySeed, "", tokenProvider)
	assert.NoError(t, err)
	assert.NotNil(t, nkeyOption)

	nkeyOpts := &nats.Options{}
	err = nkeyOption(nkeyOpts)
	assert.NoError(t, err)
	assert.NotNil(t, nkeyOpts.SignatureCB)
	assert.NotEmpty(t, nkeyOpts.Nkey)
	assert.Nil(t, nkeyOpts.TokenHandler) // Should not have TokenHandler set

	// Verify they set different authentication methods
	assert.NotEqual(t, tokenOpts.TokenHandler != nil, nkeyOpts.TokenHandler != nil)
	assert.NotEqual(t, tokenOpts.Nkey != "", nkeyOpts.Nkey != "")
}

// Helper function to generate a valid user NKey seed for testing
func generateValidUserNKeySeed(t *testing.T) string {
	userKP, err := nkeys.CreateUser()
	require.NoError(t, err)

	seed, err := userKP.Seed()
	require.NoError(t, err)

	return string(seed)
}
