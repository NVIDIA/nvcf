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

package rp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInjectGrpcSupportToReverseProxy_NoExistingModifyResponse(t *testing.T) {
	rp := &httputil.ReverseProxy{}
	err := InjectGrpcSupportToReverseProxy(rp)
	require.NoError(t, err)
	require.NotNil(t, rp.ModifyResponse)
	require.NotNil(t, rp.ErrorHandler)

	// The injected ModifyResponse is the plain modifyResponse. For an http/1
	// response it should be a no-op (nil).
	resp := &http.Response{ProtoMajor: 1, Body: io.NopCloser(strings.NewReader(""))}
	require.NoError(t, rp.ModifyResponse(resp))
}

func TestInjectGrpcSupportToReverseProxy_ChainsExistingModifyResponse(t *testing.T) {
	called := false
	rp := &httputil.ReverseProxy{
		ModifyResponse: func(*http.Response) error {
			called = true
			return nil
		},
	}
	err := InjectGrpcSupportToReverseProxy(rp)
	require.NoError(t, err)

	resp := &http.Response{ProtoMajor: 1, Body: io.NopCloser(strings.NewReader(""))}
	require.NoError(t, rp.ModifyResponse(resp))
	require.True(t, called, "existing ModifyResponse must still be invoked")
}

func TestInjectGrpcSupportToReverseProxy_ExistingModifyResponseError(t *testing.T) {
	wantErr := errors.New("existing mr failed")
	rp := &httputil.ReverseProxy{
		ModifyResponse: func(*http.Response) error { return wantErr },
	}
	require.NoError(t, InjectGrpcSupportToReverseProxy(rp))

	resp := &http.Response{ProtoMajor: 1, Body: io.NopCloser(strings.NewReader(""))}
	err := rp.ModifyResponse(resp)
	require.ErrorIs(t, err, wantErr)
}

func TestInjectGrpcSupportToReverseProxy_RejectsExistingErrorHandler(t *testing.T) {
	rp := &httputil.ReverseProxy{
		ErrorHandler: func(http.ResponseWriter, *http.Request, error) {},
	}
	err := InjectGrpcSupportToReverseProxy(rp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "errorHandler must not be set")
}

// eofBody is a body whose Read always returns io.EOF, simulating a zero-length
// grpc response body.
type eofBody struct{}

func (eofBody) Read([]byte) (int, error) { return 0, io.EOF }
func (eofBody) Close() error             { return nil }

// dataBody returns data and never EOF on a nil read.
type dataBody struct{}

func (dataBody) Read(p []byte) (int, error) { return 0, nil }
func (dataBody) Close() error               { return nil }

func TestModifyResponse_Http2EmptyBodyDetected(t *testing.T) {
	resp := &http.Response{ProtoMajor: 2, StatusCode: http.StatusOK, Body: eofBody{}}
	err := modifyResponse(resp)
	require.Error(t, err)
	var smuggled smuggledResponseBodyError
	require.True(t, errors.As(err, &smuggled))
	require.Same(t, resp, smuggled.resp)
}

func TestModifyResponse_Http2WithBodyNoError(t *testing.T) {
	resp := &http.Response{ProtoMajor: 2, StatusCode: http.StatusOK, Body: dataBody{}}
	require.NoError(t, modifyResponse(resp))
}

func TestModifyResponse_SwitchingProtocolsSkipped(t *testing.T) {
	// 101 responses are handled differently and must be skipped even on http/2.
	resp := &http.Response{ProtoMajor: 2, StatusCode: http.StatusSwitchingProtocols, Body: eofBody{}}
	require.NoError(t, modifyResponse(resp))
}

func TestModifyResponse_Http1Skipped(t *testing.T) {
	resp := &http.Response{ProtoMajor: 1, StatusCode: http.StatusOK, Body: eofBody{}}
	require.NoError(t, modifyResponse(resp))
}

func TestErrorHandler_SmuggledBodyCopiesHeadersAndTrailers(t *testing.T) {
	upstream := &http.Response{
		StatusCode: http.StatusTeapot,
		Header: http.Header{
			"Content-Type": []string{"application/grpc"},
			"X-Custom":     []string{"a", "b"},
		},
		Trailer: http.Header{
			"Grpc-Status":  []string{"0"},
			"Grpc-Message": []string{"ok"},
		},
	}
	smuggled := smuggledResponseBodyError{resp: upstream, error: errNoBodyDetected}

	rec := httptest.NewRecorder()
	errorHandler(rec, httptest.NewRequest(http.MethodPost, "/", nil), smuggled)

	require.Equal(t, http.StatusTeapot, rec.Code)
	require.Equal(t, "application/grpc", rec.Header().Get("Content-Type"))
	require.Equal(t, []string{"a", "b"}, rec.Header()["X-Custom"])
	// Trailers are emitted with the TrailerPrefix.
	require.Equal(t, "0", rec.Header().Get(http.TrailerPrefix+"Grpc-Status"))
	require.Equal(t, "ok", rec.Header().Get(http.TrailerPrefix+"Grpc-Message"))
}

func TestErrorHandler_RealErrorReturnsBadGateway(t *testing.T) {
	rec := httptest.NewRecorder()
	errorHandler(rec, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("dial failed"))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestCopyHeader(t *testing.T) {
	src := http.Header{
		"A": []string{"1", "2"},
		"B": []string{"3"},
	}
	dst := http.Header{}
	copyHeader(dst, src)
	require.Equal(t, []string{"1", "2"}, dst["A"])
	require.Equal(t, []string{"3"}, dst["B"])
}
