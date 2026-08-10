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

// White-box integration tests that drive the orchestration code paths in
// worker.go (NewNVCFWorker, SetupWorkDirs, Run, workSession), work.go
// (handleWorkRequest, makeRestRequest, createInferenceRequest,
// handleRestResponse, downloadAssets, downloadAsset, streamRequestBody, the
// backwards-compatibility mapping and polling), large.go (largeResponse,
// multipartUpload), and refresh.go end-to-end against in-memory NATS
// JetStream, a mock NVCF gRPC API, and httptest inference/asset/S3 servers.
//
// Run/Setup are always invoked with withHttpServer == false so no HTTP health
// server binds during these tests. The mock NVCF gRPC API listens on
// 127.0.0.1:9090, the OTEL collector on 127.0.0.1:8360, and the asset/S3 mocks
// must bind 8001/8002 because the mock NVCF API returns those hardcoded URLs.
// Those upstream-hardcoded ports are shared with the service-package
// end-to-end test, so the two suites are serialized against each other via a
// cross-process file lock (see fixedPortLock_test.go).
//
// The inference server, by contrast, binds an OS-assigned ephemeral port and
// the worker config InferencePort is set to the actual listen port, and NATS
// runs on an embedded ephemeral-port server (newEmbeddedNats); neither
// collides across parallel package binaries.
package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

const (
	itAssetHost = "127.0.0.1:8001"
	itS3Host    = "127.0.0.1:8002"
	itNcaId     = "test-nca-function-owner"
)

// itScenario pairs a work request with the region it should be queued in.
type itScenario struct {
	Region string
	*pb.WorkerInvokeFunctionRequest
}

// itEnv holds the shared mock infrastructure for an integration sub-suite.
type itEnv struct {
	nats      *testutils.SuperCluster
	zapLogger *logs.ZapLogger
	cfg       Config
}

// newIntegrationEnv spins up the inference, asset, S3, NATS, and OTEL mocks and
// returns a ready-to-use config. All servers are torn down via t.Cleanup.
func newIntegrationEnv(t *testing.T, mutate func(*Config)) *itEnv {
	t.Helper()

	// The asset/S3 mocks (8001/8002) and the mock NVCF gRPC API (9090) bind
	// ports that the upstream testutils hardcode; serialize against the
	// service-package E2E suite that binds the same ports.
	lockFixedPorts(t)

	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.RedirectStdLog(zapLogger.GetZapLogger())
	t.Cleanup(func() { _ = zapLogger.Close() })

	// Inference binds an OS-assigned ephemeral port; the worker config is
	// pointed at the actual listen port so no fixed :8000 collision occurs.
	inference, err := testutils.NewInferenceServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("inference server: %v", err)
	}
	t.Cleanup(inference.Close)
	inferencePort := inference.Listener.Addr().(*net.TCPAddr).Port

	asset, err := testutils.NewAssetServer(itAssetHost)
	if err != nil {
		t.Fatalf("asset server: %v", err)
	}
	t.Cleanup(asset.Close)

	s3, err := testutils.NewS3Server(itS3Host)
	if err != nil {
		t.Fatalf("s3 server: %v", err)
	}
	t.Cleanup(s3.Close)

	natsSuperCluster, err := newEmbeddedNats(t)
	if err != nil {
		t.Fatalf("embedded nats: %v", err)
	}

	mc, err := testutils.NewMockCollector(zapLogger)
	if err != nil {
		t.Fatalf("otel collector: %v", err)
	}
	t.Cleanup(mc.Shutdown)

	cfg := Config{
		NVCFWorkerToken:          "fake-worker-token",
		NVCFFqdn:                 "http://127.0.0.1:9090",
		NVCFFqdnNATS:             natsSuperCluster.Clusters[0].Servers[0].ClientURL(),
		InferencePort:            inferencePort,
		InferenceHealthEndpoint:  "/health",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		NcaId:                    itNcaId,
		BillingNcaId:             itNcaId,
		FunctionId:               validFunctionId,
		FunctionVersionId:        validFunctionVersionId,
		FunctionName:             "test-echo-function",
		BaseAssetDir:             t.TempDir(),
		BaseResponseDir:          t.TempDir(),
		SharedConfigDir:          t.TempDir(),
		HealthPort:               9099,
		InstanceId:               "test-instance-id",
		MaxRequestConcurrency:    20,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return &itEnv{nats: natsSuperCluster, zapLogger: zapLogger, cfg: cfg}
}

// runWorker drives the worker through one DoWorkFunc run, returning after the
// work function completes or the context times out.
func (e *itEnv) runWorker(t *testing.T, workFunc testutils.DoWorkFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	workerServer, err := testutils.NewMockNVCFAPI(ctx, t, e.nats.Clusters[0].Servers[0].ClientURL(), workFunc, nil)
	if err != nil {
		t.Fatalf("mock nvcf api: %v", err)
	}
	defer workerServer.Shutdown()

	go func() {
		defer cancel()
		if err := workerServer.Wait(ctx); err != nil {
			t.Error(err)
		}
	}()

	nvcfWorker, err := NewNVCFWorker(ctx, e.zapLogger, e.cfg)
	if err != nil {
		t.Fatalf("NewNVCFWorker: %v", err)
	}
	if err := nvcfWorker.SetupWorkDirs(); err != nil {
		t.Fatalf("SetupWorkDirs: %v", err)
	}
	if err := nvcfWorker.Run(false); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
}

func defaultITRequest() *pb.WorkerInvokeFunctionRequest {
	return &pb.WorkerInvokeFunctionRequest{
		RequestId:                  uuid.New().String(),
		NcaId:                      itNcaId,
		Subject:                    "test-invoker-123",
		LargeResponseUrl:           "http://localhost:8002",
		MaxDirectResponseSizeBytes: 1024,
		RequestBody:                []byte(`{"abc": 123}`),
		RequestMethod:              http.MethodPost,
		RequestPath:                "/echo",
		RequestTime:                timestamppb.Now(),
	}
}

func TestIntegrationBackwardsCompatible(t *testing.T) {
	env := newIntegrationEnv(t, nil)

	t.Run("single request with asset", func(t *testing.T) {
		req := defaultITRequest()
		req.InputAssetReference = []*pb.InputAssetReference{
			{AssetId: "foo", Reference: "http://" + itAssetHost + "/some/asset", ContentType: "text/plain"},
		}
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("no assets", func(t *testing.T) {
		req := defaultITRequest()
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("multiple concurrent requests", func(t *testing.T) {
		scenarios := make([]itScenario, 25)
		for i := range scenarios {
			scenarios[i] = itScenario{Region: "region-1", WorkerInvokeFunctionRequest: defaultITRequest()}
		}
		env.runWorker(t, itConcurrent(env.cfg.FunctionVersionId, scenarios...))
	})

	t.Run("regional request", func(t *testing.T) {
		req := defaultITRequest()
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-2", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("large response single PUT", func(t *testing.T) {
		req := defaultITRequest()
		req.MaxDirectResponseSizeBytes = 100
		req.RequestBody = []byte(`{"abc": "` + strings.Repeat("a", 2000) + `"}`)
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("asset download failure returns 500", func(t *testing.T) {
		// An unreachable asset reference makes downloadAssets fail; the worker
		// must send a 500 error response (handleWorkRequest download-error
		// branch).
		req := defaultITRequest()
		req.InputAssetReference = []*pb.InputAssetReference{
			{AssetId: "bad", Reference: "http://127.0.0.1:1/some/asset"},
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			return itHandleWork(ctx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, func(resp *http.Response) error {
				if resp.StatusCode != 500 {
					return fmt.Errorf("expected 500, got %d", resp.StatusCode)
				}
				return nil
			})
		}
		env.runWorker(t, workFunc)
	})

	t.Run("streaming event stream", func(t *testing.T) {
		req := defaultITRequest()
		req.RequestHeaders = []*pb.StringKV{{Key: "Accept", Value: "text/event-stream"}}
		req.RequestPath = "/echo-stream"
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("old request within grace period", func(t *testing.T) {
		req := defaultITRequest()
		req.RequestTime = timestamppb.New(time.Now().Add(-500 * time.Millisecond))
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("old request rejected with internal error", func(t *testing.T) {
		req := defaultITRequest()
		req.RequestTime = timestamppb.New(time.Now().Add(-time.Hour))
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			return itHandleWork(ctx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, func(resp *http.Response) error {
				if resp.StatusCode != 500 {
					return fmt.Errorf("unexpected status: %d", resp.StatusCode)
				}
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), "Internal error while making inference request") {
					return fmt.Errorf("unexpected body: %s", string(body))
				}
				return nil
			})
		}
		env.runWorker(t, workFunc)
	})

	t.Run("polling backwards compat generated 202", func(t *testing.T) {
		req := defaultITRequest()
		req.RequestHeaders = []*pb.StringKV{{Key: "nvcf-poll-seconds", Value: "1"}}
		req.RequestPath = "/echo-with-delay"
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			if err := itHandleWork(ctx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, okHandler); err != nil {
				return err
			}
			req.RequestMethod = http.MethodGet
			_ = itHandlePolling(ctx, nc, req, "region-1", env.cfg.FunctionVersionId)
			return nil
		}
		env.runWorker(t, workFunc)
	})
}

// TestIntegrationResponseDirCreateFailure drives the handleWorkRequest branch
// where creating the per-request response directory fails. The worker's
// baseResponseDir is pointed at a regular file before Run, so os.Stat on the
// per-request response subdir returns a non-NotExist error and the worker must
// send a 500.
func TestIntegrationResponseDirCreateFailure(t *testing.T) {
	env := newIntegrationEnv(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	workerReady := make(chan struct{})
	workFunc := func(workCtx context.Context, nc *nats.Conn) error {
		select {
		case <-workerReady:
		case <-time.After(10 * time.Second):
			return errors.New("timeout waiting for worker")
		}
		req := defaultITRequest()
		return itHandleWork(workCtx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, func(resp *http.Response) error {
			if resp.StatusCode != 500 {
				return fmt.Errorf("expected 500, got %d", resp.StatusCode)
			}
			return nil
		})
	}

	workerServer, err := testutils.NewMockNVCFAPI(ctx, t, env.nats.Clusters[0].Servers[0].ClientURL(), workFunc, nil)
	if err != nil {
		t.Fatalf("mock nvcf api: %v", err)
	}
	defer workerServer.Shutdown()

	go func() {
		w, err := NewNVCFWorker(ctx, env.zapLogger, env.cfg)
		if err != nil {
			t.Error(err)
			return
		}
		if err := w.SetupWorkDirs(); err != nil {
			t.Error(err)
			return
		}
		// Point baseResponseDir at a regular file so per-request response dir
		// creation fails with a non-NotExist stat error.
		badBase := w.baseResponseDir + "-file"
		if err := os.WriteFile(badBase, []byte("x"), 0644); err != nil {
			t.Error(err)
			return
		}
		w.baseResponseDir = badBase
		close(workerReady)
		if err := w.Run(false); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	}()

	if err := workerServer.Wait(ctx); err != nil {
		t.Error(err)
	}
}

// TestIntegrationGracefulShutdown drives the graceful-shutdown path: after one
// request completes the test calls Shutdown(), which unwinds workSession (the
// shutdownCtx-done branches), unsubscribes the cancel subscription, and lets
// Run return cleanly.
func TestIntegrationGracefulShutdown(t *testing.T) {
	env := newIntegrationEnv(t, func(c *Config) { c.MaxRequestConcurrency = 2 })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	workerReady := make(chan *NVCFWorker, 1)
	workFunc := func(workCtx context.Context, nc *nats.Conn) error {
		var w *NVCFWorker
		select {
		case w = <-workerReady:
		case <-time.After(10 * time.Second):
			return errors.New("timeout waiting for worker")
		}
		req := defaultITRequest()
		if err := itHandleWork(workCtx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, okHandler); err != nil {
			return err
		}
		// Trigger graceful shutdown after a successful request.
		w.Shutdown()
		return nil
	}

	workerServer, err := testutils.NewMockNVCFAPI(ctx, t, env.nats.Clusters[0].Servers[0].ClientURL(), workFunc, nil)
	if err != nil {
		t.Fatalf("mock nvcf api: %v", err)
	}
	defer workerServer.Shutdown()

	runErr := make(chan error, 1)
	go func() {
		w, err := NewNVCFWorker(ctx, env.zapLogger, env.cfg)
		if err != nil {
			runErr <- err
			return
		}
		if err := w.SetupWorkDirs(); err != nil {
			runErr <- err
			return
		}
		workerReady <- w
		runErr <- w.Run(false)
	}()

	if err := workerServer.Wait(ctx); err != nil {
		t.Error(err)
	}
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("worker Run did not return after shutdown")
	}
}

// TestIntegrationESSTokenRefresh runs the worker with an ESS assertion token
// configured so the assertion-token refresher branch of Run is exercised.
func TestIntegrationESSTokenRefresh(t *testing.T) {
	env := newIntegrationEnv(t, func(c *Config) {
		c.SecretsAssertionToken = "fake-assertion-token"
		c.EssAgentConfigDir = t.TempDir()
	})
	req := defaultITRequest()
	env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
}

// TestIntegrationStateful drives a stateful work request through
// handleWorkRequest -> handleStatefulWorkRequest -> httpProxy.Proxy. With no
// stateful peer attached the proxy attempt fails, but the message is still
// acked; we only need the worker to consume and process it.
func TestIntegrationStateful(t *testing.T) {
	env := newIntegrationEnv(t, nil)
	req := defaultITRequest()
	req.StatefulConfig = &pb.WorkerInvokeFunctionRequest_StatefulConfig{
		NvcfRegionOfInvoker: "region-1",
	}
	workFunc := func(ctx context.Context, nc *nats.Conn) error {
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		defer js.CleanupPublisher()
		requestSubject := fmt.Sprintf("rq.%s.%s.%s", "region-1", env.cfg.FunctionVersionId, req.RequestId)
		data, err := proto.Marshal(req)
		if err != nil {
			return err
		}
		pubAck, err := js.Publish(ctx, requestSubject, data)
		if err != nil {
			return err
		}
		// Wait for the worker to consume and ack (delete) the stateful message.
		stream, err := js.Stream(ctx, pubAck.Stream)
		if err != nil {
			return err
		}
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = 10 * time.Millisecond
		bo.MaxInterval = 200 * time.Millisecond
		return backoff.Retry(func() error {
			msg, err := stream.GetMsg(ctx, pubAck.Sequence)
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if msg != nil {
				return errors.New("stateful work not yet acked")
			}
			return nil
		}, backoff.WithContext(backoff.WithMaxRetries(bo, 20), ctx))
	}
	env.runWorker(t, workFunc)
}

func TestIntegrationNonBackwardsCompatible(t *testing.T) {
	env := newIntegrationEnv(t, func(c *Config) {
		c.V3BackwardsCompatibilityDisabled = true
	})

	t.Run("old request refreshes asset and large-response credentials", func(t *testing.T) {
		// An old (>30m) request forces getValidAssets and
		// getValidLargeResponseUrl down their credential-refresh branches via
		// the mock NVCF API. Compatibility is disabled here so the request is
		// not rejected by the backwards-compat poll-duration guard.
		req := defaultITRequest()
		req.RequestTime = timestamppb.New(time.Now().Add(-time.Hour))
		req.MaxDirectResponseSizeBytes = 100
		req.RequestBody = []byte(`{"abc": "` + strings.Repeat("z", 2000) + `"}`)
		req.InputAssetReference = []*pb.InputAssetReference{
			{AssetId: "foo", Reference: "http://" + itAssetHost + "/some/asset", ContentType: "text/plain"},
		}
		env.runWorker(t, itSerial(env.cfg.FunctionVersionId, itScenario{Region: "region-1", WorkerInvokeFunctionRequest: req}))
	})

	t.Run("inference-driven 202 polling", func(t *testing.T) {
		req := defaultITRequest()
		req.RequestHeaders = []*pb.StringKV{{Key: "nvcf-poll-seconds", Value: "1"}}
		req.RequestPath = "/echo-with-status/202"
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			req.RequestTime = timestamppb.Now()
			if err := itHandleWork(ctx, nc, req, "region-1", env.cfg.FunctionVersionId, nil, okHandler); err != nil {
				return err
			}
			for i := 0; i < 3; i++ {
				req.RequestTime = timestamppb.Now()
				if err := itHandlePolling(ctx, nc, req, "region-1", env.cfg.FunctionVersionId); err != nil {
					return nil
				}
			}
			req.RequestTime = timestamppb.Now()
			req.RequestPath = "/echo-with-status/201"
			return itHandlePolling(ctx, nc, req, "region-1", env.cfg.FunctionVersionId)
		}
		env.runWorker(t, workFunc)
	})
}

// okHandler asserts the inference response status is < 400.
func okHandler(resp *http.Response) error {
	if resp.StatusCode >= 400 {
		return fmt.Errorf("unexpected inference response: %d", resp.StatusCode)
	}
	return nil
}

func itSerial(functionVersionId string, scenarios ...itScenario) testutils.DoWorkFunc {
	return func(ctx context.Context, nc *nats.Conn) error {
		for _, s := range scenarios {
			if err := itHandleWork(ctx, nc, s.WorkerInvokeFunctionRequest, s.Region, functionVersionId, nil, okHandler); err != nil {
				return err
			}
		}
		return nil
	}
}

func itConcurrent(functionVersionId string, scenarios ...itScenario) testutils.DoWorkFunc {
	return func(ctx context.Context, nc *nats.Conn) error {
		group, ctx := errgroup.WithContext(ctx)
		for _, s := range scenarios {
			s := s
			group.Go(func() error {
				return itHandleWork(ctx, nc, s.WorkerInvokeFunctionRequest, s.Region, functionVersionId, nil, okHandler)
			})
		}
		return group.Wait()
	}
}

// itHandleWork acts as the invocation service: it stands up an HTTP server that
// serves the client request body (GET) and receives the worker response
// (POST), publishes the work request onto the JetStream request subject, and
// waits for the worker to attach its response.
func itHandleWork(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string, bodyProducer io.Reader, responseConsumer func(*http.Response) error) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	defer js.CleanupPublisher()

	requestKey := uuid.New().String()
	responseKey := uuid.New().String()
	finished := make(chan error, 1)
	var completedStatus int

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+responseKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- errors.New("unauthorized response attach")
			return
		}
		defer r.Body.Close()
		inferenceResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		defer inferenceResponse.Body.Close()
		completedStatus = inferenceResponse.StatusCode
		if responseConsumer != nil {
			if err := responseConsumer(inferenceResponse); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				finished <- err
				return
			}
		} else {
			_, _ = httputil.DumpResponse(inferenceResponse, false)
		}
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		finished <- nil
	})
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+requestKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- errors.New("unauthorized request attach")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if bodyProducer != nil {
			fw := &itFlushWriter{Writer: w, Flusher: w.(http.Flusher)}
			_, _ = io.Copy(fw, bodyProducer)
		}
	})

	isServer := httptest.NewServer(mux)
	defer isServer.Close()

	work.StatelessConfig = itStatelessConfig(isServer.URL, requestKey, responseKey)

	requestSubject := fmt.Sprintf("rq.%s.%s.%s", region, functionVersionId, work.RequestId)
	data, err := proto.Marshal(work)
	if err != nil {
		return err
	}
	pubAck, err := js.Publish(ctx, requestSubject, data)
	if err != nil {
		return err
	}

	select {
	case err = <-finished:
	case <-ctx.Done():
		return errors.New("timeout waiting for response")
	}
	if err != nil {
		return err
	}

	// Wait for the worker to ack (delete) the message, unless we left it
	// unacked intentionally (202 = expecting more polling requests).
	stream, err := js.Stream(ctx, pubAck.Stream)
	if err != nil {
		return err
	}
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 10 * time.Millisecond
	bo.MaxInterval = 200 * time.Millisecond
	err = backoff.Retry(func() error {
		msg, err := stream.GetMsg(ctx, pubAck.Sequence)
		if completedStatus == http.StatusAccepted {
			if err != nil || msg == nil {
				return backoff.Permanent(errors.New("expected work to remain, but it is gone"))
			}
			return nil
		}
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				return nil
			}
			return err
		}
		if msg != nil {
			return errors.New("expected work to be deleted")
		}
		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(bo, 5), ctx))
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// itHandlePolling acts as the invocation service sending a single polling
// request over NATS request-reply and waiting for the attached response.
func itHandlePolling(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string) error {
	requestKey := uuid.New().String()
	responseKey := uuid.New().String()
	finished := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+responseKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- errors.New("unauthorized")
			return
		}
		defer r.Body.Close()
		inferenceResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		defer inferenceResponse.Body.Close()
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		if inferenceResponse.StatusCode >= 400 {
			finished <- fmt.Errorf("unexpected inference response: %d", inferenceResponse.StatusCode)
			return
		}
		finished <- nil
	})
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	})

	isServer := httptest.NewServer(mux)
	defer isServer.Close()

	work.StatelessConfig = itStatelessConfig(isServer.URL, requestKey, responseKey)

	subject := "rq_polling." + work.RequestId
	data, _ := proto.Marshal(work)
	if _, err := nc.RequestWithContext(ctx, subject, data); err != nil {
		return err
	}

	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		return errors.New("timeout waiting for polling response")
	}
}

func itStatelessConfig(targetURI, requestKey, responseKey string) *pb.WorkerInvokeFunctionRequest_StatelessConfig {
	return &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  targetURI,
						ResponseAuthorizationToken: responseKey,
						RequestAuthorizationToken:  &requestKey,
					},
				},
			},
		},
	}
}

type itFlushWriter struct {
	io.Writer
	http.Flusher
}

func (w *itFlushWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.Flusher.Flush()
	return n, err
}
