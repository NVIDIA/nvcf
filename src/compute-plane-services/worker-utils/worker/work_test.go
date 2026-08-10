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

// White-box tests for createInferenceRequest and the downloadAssets error
// branch, which are awkward to reach deterministically through the end-to-end
// flow.
package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/httpstream"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

// newRequestStreamHandler stands up a minimal request-attach server (the GET
// endpoint NewRequestStreamHandler fetches the client body from on
// construction) and returns a wired RequestStreamHandler.
func newRequestStreamHandler(t *testing.T) *httpstream.RequestStreamHandler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	reqKey := "req-key"
	cfg := &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  srv.URL,
						ResponseAuthorizationToken: "resp-key",
						RequestAuthorizationToken:  &reqKey,
					},
				},
			},
		},
	}
	rsh, err := httpstream.NewRequestStreamHandler(context.Background(), httpstream.NewProxiedClient(), cfg, &pb.WorkerInvokeFunctionRequest{RequestMethod: http.MethodPost})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rsh.Close() })
	return rsh
}

func newInferenceRequestWorker() *NVCFWorker {
	return &NVCFWorker{
		config: Config{
			FunctionName:      "fn-name",
			FunctionId:        validFunctionId,
			FunctionVersionId: validFunctionVersionId,
		},
		inferenceUrlWithoutPath: "http://127.0.0.1:65000",
		meteringConfig:          &metering.Config{Backend: "GFN", InstanceType: "g1", ZoneName: "z1", ICMSEnvironment: "prod"},
	}
}

func TestCreateInferenceRequest(t *testing.T) {
	w := newInferenceRequestWorker()
	rsh := newRequestStreamHandler(t)

	t.Run("sets nvcf headers, asset ids, poll seconds and content length", func(t *testing.T) {
		work := &pb.WorkerInvokeFunctionRequest{
			RequestId:     "req-1",
			Subject:       "sub",
			NcaId:         "nca",
			RequestMethod: http.MethodPost,
			RequestPath:   "/infer?x=1",
			RequestTime:   timestamppb.Now(),
			RequestHeaders: []*pb.StringKV{
				{Key: "nvcf-poll-seconds", Value: "45"},
				{Key: "Content-Length", Value: "123"},
			},
			InputAssetReference: []*pb.InputAssetReference{
				{AssetId: "a1"}, {AssetId: "a2"},
			},
		}
		req, shifted, target, err := w.createInferenceRequest(context.Background(), work, rsh, "/assets", "/resp")
		require.NoError(t, err)
		// The production code stores literal upper-case NVCF header keys to
		// preserve case, so they must be read via direct map indexing.
		assert.Equal(t, "req-1", req.Header["NVCF-REQID"][0])
		assert.Equal(t, "fn-name", req.Header["NVCF-FUNCTION-NAME"][0])
		assert.Equal(t, "a1,a2", req.Header["NVCF-FUNCTION-ASSET-IDS"][0])
		assert.Equal(t, "application/json", req.Header.Get("Content-Type")) // defaulted
		assert.Equal(t, int64(123), req.ContentLength)
		assert.Equal(t, 45*time.Second, target)
		assert.LessOrEqual(t, shifted, 45*time.Second)
		assert.Equal(t, strconv.Itoa(int(shifted.Seconds())), req.Header.Get("nvcf-poll-seconds"))
	})

	t.Run("invalid method returns error", func(t *testing.T) {
		work := &pb.WorkerInvokeFunctionRequest{
			RequestId:     "req-2",
			RequestMethod: "BAD METHOD WITH SPACES",
			RequestPath:   "/infer",
			RequestTime:   timestamppb.Now(),
		}
		_, _, _, err := w.createInferenceRequest(context.Background(), work, rsh, "/assets", "/resp")
		assert.Error(t, err)
	})

	t.Run("content-type preserved when supplied", func(t *testing.T) {
		work := &pb.WorkerInvokeFunctionRequest{
			RequestId:      "req-3",
			RequestMethod:  http.MethodPost,
			RequestPath:    "/infer",
			RequestTime:    timestamppb.Now(),
			RequestHeaders: []*pb.StringKV{{Key: "Content-Type", Value: "application/octet-stream"}},
		}
		req, _, _, err := w.createInferenceRequest(context.Background(), work, rsh, "/assets", "/resp")
		require.NoError(t, err)
		assert.Equal(t, "application/octet-stream", req.Header.Get("Content-Type"))
	})
}

func TestCreateInferenceRequest_CompatDisabledNoContentTypeDefault(t *testing.T) {
	w := newInferenceRequestWorker()
	w.config.V3BackwardsCompatibilityDisabled = true
	rsh := newRequestStreamHandler(t)

	work := &pb.WorkerInvokeFunctionRequest{
		RequestId:     "req",
		RequestMethod: http.MethodPost,
		RequestPath:   "/infer",
		RequestTime:   timestamppb.Now(),
	}
	req, _, _, err := w.createInferenceRequest(context.Background(), work, rsh, "/assets", "/resp")
	require.NoError(t, err)
	// Compatibility disabled: no default Content-Type is injected.
	assert.Empty(t, req.Header.Get("Content-Type"))
}

// TestDownloadAssetsMkdirError drives the downloadAssets failure branch where
// the asset directory cannot be created because its parent does not exist.
func TestDownloadAssetsMkdirError(t *testing.T) {
	w := &NVCFWorker{}
	badAssetDir := filepath.Join(t.TempDir(), "missing-parent", "assets")
	work := &pb.WorkerInvokeFunctionRequest{
		RequestId: "req",
		InputAssetReference: []*pb.InputAssetReference{
			{AssetId: "a1", Reference: "http://127.0.0.1:1/x"},
		},
	}
	err := w.downloadAssets(context.Background(), work, badAssetDir)
	assert.Error(t, err)
}

// TestDownloadAssetsNoAssets short-circuits when there are no input assets.
func TestDownloadAssetsNoAssets(t *testing.T) {
	w := &NVCFWorker{}
	err := w.downloadAssets(context.Background(), &pb.WorkerInvokeFunctionRequest{RequestId: "req"}, t.TempDir())
	assert.NoError(t, err)
}
