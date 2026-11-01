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

package logs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestZapEndpointLogger_Success(t *testing.T) {
	zap.ReplaceGlobals(zap.NewNop())
	kv := &KV{}
	kv.With("request_id", "req-123").With("service", "test-service")
	middleware := ZapEndpointLogger(kv)
	nextCalled := false
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		nextCalled = true
		return "response", nil
	}
	endpoint := middleware(next)
	resp, err := endpoint(context.Background(), "request")
	assert.NoError(t, err)
	assert.Equal(t, "response", resp)
	assert.True(t, nextCalled)
}

func TestZapEndpointLogger_WithError(t *testing.T) {
	zap.ReplaceGlobals(zap.NewNop())
	kv := &KV{}
	middleware := ZapEndpointLogger(kv)
	expectedErr := errors.New("test error")
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, expectedErr
	}
	endpoint := middleware(next)
	_, err := endpoint(context.Background(), "request")
	assert.Equal(t, expectedErr, err)
}

func TestKV_With(t *testing.T) {
	kv := &KV{}
	kv2 := kv.With("key1", "val1")
	assert.NotNil(t, kv2)
	assert.Same(t, kv, kv2)
	kv3 := kv2.With("key2", 42)
	assert.NotNil(t, kv3)
	assert.Equal(t, "val1", kv.kv["key1"])
	assert.Equal(t, 42, kv.kv["key2"])
}

func TestKV_With_InitializesMap(t *testing.T) {
	kv := &KV{}
	assert.Nil(t, kv.kv)
	kv.With("k", "v")
	assert.NotNil(t, kv.kv)
}
