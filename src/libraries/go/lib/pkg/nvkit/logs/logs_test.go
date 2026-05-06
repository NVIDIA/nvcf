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
	"testing"
	"time"

	"github.com/go-kit/kit/log" //nolint:staticcheck
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc/metadata"

	serverutils "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/servers/utils"
)

func TestStandardLogger(t *testing.T) {
	logger := StandardLogger()
	assert.NotNil(t, logger)
	err := logger.Log("key", "value")
	assert.NoError(t, err)
}

func TestNewZapLogger(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := NewZapLogger(atom)
	require.NotNil(t, logger)
	assert.NotNil(t, logger.GetKitLogger())
	assert.NotNil(t, logger.GetZapLogger())
	assert.NotNil(t, logger.L())
	assert.NotNil(t, logger.S())
	err := logger.Log("key", "value")
	assert.NoError(t, err)
	err = logger.Close()
	assert.NoError(t, err)
}

func TestNewZapLogger_Debug(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.DebugLevel)
	logger := NewZapLogger(atom)
	require.NotNil(t, logger)
	assert.NotNil(t, logger.L())
}

func TestNewZapLogger_WithCustomEncoder(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := NewZapLogger(atom, WithEncoderFunc(zapcore.NewJSONEncoder))
	require.NotNil(t, logger)
	assert.NotNil(t, logger.GetZapLogger())
}

func TestNewZapLogger_WithAsyncFlush(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := NewZapLogger(atom, WithAsyncFlush(100*time.Millisecond))
	require.NotNil(t, logger)
	_ = logger.Close()
}

func TestNewZapLogger_WithZapLogger(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	customZap, _ := zap.NewDevelopment()
	logger := NewZapLogger(atom, WithZapLogger(customZap))
	require.NotNil(t, logger)
	assert.Equal(t, customZap, logger.GetZapLogger())
}

func TestNewAsyncZapLogger(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := NewAsyncZapLogger(atom, 100)
	require.NotNil(t, logger)
	_ = logger.Close()
}

func TestGetZapFieldsFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	fields := GetZapFieldsFromContext(ctx)
	assert.NotNil(t, fields)
}

func TestGetZapFieldsFromContext_CustomHandler(t *testing.T) {
	ctx := context.Background()
	ctx = serverutils.ContextWithCustomHandlerForTest(ctx, "req-abc-123")
	fields := GetZapFieldsFromContext(ctx)
	assert.NotEmpty(t, fields)
}

func TestGetZapFieldsFromContext_WithGRPCMetadata(t *testing.T) {
	md := metadata.Pairs(serverutils.HeaderKeyRequestID, "req-grpc-123")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	fields := GetZapFieldsFromContext(ctx)
	assert.NotEmpty(t, fields)
}

func TestGetZapFieldsFromContext_CustomHandlerNoRequestID(t *testing.T) {
	ctx := context.Background()
	ctx = serverutils.ContextWithCustomHandlerMarkerForTest(ctx)
	fields := GetZapFieldsFromContext(ctx)
	assert.NotNil(t, fields)
}

func TestGetZapFieldsForError_Nil(t *testing.T) {
	fields := GetZapFieldsForError(nil)
	assert.Empty(t, fields)
}

func TestGetZapFieldsForError_NoNVError(t *testing.T) {
	fields := GetZapFieldsForError(assert.AnError)
	assert.Empty(t, fields)
}

func TestWithKitLogger(t *testing.T) {
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	zl := NewZapLogger(atom)
	kitLogger := log.NewNopLogger()
	opt := WithKitLogger(kitLogger)
	opt(zl)
	assert.Equal(t, kitLogger, zl.kitLogger)
}
