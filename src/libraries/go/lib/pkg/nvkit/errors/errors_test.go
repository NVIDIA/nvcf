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

package errors

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/api/errors/v1"
)

func TestConfigError_WithFieldName(t *testing.T) {
	e := &ConfigError{FieldName: "myField", Message: "bad"}
	assert.Equal(t, "field: myField message: bad", e.Error())
}

func TestConfigError_WithoutFieldName(t *testing.T) {
	e := &ConfigError{Message: "bad"}
	assert.Equal(t, "message: bad", e.Error())
}

func TestNewNVError_PlainError(t *testing.T) {
	err := errors.New("plain error")
	nvErr := NewNVError(err)
	assert.NotNil(t, nvErr)
	assert.Contains(t, nvErr.Error(), "plain error")
	assert.NotEmpty(t, nvErr.ID())
}

func TestNewNVError_WrapsExisting(t *testing.T) {
	original := NewNVError(errors.New("original"))
	wrapped := NewNVError(original.(error))
	assert.Equal(t, original.ID(), wrapped.ID())
}

func TestNVError_WithCode(t *testing.T) {
	base := NewNVError(errors.New("base error"))
	result := base.WithCode(codes.NotFound)
	assert.Equal(t, codes.NotFound, result.GRPCStatus().Code())
}

func TestNVError_WithReason(t *testing.T) {
	base := NewNVError(errors.New("base"))
	result := base.WithReason("custom reason")
	assert.Equal(t, "custom reason", result.Reason())
}

func TestNVError_WithOrigin(t *testing.T) {
	base := NewNVError(errors.New("base"))
	result := base.WithOrigin("test-service")
	assert.Equal(t, "test-service", result.Origin())
}

func TestNVError_WithMetadata(t *testing.T) {
	base := NewNVError(errors.New("base"))
	result := base.WithMetadata(map[string]string{"key1": "val1"})
	assert.Equal(t, "val1", result.GetMetadata("key1"))
	md := result.Metadata()
	assert.Equal(t, "val1", md["key1"])
}

func TestNVError_WithMetadata_Merges(t *testing.T) {
	base := NewNVError(errors.New("base"))
	base.WithMetadata(map[string]string{"a": "1"})
	base.WithMetadata(map[string]string{"b": "2"})
	assert.Equal(t, "1", base.GetMetadata("a"))
}

func TestNVError_ErrorString(t *testing.T) {
	base := NewNVError(errors.New("test-err"))
	assert.NotEmpty(t, base.Error())
}

func TestNVError_GRPCStatus(t *testing.T) {
	nvErr := &NVError{
		NVError: nverrors.NVError{ErrorId: "test-id", Reason: "test reason"},
		code:    codes.Internal,
	}
	st := nvErr.GRPCStatus()
	assert.Equal(t, codes.Internal, st.Code())
}

func TestNVErrorMarshaler_MarshalFallback(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	data, err := marshaler.Marshal(map[string]string{"key": "value"})
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestNVErrorMarshaler_MarshalStatus(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	st := status.New(codes.InvalidArgument, "bad request")
	data, err := marshaler.Marshal(st.Proto())
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestNVErrorMarshaler_MarshalStatusWithNVError(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	nvErrDetail := &nverrors.NVError{ErrorId: "abc-123", Reason: "reason"}
	st := status.New(codes.Internal, "internal error")
	stWithDetails, err := st.WithDetails(nvErrDetail)
	require.NoError(t, err)
	data, err2 := marshaler.Marshal(stWithDetails.Proto())
	require.NoError(t, err2)
	assert.NotEmpty(t, data)
}

func TestNVErrorMarshaler_MarshalSPBStatus(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	proto := &spb.Status{Code: int32(codes.OK), Message: "ok"}
	data, err := marshaler.Marshal(proto)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestNVErrorMarshaler_Unmarshal(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	err := marshaler.Unmarshal([]byte("{}"), &map[string]interface{}{})
	assert.NoError(t, err)
}

func TestNVErrorMarshaler_NewDecoder(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	dec := marshaler.NewDecoder(bytes.NewReader([]byte("{}")))
	assert.NotNil(t, dec)
}

func TestNVErrorMarshaler_NewEncoder(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	enc := marshaler.NewEncoder(&bytes.Buffer{})
	assert.NotNil(t, enc)
}

func TestNVErrorMarshaler_ContentType(t *testing.T) {
	marshaler := &NVErrorMarshaler{FallbackMarshaler: &runtime.JSONPb{}}
	ct := marshaler.ContentType(nil)
	assert.NotEmpty(t, ct)
}

func TestHttpStatusCodeToGrpcCode(t *testing.T) {
	tests := []struct {
		httpCode int
		grpcCode codes.Code
	}{
		{http.StatusOK, codes.OK},
		{http.StatusRequestTimeout, codes.Internal},
		{http.StatusBadRequest, codes.InvalidArgument},
		{http.StatusGatewayTimeout, codes.DeadlineExceeded},
		{http.StatusNotFound, codes.NotFound},
		{http.StatusConflict, codes.AlreadyExists},
		{http.StatusUnprocessableEntity, codes.OutOfRange},
		{http.StatusForbidden, codes.PermissionDenied},
		{http.StatusUnauthorized, codes.Unauthenticated},
		{http.StatusTooManyRequests, codes.ResourceExhausted},
		{http.StatusNotImplemented, codes.Unimplemented},
		{http.StatusInternalServerError, codes.Internal},
		{http.StatusServiceUnavailable, codes.Unavailable},
		{http.StatusTeapot, codes.Internal},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.httpCode), func(t *testing.T) {
			assert.Equal(t, tc.grpcCode, HttpStatusCodeToGrpcCode(tc.httpCode))
		})
	}
}
