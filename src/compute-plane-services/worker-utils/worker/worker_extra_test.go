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

// White-box tests for lifecycle helpers (Setup, Shutdown, SetupWorkDirs) and
// the stateful and asset-download paths that are not naturally covered by the
// end-to-end happy path.
package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy"
)

// TestSetupAndShutdown exercises Setup (which prepares the work dirs and the
// grpc framework server without binding listener ports) and Shutdown (which
// cancels the shutdown context). Setup registers routes on
// http.DefaultServeMux, so it is only safe to call once per test binary; this
// is the single Setup caller.
func TestSetupAndShutdown(t *testing.T) {
	cfg := baseValidConfig(t)
	cfg.HealthPort = 9098
	w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.NoError(t, err)

	require.NoError(t, w.Setup())

	// Shutdown cancels the shutdown context.
	require.NoError(t, w.shutdownCtx.Err())
	w.Shutdown()
	assert.Error(t, w.shutdownCtx.Err())
}

// TestSetupWorkDirsCreatesMissing drives the create-new branch of SetupWorkDirs
// by pointing the dirs at paths that do not yet exist.
func TestSetupWorkDirsCreatesMissing(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "assets", "nested")
	responseDir := filepath.Join(root, "response", "nested")

	w := &NVCFWorker{baseAssetDir: assetDir, baseResponseDir: responseDir}
	require.NoError(t, w.SetupWorkDirs())

	for _, d := range []string{assetDir, responseDir} {
		info, err := os.Stat(d)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	}
}

// TestSetupWorkDirsStatError drives the SetupWorkDirs branch where os.Stat
// returns an error other than ErrNotExist: the response dir path traverses
// through a regular file, so Stat fails with ENOTDIR.
func TestSetupWorkDirsStatError(t *testing.T) {
	root := t.TempDir()
	fileAsParent := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(fileAsParent, []byte("x"), 0644))

	w := &NVCFWorker{
		baseResponseDir: filepath.Join(fileAsParent, "response"),
		baseAssetDir:    filepath.Join(root, "assets"),
	}
	assert.Error(t, w.SetupWorkDirs())
}

// TestHandleStatefulWorkRequest builds a real HttpProxy on a NATS connection
// from an in-memory supercluster and drives handleStatefulWorkRequest. With no
// stateful peer attached the proxy returns an error, which exercises both the
// proxy invocation and the error-logging branch of stateful.go.
func TestHandleStatefulWorkRequest(t *testing.T) {
	sc, err := newEmbeddedNats(t)
	require.NoError(t, err)

	nc, err := nats.Connect(sc.Clusters[0].Servers[0].ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	httpProxy, err := proxy.NewHttpProxy(nc, js, validFunctionId, validFunctionVersionId, func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = "http"
		r.Out.URL.Host = "127.0.0.1:8000"
	}, nil, nil, nil)
	require.NoError(t, err)
	defer httpProxy.Close()

	w := &NVCFWorker{httpProxy: httpProxy}
	work := &pb.WorkerInvokeFunctionRequest{
		RequestId: "stateful-req",
		StatefulConfig: &pb.WorkerInvokeFunctionRequest_StatefulConfig{
			NvcfRegionOfInvoker: "region-1",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled context makes the proxy attempt fail fast
	err = w.handleStatefulWorkRequest(ctx, work, "region-1")
	assert.Error(t, err)
}

// TestDownloadAssetErrors drives downloadAsset failure branches against an
// asset server: a non-200 response and an unreachable host both surface errors.
func TestDownloadAssetErrors(t *testing.T) {
	cfg := baseValidConfig(t)
	w, err := NewNVCFWorker(context.Background(), newTestLogger(t), cfg)
	require.NoError(t, err)

	assetDir := t.TempDir()

	t.Run("non-200 response", func(t *testing.T) {
		srv := newStatusAssetServer(http.StatusNotFound)
		defer srv.Close()
		err := w.downloadAsset(context.Background(), "req", assetDir, &pb.InputAssetReference{
			AssetId: "missing", Reference: srv.URL + "/asset",
		})
		assert.Error(t, err)
	})

	t.Run("unreachable host", func(t *testing.T) {
		err := w.downloadAsset(context.Background(), "req", assetDir, &pb.InputAssetReference{
			AssetId: "x", Reference: "http://127.0.0.1:1/asset",
		})
		assert.Error(t, err)
	})

	t.Run("open file error when asset dir is a file", func(t *testing.T) {
		srv := newStatusAssetServer(http.StatusOK)
		defer srv.Close()
		// assetDir points at a regular file, so OpenFile under it fails.
		fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(fileAsDir, []byte("x"), 0644))
		err := w.downloadAsset(context.Background(), "req", fileAsDir, &pb.InputAssetReference{
			AssetId: "a", Reference: srv.URL + "/asset",
		})
		assert.Error(t, err)
	})

	t.Run("success writes file", func(t *testing.T) {
		srv := newStatusAssetServer(http.StatusOK)
		defer srv.Close()
		ref := &pb.InputAssetReference{AssetId: "ok-asset", Reference: srv.URL + "/asset"}
		require.NoError(t, w.downloadAsset(context.Background(), "req", assetDir, ref))
		_, statErr := os.Stat(filepath.Join(assetDir, "ok-asset"))
		assert.NoError(t, statErr)
	})
}

// newStatusAssetServer returns an httptest server that replies with the given
// status code and a small body for any request.
func newStatusAssetServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("asset-bytes"))
	}))
}
