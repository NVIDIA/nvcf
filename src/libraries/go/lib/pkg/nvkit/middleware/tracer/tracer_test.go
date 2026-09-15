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

package tracer

import (
	"context"
	"errors"
	"testing"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/api/errors/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrorTracer_NoError(t *testing.T) {
	middleware := ErrorTracer()
	nextCalled := false
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		nextCalled = true
		return "response", nil
	}
	endpoint := middleware(next)
	resp, err := endpoint(context.Background(), "request")
	require.NoError(t, err)
	assert.True(t, nextCalled)
	assert.Equal(t, "response", resp)
}

func TestErrorTracer_WithError(t *testing.T) {
	middleware := ErrorTracer()
	sentinelErr := errors.New("something failed")
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, sentinelErr
	}
	endpoint := middleware(next)
	_, err := endpoint(context.Background(), "request")
	assert.Error(t, err)
	assert.Equal(t, sentinelErr, err)
}

func TestErrorTracer_WithGRPCError(t *testing.T) {
	middleware := ErrorTracer()
	grpcErr := status.Error(codes.NotFound, "not found")
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, grpcErr
	}
	endpoint := middleware(next)
	_, err := endpoint(context.Background(), "request")
	assert.Error(t, err)
	assert.Equal(t, grpcErr, err)
}

func TestErrorTracer_WithNVErrorDetailsAndSpan(t *testing.T) {
	// Build a gRPC status error that carries an NVError detail.
	st := status.New(codes.Internal, "internal error")
	st, err := st.WithDetails(&nverrors.NVError{ErrorId: "err-123", Reason: "something went wrong"})
	require.NoError(t, err)
	grpcErr := st.Err()

	// Create an active span in the context so the span branch is exercised.
	noopTP := noop.NewTracerProvider()
	ctx, span := noopTP.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	middleware := ErrorTracer()
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, grpcErr
	}
	ep := middleware(next)
	_, callErr := ep(ctx, "request")
	assert.Error(t, callErr)
	assert.Equal(t, grpcErr, callErr)
}
