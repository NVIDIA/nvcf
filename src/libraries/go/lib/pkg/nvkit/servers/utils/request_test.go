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

package utils

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddRequestInfoToContext(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/test", nil)
	require.NoError(t, err)
	req.Header.Set(HeaderKeyRequestID, "req-123")
	req.Header.Set(HeaderKeyAuditID, "audit-456")
	req.Header.Set(HeaderETag, "etag-789")
	updatedReq := AddRequestInfoToContext(req)
	ctx := updatedReq.Context()
	assert.Equal(t, HandlerTypeCustom, ctx.Value(keyHandlerTypeCustom))
	assert.Equal(t, "req-123", ctx.Value(keyRequestID))
	assert.Equal(t, "audit-456", ctx.Value(keyAuditID))
}

func TestAddRequestInfoToContext_EmptyHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/test", nil)
	require.NoError(t, err)
	updatedReq := AddRequestInfoToContext(req)
	ctx := updatedReq.Context()
	assert.Equal(t, HandlerTypeCustom, ctx.Value(keyHandlerTypeCustom))
	assert.Equal(t, "", ctx.Value(keyRequestID))
}
