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

package cassandra

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

func TestValidateCassandraTLSConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  config.CassandraConfig
		wantErr string
	}{
		{
			name: "no TLS configuration",
		},
		{
			name: "file key pair uses system roots",
			config: config.CassandraConfig{
				PubKeyPath:  "client.crt",
				PrivKeyPath: "client.key",
			},
		},
		{
			name: "file key pair uses custom CA",
			config: config.CassandraConfig{
				PubKeyPath:  "client.crt",
				PrivKeyPath: "client.key",
				CACertPath:  "ca.crt",
			},
		},
		{
			name: "base64 key pair uses system roots",
			config: config.CassandraConfig{
				PubKeyB64:  "client-cert",
				PrivKeyB64: "client-key",
			},
		},
		{
			name: "base64 key pair uses custom CA",
			config: config.CassandraConfig{
				PubKeyB64:  "client-cert",
				PrivKeyB64: "client-key",
				CACertB64:  "ca-cert",
			},
		},
		{
			name: "CA path without client key pair",
			config: config.CassandraConfig{
				CACertPath: "ca.crt",
			},
			wantErr: "requires both pub-key-path and priv-key-path",
		},
		{
			name: "base64 CA without client key pair",
			config: config.CassandraConfig{
				CACertB64: "ca-cert",
			},
			wantErr: "requires both pub-key-b64 and priv-key-b64",
		},
		{
			name: "partial file key pair",
			config: config.CassandraConfig{
				PubKeyPath: "client.crt",
			},
			wantErr: "requires both pub-key-path and priv-key-path",
		},
		{
			name: "partial base64 key pair",
			config: config.CassandraConfig{
				PrivKeyB64: "client-key",
			},
			wantErr: "requires both pub-key-b64 and priv-key-b64",
		},
		{
			name: "mixed file and base64 configuration",
			config: config.CassandraConfig{
				PubKeyPath:  "client.crt",
				PrivKeyPath: "client.key",
				CACertB64:   "ca-cert",
			},
			wantErr: "cannot mix file paths and base64 values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCassandraTLSConfig(tt.config)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
