// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

func TestDefaultAuthorizerFactory_AuthDisabled_ReturnsEmptyChain(t *testing.T) {
	cfg := &config.RevalConfig{
		Auth: config.AuthnConfig{JWT: config.JWTAuthConfig{Enabled: false}},
	}
	steps, err := defaultAuthorizerFactory(context.Background(), nil, cfg, nil)
	require.NoError(t, err)
	assert.Empty(t, steps)
}

func TestDefaultAuthorizerFactory_AuthEnabled_ReturnsLocalAuthorizer(t *testing.T) {
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer jwksSrv.Close()

	cfg := &config.RevalConfig{
		Auth: config.AuthnConfig{JWT: config.JWTAuthConfig{Enabled: true, JWKSetURL: jwksSrv.URL}},
	}
	auths, err := defaultAuthorizerFactory(context.Background(), nil, cfg, nil)
	require.NoError(t, err)
	require.Len(t, auths, 1)
	_, ok := auths[0].(authorizers.Local)
	assert.True(t, ok)
}
