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

package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/plugins/types"
)

func Test_decodeB64Token(t *testing.T) {
	authReq := &types.Request{
		Account:    "test-account",
		PluginName: "webhook",
		Payload:    "valid-token",
	}
	authReqBytes, err := json.Marshal(authReq)
	if err != nil {
		t.Fatalf("Failed to marshal auth request: %v", err)
	}
	t.Run("RawURLEncoding", func(t *testing.T) {
		token := base64.RawURLEncoding.EncodeToString(authReqBytes)
		decodedAuthReq, err := decodeB64Token(token)
		if err != nil {
			t.Fatalf("Failed to decode token: %v", err)
		}
		if *decodedAuthReq != *authReq {
			t.Fatalf("Auth request does not match")
		}
	})

	t.Run("URLEncoding", func(t *testing.T) {
		token := base64.URLEncoding.EncodeToString(authReqBytes)
		decodedAuthReq, err := decodeB64Token(token)
		if err != nil {
			t.Fatalf("Failed to decode token: %v", err)
		}
		if *decodedAuthReq != *authReq {
			t.Fatalf("Auth request does not match")
		}
	})
}
