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

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/auth/statickeys"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

func TestNewAuthClient(t *testing.T) {
	t.Parallel()

	secretsPath := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(secretsPath, []byte(`{"nvcfApiToken":"gateway-token"}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}

	tests := []struct {
		name           string
		grpcAddr       string
		apiKeysPath    string
		allowAnonymous bool
		wantMode       config.AuthMode
		wantErr        error
		wantClient     bool
		wantWarning    bool
	}{
		{
			name:    "fails closed without an authenticator",
			wantErr: config.ErrNoAuthConfigured,
		},
		{
			name:           "anonymous only when allowed, with a warning",
			allowAnonymous: true,
			wantMode:       config.AuthModeAnonymous,
			wantWarning:    true,
		},
		{
			name:        "static keys",
			apiKeysPath: "/etc/llm-api-gateway/api-keys.json",
			wantMode:    config.AuthModeStaticKeys,
			wantClient:  true,
		},
		{
			name:       "nvcf grpc",
			grpcAddr:   "127.0.0.1:1",
			wantMode:   config.AuthModeNVCF,
			wantClient: true,
		},
		{
			name:        "nvcf and static keys together",
			grpcAddr:    "127.0.0.1:1",
			apiKeysPath: "/etc/llm-api-gateway/api-keys.json",
			wantErr:     errors.New("mutually exclusive"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.NVCF.GRPCAddr = tc.grpcAddr
			cfg.NVCF.SecretsPath = secretsPath
			cfg.NVCF.GRPCInsecure = true
			cfg.Auth.APIKeysPath = tc.apiKeysPath
			cfg.Auth.AllowAnonymous = tc.allowAnonymous

			var logs bytes.Buffer
			client, mode, err := newAuthClient(cfg, zerolog.New(&logs))
			if client != nil {
				t.Cleanup(func() { _ = client.Close() })
			}

			if tc.wantErr != nil {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr.Error()) {
					t.Fatalf("newAuthClient() error = %v, want %v", err, tc.wantErr)
				}
				if client != nil {
					t.Fatal("client returned alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("newAuthClient() error = %v", err)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tc.wantMode)
			}
			if (client != nil) != tc.wantClient {
				t.Fatalf("client = %T, want client present = %v", client, tc.wantClient)
			}
			if tc.wantMode == config.AuthModeStaticKeys {
				if _, ok := client.(*statickeys.Authorizer); !ok {
					t.Fatalf("client = %T, want *statickeys.Authorizer", client)
				}
			}
			gotWarning := strings.Contains(logs.String(), `"level":"warn"`) &&
				strings.Contains(logs.String(), "unauthenticated requests")
			if gotWarning != tc.wantWarning {
				t.Fatalf("warning logged = %v, want %v; logs:\n%s", gotWarning, tc.wantWarning, logs.String())
			}
		})
	}
}

func TestNewAuthClientErrorNamesEveryOption(t *testing.T) {
	t.Parallel()

	_, _, err := newAuthClient(config.Default(), zerolog.Nop())
	if !errors.Is(err, config.ErrNoAuthConfigured) {
		t.Fatalf("newAuthClient() error = %v, want ErrNoAuthConfigured", err)
	}
	for _, option := range []string{"NVCF_GRPC_ADDR", "API_KEYS_PATH", "ALLOW_ANONYMOUS"} {
		if !strings.Contains(err.Error(), option) {
			t.Fatalf("error %q does not name %s", err.Error(), option)
		}
	}
}

type fakeStarter struct {
	startAddr     string
	tlsAddr       string
	tlsCertFile   string
	tlsKeyFile    string
	tlsReload     time.Duration
	startCalls    int
	startTLSCalls int
}

func (f *fakeStarter) Start(address string) error {
	f.startCalls++
	f.startAddr = address
	return nil
}

func (f *fakeStarter) StartTLS(address, certFile, keyFile string, reloadInterval time.Duration) error {
	f.startTLSCalls++
	f.tlsAddr = address
	f.tlsCertFile = certFile
	f.tlsKeyFile = keyFile
	f.tlsReload = reloadInterval
	return nil
}

func TestGatewayStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		certFile string
		keyFile  string
		wantTLS  bool
	}{
		{name: "plaintext without tls files"},
		{name: "tls with both files", certFile: "/tls/tls.crt", keyFile: "/tls/tls.key", wantTLS: true},
		{name: "half pair still selects tls", certFile: "/tls/tls.crt", wantTLS: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			starter := &fakeStarter{}
			start := gatewayStart(starter, config.ServerConfig{
				TLSCertFile:       tc.certFile,
				TLSKeyFile:        tc.keyFile,
				TLSReloadInterval: 7 * time.Second,
			})
			if err := start(":8443"); err != nil {
				t.Fatalf("start() error = %v", err)
			}

			if !tc.wantTLS {
				if starter.startCalls != 1 || starter.startTLSCalls != 0 || starter.startAddr != ":8443" {
					t.Fatalf("starter = %+v, want one plaintext Start on :8443", starter)
				}
				return
			}
			if starter.startTLSCalls != 1 || starter.startCalls != 0 || starter.tlsAddr != ":8443" {
				t.Fatalf("starter = %+v, want one StartTLS on :8443", starter)
			}
			if starter.tlsCertFile != tc.certFile || starter.tlsKeyFile != tc.keyFile {
				t.Fatalf("tls files = %v, %v, want %q, %q", starter.tlsCertFile, starter.tlsKeyFile, tc.certFile, tc.keyFile)
			}
			if starter.tlsReload != 7*time.Second {
				t.Fatalf("tls reload interval = %s, want 7s", starter.tlsReload)
			}
		})
	}
}
