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

package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
)

func TestVaultClientHelpers(t *testing.T) {
	t.Run("DecodeJWTClaims success", func(t *testing.T) {
		claims := models.JWTClaims{
			IAT:    1234567890,
			EXP:    1234567890 + 3600,
			Scopes: []string{"admin", "read"},
		}

		claimsJSON, _ := json.Marshal(claims)
		payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
		token := "header." + payload + ".signature"

		decoded, err := DecodeJWTClaims(token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if decoded.IAT != claims.IAT {
			t.Errorf("expected IAT %d, got %d", claims.IAT, decoded.IAT)
		}
	})

	t.Run("DecodeJWTClaims invalid format", func(t *testing.T) {
		_, err := DecodeJWTClaims("invalid")
		if err == nil {
			t.Error("expected error for invalid JWT format")
		}
	})

	t.Run("ReadTokenFile success", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-token-*")
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		defer func() { _ = os.Remove(tmpFile.Name()) }()

		token := "test-vault-token-12345"
		if _, err := tmpFile.WriteString(token); err != nil {
			t.Fatalf("failed to write token: %v", err)
		}
		_ = tmpFile.Close()

		readToken, err := ReadTokenFile(tmpFile.Name())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if readToken != token {
			t.Errorf("expected token '%s', got '%s'", token, readToken)
		}
	})

	t.Run("ReadTokenFile not found", func(t *testing.T) {
		_, err := ReadTokenFile("/nonexistent/file")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("NewVaultClient invalid address", func(t *testing.T) {
		_, err := NewVaultClient(":")
		if err == nil {
			t.Error("expected error for invalid vault address")
		}
	})

	t.Run("SignToken sends only the latest token in the standard Vault header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got, want := r.Header.Values("X-Vault-Token"), []string{"latest-token"}; !equalStrings(got, want) {
				t.Errorf("X-Vault-Token headers = %q, want %q", got, want)
			}
			if got := r.Header.Values("X-ESS-Token"); len(got) != 0 {
				t.Errorf("X-ESS-Token headers = %q, want none", got)
			}
			http.Error(w, "signing failed", http.StatusInternalServerError)
		}))
		defer server.Close()

		client, err := NewVaultClient(server.URL)
		if err != nil {
			t.Fatalf("NewVaultClient() returned an unexpected error: %v", err)
		}
		client.SetToken("old-token")
		client.SetToken("latest-token")

		_, err = client.SignToken(t.Context(), "services/example/jwt/sign", "admin-issuer-proxy")
		if err == nil {
			t.Fatal("SignToken() returned nil error")
		}
		if !strings.Contains(err.Error(), `write Vault signing path "services/example/jwt/sign/admin-issuer-proxy"`) {
			t.Fatalf("SignToken() error = %q, want resolved signing path context", err)
		}
	})

	t.Run("SignToken cancels an in-flight Vault write", func(t *testing.T) {
		requestStarted := make(chan struct{})
		releaseRequest := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			select {
			case <-r.Context().Done():
			case <-releaseRequest:
			}
		}))
		t.Cleanup(func() {
			close(releaseRequest)
			server.Close()
		})

		client, err := NewVaultClient(server.URL)
		if err != nil {
			t.Fatalf("NewVaultClient() returned an unexpected error: %v", err)
		}
		client.SetToken("test-token")

		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := client.SignToken(ctx, "services/example/jwt/sign", "admin-issuer-proxy")
			result <- err
		}()

		select {
		case <-requestStarted:
		case <-time.After(time.Second):
			t.Fatal("Vault write did not start")
		}
		cancel()

		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("SignToken() error = %v, want context cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Vault write did not stop after context cancellation")
		}
	})
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
