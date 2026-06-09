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

package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProfilerEntrypoint(t *testing.T) {
	t.SkipNow()
	go Run()
	time.Sleep(20 * time.Second)
}

func TestSuccessfulEndToEnd(t *testing.T) {
	zapLogger := setupLogs(t)
	inferenceServerHost := "127.0.0.1:8000"
	assetServerHost := "127.0.0.1:8001"
	largeResponseServerHost := "127.0.0.1:8002"
	ncaId := "test-nca-function-owner"

	// Start inference mock
	server, err := testutils.NewInferenceServer(inferenceServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// start asset mock
	asset, err := testutils.NewAssetServer(assetServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()

	// start large response s3 mock
	largeResponse, err := testutils.NewS3Server(largeResponseServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer largeResponse.Close()

	// start nats server
	natsSuperCluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer natsSuperCluster.Shutdown()

	// Start otel mock
	mc, err := testutils.NewMockCollector(zapLogger)
	if err != nil {
		t.Fatal(err)
	}

	defer mc.Shutdown()

	workerConfig := worker.Config{
		NVCFWorkerToken:          "fake-worker-token",
		NVCFFqdn:                 "http://127.0.0.1:9090",
		NVCFFqdnNATS:             natsSuperCluster.Clusters[0].Servers[0].ClientURL(),
		InferencePort:            8000,
		InferenceHealthEndpoint:  "/health",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		NcaId:                    ncaId,
		BillingNcaId:             ncaId,
		FunctionId:               "10b076eb-b6d2-4cd9-878b-a3614a931570",
		FunctionVersionId:        "f85f1808-966c-4ac5-8e19-1c6defadb891",
		FunctionName:             "test-echo-function",
		BaseAssetDir:             t.TempDir(),
		BaseResponseDir:          t.TempDir(),
		SharedConfigDir:          t.TempDir(),
		HealthPort:               9093,
		InstanceId:               "test-instance-id",
		MaxRequestConcurrency:    20,
	}

	t.Run("single work request", func(t *testing.T) {
		invokeFunctionRequest := defaultInferenceRequest(ncaId, assetServerHost)
		workFunc := serial(workerConfig.FunctionVersionId, invokeFunctionRequest)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("multiple work requests", func(t *testing.T) {
		requests := make([]Scenario, 100)
		for i := range requests {
			requests[i] = defaultInferenceRequest(ncaId, assetServerHost)
		}
		workFunc := serial(workerConfig.FunctionVersionId, requests...)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("multiple concurrent work requests", func(t *testing.T) {
		requests := make([]Scenario, 100)
		for i := range requests {
			requests[i] = defaultInferenceRequest(ncaId, assetServerHost)
		}
		workFunc := concurrent(workerConfig.FunctionVersionId, requests...)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("no assets", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestPath:                "/echo",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := serial(workerConfig.FunctionVersionId, Scenario{
			Region:                      "region-1",
			WorkerInvokeFunctionRequest: invokeFunctionRequest,
		})
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	// TODO also test large multipart response (over 20MB)
	t.Run("large response (single PUT)", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 100,
			RequestBody:                []byte(`{"abc": "` + strings.Repeat("a", 1000) + `"}`),
			RequestMethod:              http.MethodPost,
			RequestPath:                "/echo",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := serial(workerConfig.FunctionVersionId, Scenario{
			Region:                      "region-1",
			WorkerInvokeFunctionRequest: invokeFunctionRequest,
		})
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("regional work request", func(t *testing.T) {
		invokeFunctionRequest := regionalInferenceRequest(ncaId, "region-2")
		workFunc := serial(workerConfig.FunctionVersionId, invokeFunctionRequest)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("multiple regional work requests", func(t *testing.T) {
		regions := []string{"region-1", "region-2", "region-3"}
		requests := make([]Scenario, 100)
		for i := range requests {
			requests[i] = regionalInferenceRequest(ncaId, regions[i%len(regions)])
		}
		workFunc := serial(workerConfig.FunctionVersionId, requests...)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("multiple concurrent regional work requests", func(t *testing.T) {
		regions := []string{"region-1", "region-2", "region-3"}
		requests := make([]Scenario, 100)
		for i := range requests {
			requests[i] = regionalInferenceRequest(ncaId, regions[i%len(regions)])
		}
		workFunc := concurrent(workerConfig.FunctionVersionId, requests...)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("provision streams if absent", func(t *testing.T) {
		t.SkipNow() // racy test
		regions := []string{"region-1", "region-2", "region-3"}
		requests := make([]Scenario, 100)
		for i := range requests {
			requests[i] = regionalInferenceRequest(ncaId, regions[i%len(regions)])
		}
		var workFunc testutils.DoWorkFunc = func(ctx context.Context, nc *nats.Conn) error {
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			defer js.CleanupPublisher()
			// wait for streams to get created by the worker
			err = backoff.Retry(func() error {
				streams := lo.ChannelToSlice(js.StreamNames(ctx).Name())
				for _, region := range regions {
					prefix := "rq_" + region
					found := false
					for _, stream := range streams {
						if strings.HasPrefix(stream, prefix) {
							found = true
						}
					}
					if !found {
						return errors.New("did not find " + prefix)
					}
				}
				return nil
			}, backoff.WithContext(backoff.NewExponentialBackOff(), ctx))
			if err != nil {
				return err
			}

			return concurrent(workerConfig.FunctionVersionId, requests...)(ctx, nc)
		}

		t.Logf("starting test %s", t.Name())
		t.Cleanup(func() {
			t.Logf("finishing test %s", t.Name())
		})
		ctx, cancel := contextWithCancelAndTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Start nvcf api grpc mock
		workerServer, err := testutils.NewMockNVCFAPI(ctx, t, natsSuperCluster.Clusters[0].Servers[0].ClientURL(), workFunc, []string{"region-2"})
		if err != nil {
			t.Fatal(err)
		}

		defer workerServer.Shutdown()
		// cancel when the work scenarios have finished
		go func() {
			defer cancel()
			if err := workerServer.Wait(ctx); err != nil {
				t.Error(err)
			}
		}()

		err = runTestWorker(ctx, zapLogger, workerConfig)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("assertion token refresh", func(t *testing.T) {
		t.Logf("starting test %s", t.Name())

		ctx, cancel := contextWithCancelAndTimeout(context.Background(), 10*time.Second)
		defer cancel()

		workerServer, err := testutils.NewMockNVCFAPI(ctx, t, natsSuperCluster.Clusters[0].Servers[0].ClientURL(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer workerServer.Shutdown()

		client, err := nvcf.CreateClient(
			workerConfig.NVCFFqdn,
			lo.ToPtr(natsSuperCluster.Clusters[0].Servers[0].ClientURL()),
			workerConfig.NVCFWorkerToken,
			workerConfig.NKeySeed,
			workerConfig.NcaId,
			workerConfig.InstanceId,
			workerConfig.FunctionId,
			workerConfig.FunctionVersionId,
			workerConfig.SharedConfigDir,
			nvcf.DefaultNvcfClientTimeout,
		)
		if err != nil {
			t.Fatal(err)
		}

		tokenPath := filepath.Join(t.TempDir(), worker.EssTokenFileName)
		client.StartAssertionTokenRefresher(ctx, tokenPath, false)
		prevToken := ""

		for i := 0; i < 2; i++ {
			time.Sleep(2 * time.Second)

			tokenBytes, err := os.ReadFile(tokenPath)
			if err != nil {
				t.Fatal(err)
			}

			token := string(tokenBytes)
			if !strings.HasPrefix(token, workerConfig.FunctionId) {
				t.Fatalf("invalid token format")
			}

			if token == prevToken {
				t.Fatalf("token is not refreshed")
			}

			prevToken = token
		}
	})

	t.Run("streaming", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestHeaders:             []*pb.StringKV{{Key: "Accept", Value: "text/event-stream"}},
			RequestPath:                "/echo-stream",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := serial(workerConfig.FunctionVersionId, Scenario{
			Region:                      "region-1",
			WorkerInvokeFunctionRequest: invokeFunctionRequest,
		})
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("polling", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestHeaders:             []*pb.StringKV{{Key: "nvcf-poll-seconds", Value: "1"}},
			RequestPath:                "/echo-with-delay",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			err := handleWorkRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
			if err != nil {
				return err
			}
			t.Log("initial polling request returned")
			invokeFunctionRequest.RequestMethod = http.MethodGet
			err = handlePollingRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
			if err != nil {
				return nil
			}
			return nil
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("streaming request", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte("data seq 0\n"),
			RequestMethod:              http.MethodPost,
			RequestHeaders:             []*pb.StringKV{{Key: "Accept", Value: "text/event-stream"}},
			RequestPath:                "/echo-stream",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			buf := &bytes.Buffer{}
			for seq := 1; seq <= 10; seq++ {
				buf.WriteString(fmt.Sprintf("data seq %d\n", seq))
			}
			return handleWorkRequestCustomHandler(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId, buf, func(inferenceResponse *http.Response) error {
				if inferenceResponse.StatusCode >= 400 {
					return fmt.Errorf("unexpected inference response: %d", inferenceResponse.StatusCode)
				}
				return nil
			})
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("fully streamed request", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestTime:                timestamppb.Now(),
		}
		clientRequest := &http.Request{
			Method: http.MethodPost,
			URL:    lo.Must(url.Parse("http://dummy/echo")),
			Header: make(http.Header),
			Body:   io.NopCloser(bytes.NewReader([]byte(`{"abc": 123}`))),
		}
		clientRequest.Header.Set("Accept", "application/json")
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			buf := &bytes.Buffer{}
			for seq := 1; seq <= 10; seq++ {
				buf.WriteString(fmt.Sprintf("data seq %d\n", seq))
			}
			return handleGetRequestEncodedWorkRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId, clientRequest)
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("large request body", func(t *testing.T) {
		a32k := []byte(strings.Repeat("a", 32*1024))
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                a32k,
			RequestMethod:              http.MethodPost,
			RequestPath:                "/body-counter",
			RequestTime:                timestamppb.Now(),
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			sub, err := nc.Subscribe("readyForData", func(msg *nats.Msg) {
				seq := 1
				for ; seq < 1024; seq++ {
					resp, err := nc.RequestMsgWithContext(ctx, &nats.Msg{
						Subject: msg.Reply,
						Header: nats.Header{
							"nvcf-seq": []string{strconv.Itoa(seq)},
						},
						Data: a32k,
					})
					if err != nil {
						t.Fatal(err)
					}
					if string(resp.Data) == "NAK" {
						t.Fatal("NAK")
					}
				}
				resp, err := nc.RequestMsgWithContext(ctx, &nats.Msg{
					Subject: msg.Reply,
					Header: nats.Header{
						"nvcf-seq": []string{strconv.Itoa(seq)},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				if string(resp.Data) == "NAK" {
					t.Fatal("NAK")
				}
			})
			if err != nil {
				return err
			}
			defer sub.Unsubscribe()
			err = handleWorkRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
			if err != nil {
				return err
			}
			return nil
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("old request (still within grace period)", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestPath:                "/echo",
			RequestTime:                timestamppb.New(time.Now().Add(-500 * time.Millisecond)),
		}
		workFunc := serial(workerConfig.FunctionVersionId, Scenario{
			Region:                      "region-1",
			WorkerInvokeFunctionRequest: invokeFunctionRequest,
		})
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("old request (expected to reject)", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestPath:                "/echo",
			RequestTime:                timestamppb.New(time.Now().Add(-time.Hour)),
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			return handleWorkRequestCustomHandler(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId, nil, func(inferenceResponse *http.Response) error {
				if inferenceResponse.StatusCode != 500 {
					return fmt.Errorf("unexpected inference response: %d", inferenceResponse.StatusCode)
				}
				body, err := io.ReadAll(inferenceResponse.Body)
				if err != nil {
					return err
				}
				if !strings.Contains(string(body), "Internal error while making inference request") {
					return fmt.Errorf("unexpected inference response: %s", string(body))
				}
				t.Log("happy failure")
				return nil
			})
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

	t.Run("ping pong", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:     uuid.New().String(),
			NcaId:         ncaId,
			Subject:       "test-invoker-123",
			RequestMethod: http.MethodPost,
			// not an event stream, we're just triggering the streaming behaviour for backwards compatible requests
			RequestHeaders: []*pb.StringKV{{Key: "Accept", Value: "text/event-stream"}},
			RequestPath:    "/ping-pong",
			RequestTime:    timestamppb.Now(),
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			const expectedPongs = 10
			pongReceived := make(chan struct{})
			pr, pw := io.Pipe()
			go func() {
				defer pw.Close()
				for range expectedPongs {
					_, _ = pw.Write([]byte("ping\n"))
					zap.L().Info("ping sent")
					<-pongReceived
				}
			}()
			return handleWorkRequestCustomHandler(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId, pr, func(r *http.Response) error {
				pongs := 0
				scanner := bufio.NewScanner(r.Body)
				for scanner.Scan() {
					line := scanner.Text()
					if line == "pong" {
						zap.L().Info("pong received")
						pongReceived <- struct{}{}
						pongs += 1
					} else {
						return fmt.Errorf("unexpected line: %s", line)
					}
				}
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("error reading response: %w", err)
				}
				if pongs != expectedPongs {
					return fmt.Errorf("expected 10 pongs, got %d", pongs)
				}
				return nil
			})
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})
}

func TestSuccessfulEndToEndNonBackwardsCompatible(t *testing.T) {
	zapLogger := setupLogs(t)
	inferenceServerHost := "127.0.0.1:8000"
	assetServerHost := "127.0.0.1:8001"
	ncaId := "test-nca-function-owner"

	// Start inference mock
	server, err := testutils.NewInferenceServer(inferenceServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// start asset mock
	asset, err := testutils.NewAssetServer(assetServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()

	// start nats server
	natsSuperCluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer natsSuperCluster.Shutdown()

	// Start otel mock
	mc, err := testutils.NewMockCollector(zapLogger)
	if err != nil {
		t.Fatal(err)
	}

	defer mc.Shutdown()

	workerConfig := worker.Config{
		NVCFWorkerToken:                  "fake-worker-token",
		NVCFFqdn:                         "http://127.0.0.1:9090",
		NVCFFqdnNATS:                     natsSuperCluster.Clusters[0].Servers[0].ClientURL(),
		InferencePort:                    8000,
		InferenceHealthEndpoint:          "/health",
		OTELExporterOTLPEndpoint:         "http://127.0.0.1:8360",
		TracingAccessToken:               "fake-tracing-token",
		NcaId:                            ncaId,
		BillingNcaId:                     ncaId,
		FunctionId:                       "10b076eb-b6d2-4cd9-878b-a3614a931570",
		FunctionVersionId:                "f85f1808-966c-4ac5-8e19-1c6defadb891",
		FunctionName:                     "test-echo-function",
		BaseAssetDir:                     t.TempDir(),
		BaseResponseDir:                  t.TempDir(),
		SharedConfigDir:                  t.TempDir(),
		HealthPort:                       9093,
		InstanceId:                       "test-instance-id",
		MaxRequestConcurrency:            20,
		V3BackwardsCompatibilityDisabled: true, // disable backwards compatibility
	}

	// let the inference container return 202s rather than utils generating them
	t.Run("polling", func(t *testing.T) {
		invokeFunctionRequest := &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			RequestBody:                []byte(`{"abc": 123}`),
			RequestMethod:              http.MethodPost,
			RequestHeaders:             []*pb.StringKV{{Key: "nvcf-poll-seconds", Value: "1"}},
			RequestPath:                "/echo-with-status/202",
		}
		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			invokeFunctionRequest.RequestTime = timestamppb.Now()
			err := handleWorkRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
			if err != nil {
				return err
			}
			t.Log("initial polling request returned")
			for i := 0; i < 10; i++ {
				t.Log("sending polling request", i)
				invokeFunctionRequest.RequestTime = timestamppb.Now()
				err = handlePollingRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
				if err != nil {
					return nil
				}
				t.Log("polling request", i, "returned")
			}
			invokeFunctionRequest.RequestTime = timestamppb.Now()
			invokeFunctionRequest.RequestPath = "/echo-with-status/201"
			return handlePollingRequest(ctx, nc, invokeFunctionRequest, "region-1", workerConfig.FunctionVersionId)
		}
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})
}

func runWorkerTest(t *testing.T, natsSuperCluster *testutils.SuperCluster, workFunc testutils.DoWorkFunc, zapLogger *logs.ZapLogger, workerConfig worker.Config) {
	t.Logf("starting test %s", t.Name())
	t.Cleanup(func() {
		t.Logf("finishing test %s", t.Name())
	})
	ctx, cancel := contextWithCancelAndTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Start nvcf api grpc mock
	workerServer, err := testutils.NewMockNVCFAPI(ctx, t, natsSuperCluster.Clusters[0].Servers[0].ClientURL(), workFunc, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer workerServer.Shutdown()
	// cancel when the work scenarios have finished
	go func() {
		defer cancel()
		if err := workerServer.Wait(ctx); err != nil {
			t.Error(err)
		}
	}()

	err = runTestWorker(ctx, zapLogger, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
}

func contextWithCancelAndTimeout(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancelTimeout := context.WithTimeout(ctx, duration)
	ctx, cancel := context.WithCancel(ctx)
	return ctx, func() {
		cancel()
		cancelTimeout()
	}
}

// runTestWorker skips running the nvkit framework as it makes assumptions about global state that don't let us run multiple workers within the same test binary
func runTestWorker(ctx context.Context, zapLogger *logs.ZapLogger, workerConfig worker.Config) error {
	nvcfWorker, err := worker.NewNVCFWorker(ctx, zapLogger, workerConfig)
	if err != nil {
		return err
	}
	err = nvcfWorker.SetupWorkDirs()
	if err != nil {
		return err
	}
	err = nvcfWorker.Run(false)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func defaultInferenceRequest(ncaId string, assetServerHost string) Scenario {
	return Scenario{
		Region: "region-1",
		WorkerInvokeFunctionRequest: &pb.WorkerInvokeFunctionRequest{
			RequestId:                  uuid.New().String(),
			NcaId:                      ncaId,
			Subject:                    "test-invoker-123",
			LargeResponseUrl:           "http://localhost:8002",
			MaxDirectResponseSizeBytes: 1024,
			InputAssetReference: []*pb.InputAssetReference{
				{
					AssetId:     "foo",
					Reference:   "http://" + assetServerHost + "/some/asset",
					ContentType: "text/plain",
				},
			},
			RequestBody:   []byte(`{"abc": 123}`),
			RequestMethod: http.MethodPost,
			RequestPath:   "/echo",
			RequestTime:   timestamppb.Now(),
		},
	}
}

type Scenario struct {
	Region string
	*pb.WorkerInvokeFunctionRequest
}

func regionalInferenceRequest(ncaId, region string) Scenario {
	return Scenario{Region: region, WorkerInvokeFunctionRequest: &pb.WorkerInvokeFunctionRequest{
		RequestId:                  uuid.New().String(),
		NcaId:                      ncaId,
		Subject:                    "test-invoker-123",
		LargeResponseUrl:           "http://localhost:8002",
		MaxDirectResponseSizeBytes: 1024,
		RequestBody:                []byte(`{"abc": 123}`),
		RequestMethod:              http.MethodPost,
		RequestPath:                "/echo",
		RequestTime:                timestamppb.Now(),
	}}
}

func setupLogs(t *testing.T) *logs.ZapLogger {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.RedirectStdLog(zapLogger.GetZapLogger())
	t.Cleanup(func() {
		_ = zapLogger.Close()
	})
	return zapLogger
}

func TestCluster(t *testing.T) {
	t.SkipNow() // only needed for debugging the local cluster setup
	setupLogs(t)
	cluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Shutdown()

	nc, err := nats.Connect(cluster.Clusters[0].Servers[0].ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	servers := nc.Servers()
	t.Logf("servers: %v", servers)
	if len(servers) != 1 {
		t.Fail()
	}
}

func serial(functionVersionId string, scenarios ...Scenario) testutils.DoWorkFunc {
	return func(ctx context.Context, nc *nats.Conn) error {
		for _, work := range scenarios {
			err := handleWorkRequest(ctx, nc, work.WorkerInvokeFunctionRequest, work.Region, functionVersionId)
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func concurrent(functionVersionId string, scenarios ...Scenario) testutils.DoWorkFunc {
	return func(ctx context.Context, nc *nats.Conn) error {
		group, ctx := errgroup.WithContext(ctx)
		for _, work := range scenarios {
			work := work
			group.Go(func() error {
				return handleWorkRequest(ctx, nc, work.WorkerInvokeFunctionRequest, work.Region, functionVersionId)
			})
		}
		return group.Wait()
	}
}

// performs as the invocation service being driven by a client to send a work request to nats and wait for the response
func handleWorkRequest(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string) error {
	return handleWorkRequestCustomHandler(ctx, nc, work, region, functionVersionId, nil, func(inferenceResponse *http.Response) error {
		if inferenceResponse.StatusCode >= 400 {
			return fmt.Errorf("unexpected inference response: %d", inferenceResponse.StatusCode)
		}
		return nil
	})
}

// performs as the invocation service being driven by a client to send a work request to nats and wait for the response
func handleWorkRequestCustomHandler(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string, customRequestBodyProducer io.Reader, customResponseConsumer func(*http.Response) error) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	defer js.CleanupPublisher()

	mux := http.NewServeMux()
	requestKey := uuid.New().String()
	responseKey := uuid.New().String()
	finished := make(chan error)
	var completedInferenceResponseStatus int

	// POST endpoint for worker responses
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+responseKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized")
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream+h1" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type")
			return
		}
		zap.L().Debug("received POST attach request, reading response")
		defer r.Body.Close()

		inferenceResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			zap.L().Error("error reading response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		defer inferenceResponse.Body.Close()
		if inferenceResponse.TransferEncoding != nil {
			zap.L().Error("unexpected transfer encoding", zap.Strings("transfer encoding", inferenceResponse.TransferEncoding))
			http.Error(w, "unexpected transfer encoding", http.StatusInternalServerError)
			finished <- fmt.Errorf("unexpected transfer encoding")
			return
		}
		dump, err := httputil.DumpResponse(inferenceResponse, false)
		if err != nil {
			zap.L().Error("error dumping response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		zap.L().Info("test attach response", zap.ByteString("inference response", dump))
		completedInferenceResponseStatus = inferenceResponse.StatusCode
		if customResponseConsumer != nil {
			err = customResponseConsumer(inferenceResponse)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				finished <- err
				return
			}
		}
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		finished <- nil
	})

	// GET endpoint for client request data
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+requestKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized for request data")
			return
		}
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type for request data")
			return
		}

		zap.L().Debug("received GET request for client data")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if customRequestBodyProducer != nil {
			fw := &flushWriter{
				Writer:  w,
				Flusher: w.(http.Flusher),
			}
			_, err := io.Copy(fw, customRequestBodyProducer)
			if err != nil {
				zap.L().Error("error sending request body", zap.Error(err))
				finished <- err
				return
			}
		}
		zap.L().Debug("sent client data via GET endpoint")
	})

	nvcfISServer := httptest.NewServer(mux)
	defer nvcfISServer.Close()

	work.StatelessConfig = &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  nvcfISServer.URL,
						ResponseAuthorizationToken: responseKey,
						RequestAuthorizationToken:  &requestKey,
					},
				},
			},
		},
	}

	requestSubject := fmt.Sprintf("rq.%s.%s.%s", region, functionVersionId, work.RequestId)
	zap.L().Info("pushing work onto request queue", zap.String("subject", requestSubject), zap.Any("work", work))
	data, err := proto.Marshal(work)
	if err != nil {
		return err
	}
	pubAck, err := js.Publish(ctx, requestSubject, data)
	if err != nil {
		zap.L().Error("failed to publish work request", zap.Error(err))
		return err
	}

	select {
	case err = <-finished:
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for response")
	}
	if err != nil {
		return err
	}

	stream, err := js.Stream(ctx, pubAck.Stream)
	if err != nil {
		zap.L().Error("failed to get stream", zap.Error(err))
		return err
	}
	// message is allowed to be acked async, so keep checking
	backoffConfig := backoff.NewExponentialBackOff()
	backoffConfig.InitialInterval = 10 * time.Millisecond
	backoffConfig.MaxInterval = 200 * time.Millisecond
	err = backoff.Retry(func() error {
		finishedMessage, err := stream.GetMsg(ctx, pubAck.Sequence)
		// we expect the worker to not ack this message
		if completedInferenceResponseStatus == http.StatusAccepted {
			if err != nil || finishedMessage == nil {
				return backoff.Permanent(fmt.Errorf("expected work not to be deleted, but it is missing"))
			}
			return nil
		}
		// check that the message was acknowledged
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				return nil
			}
			return err
		}
		if finishedMessage != nil {
			zap.L().Warn("work request was not yet deleted", zap.Any("work", work))
			return fmt.Errorf("expected work request to be deleted, but it was not: %v", finishedMessage)
		}
		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(backoffConfig, 5), ctx))
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		zap.L().Error("failed to check if work request was deleted", zap.Error(err))
		return err
	}
	return nil
}

type flushWriter struct {
	io.Writer
	http.Flusher
}

func (w *flushWriter) Write(p []byte) (n int, err error) {
	n, err = w.Writer.Write(p)
	w.Flusher.Flush()
	return
}

// performs as the invocation service being driven by a client to send a polling request to nats and wait for the response
func handlePollingRequest(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string) error {
	mux := http.NewServeMux()
	requestKey := uuid.New().String()
	responseKey := uuid.New().String()
	finished := make(chan error)

	// POST endpoint for worker responses
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+responseKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized")
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream+h1" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type")
			return
		}
		zap.L().Debug("received POST attach request, reading response")
		defer r.Body.Close()

		inferenceResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			zap.L().Error("error reading response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		defer inferenceResponse.Body.Close()
		if inferenceResponse.TransferEncoding != nil {
			zap.L().Error("unexpected transfer encoding", zap.Strings("transfer encoding", inferenceResponse.TransferEncoding))
			http.Error(w, "unexpected transfer encoding", http.StatusInternalServerError)
			finished <- fmt.Errorf("unexpected transfer encoding")
			return
		}
		dump, err := httputil.DumpResponse(inferenceResponse, true)
		if err != nil {
			zap.L().Error("error dumping response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}

		zap.L().Info("test attach response", zap.ByteString("inference response", dump))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)

		if inferenceResponse.StatusCode >= 400 {
			finished <- fmt.Errorf("unexpected inference response: %s", string(dump))
			return
		}
		finished <- nil
	})

	// GET endpoint for client request data
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+requestKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized for request data")
			return
		}
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type for request data")
			return
		}

		zap.L().Debug("received GET request for client data")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// we're not sending any extra data
		zap.L().Debug("sent client data via GET endpoint")
	})

	nvcfISServer := httptest.NewServer(mux)
	defer nvcfISServer.Close()

	work.StatelessConfig = &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  nvcfISServer.URL,
						ResponseAuthorizationToken: responseKey,
						RequestAuthorizationToken:  &requestKey,
					},
				},
			},
		},
	}

	subject := "rq_polling." + work.RequestId
	zap.L().Info("pushing polling request onto request queue", zap.String("subject", subject), zap.Any("work", work))
	data, _ := proto.Marshal(work)
	_, err := nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		zap.L().Error("failed to publish work request", zap.Error(err))
		return err
	}
	zap.L().Info("polling request acked by worker", zap.String("subject", subject))

	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for response")
	}
}

// all request data comes from the GET request body, not the NATS message
func handleGetRequestEncodedWorkRequest(ctx context.Context, nc *nats.Conn, work *pb.WorkerInvokeFunctionRequest, region, functionVersionId string, request *http.Request) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	defer js.CleanupPublisher()

	mux := http.NewServeMux()
	requestKey := uuid.New().String()
	responseKey := uuid.New().String()
	finished := make(chan error)

	// POST endpoint for worker responses
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+responseKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized")
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream+h1" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type")
			return
		}
		zap.L().Debug("received POST attach request, reading response")
		defer r.Body.Close()

		inferenceResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
		if err != nil {
			zap.L().Error("error reading response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}
		defer inferenceResponse.Body.Close()
		if inferenceResponse.TransferEncoding != nil {
			zap.L().Error("unexpected transfer encoding", zap.Strings("transfer encoding", inferenceResponse.TransferEncoding))
			http.Error(w, "unexpected transfer encoding", http.StatusInternalServerError)
			finished <- fmt.Errorf("unexpected transfer encoding")
			return
		}
		dump, err := httputil.DumpResponse(inferenceResponse, true)
		if err != nil {
			zap.L().Error("error dumping response", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			finished <- err
			return
		}

		zap.L().Info("test attach response", zap.ByteString("inference response", dump))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)

		if inferenceResponse.StatusCode >= 400 {
			finished <- fmt.Errorf("unexpected inference response: %s", string(dump))
			return
		}
		finished <- nil
	})

	// GET endpoint for client request data
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+requestKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			finished <- fmt.Errorf("unauthorized for request data")
			return
		}
		if r.Header.Get("Accept") != "application/octet-stream+h1" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			finished <- fmt.Errorf("unsupported media type for request data")
			return
		}

		zap.L().Debug("received GET request for client data")
		w.Header().Set("Content-Type", "application/octet-stream+h1")
		w.WriteHeader(http.StatusOK)
		request.ProtoMajor = 1
		request.ProtoMinor = 1
		request.TransferEncoding = []string{"identity"}
		err := request.Write(w)
		if err != nil {
			zap.L().Error("error sending request", zap.Error(err))
			finished <- err
			return
		}
		zap.L().Debug("sent client data via GET endpoint")
	})

	nvcfISServer := httptest.NewServer(mux)
	defer nvcfISServer.Close()

	work.StatelessConfig = &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{
			{
				Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
					Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
						TargetURI:                  nvcfISServer.URL,
						ResponseAuthorizationToken: responseKey,
						RequestAuthorizationToken:  &requestKey,
					},
				},
			},
		},
	}

	requestSubject := fmt.Sprintf("rq.%s.%s.%s", region, functionVersionId, work.RequestId)
	zap.L().Info("pushing work onto request queue", zap.String("subject", requestSubject), zap.Any("work", work))
	data, err := proto.Marshal(work)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, requestSubject, data)
	if err != nil {
		zap.L().Error("failed to publish work request", zap.Error(err))
		return err
	}
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for response")
	}
}

func TestWorkerGracefulShutdown(t *testing.T) {
	zapLogger := setupLogs(t)
	inferenceServerHost := "127.0.0.1:8000"
	assetServerHost := "127.0.0.1:8001"
	largeResponseServerHost := "127.0.0.1:8002"
	ncaId := "test-nca-function-owner"

	// Start inference mock
	server, err := testutils.NewInferenceServer(inferenceServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// start asset mock
	asset, err := testutils.NewAssetServer(assetServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()

	// start large response s3 mock
	largeResponse, err := testutils.NewS3Server(largeResponseServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer largeResponse.Close()

	// start nats server
	natsSuperCluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer natsSuperCluster.Shutdown()

	// Start otel mock
	mc, err := testutils.NewMockCollector(zapLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Shutdown()

	workerConfig := worker.Config{
		NVCFWorkerToken:          "fake-worker-token",
		NVCFFqdn:                 "http://127.0.0.1:9090",
		NVCFFqdnNATS:             natsSuperCluster.Clusters[0].Servers[0].ClientURL(),
		InferencePort:            8000,
		InferenceHealthEndpoint:  "/health",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		NcaId:                    ncaId,
		BillingNcaId:             ncaId,
		FunctionId:               "10b076eb-b6d2-4cd9-878b-a3614a931570",
		FunctionVersionId:        "f85f1808-966c-4ac5-8e19-1c6defadb891",
		FunctionName:             "test-echo-function",
		BaseAssetDir:             t.TempDir(),
		BaseResponseDir:          t.TempDir(),
		SharedConfigDir:          t.TempDir(),
		HealthPort:               9093,
		InstanceId:               "test-instance-id",
		MaxRequestConcurrency:    5,
	}

	nvcf.MetadataCredsTokenFile = filepath.Join(t.TempDir(), "self")

	t.Run("graceful shutdown with in-flight and rejected requests", func(t *testing.T) {
		t.Logf("starting graceful shutdown test")

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Track completed and rejected requests
		completedRequests := &atomic.Int32{}
		rejectedRequests := &atomic.Int32{}
		initialBatchSize := 5 // These should get processed
		secondBatchSize := 5  // These should be rejected after shutdown

		// Create a channel to signal when worker is running
		workerRunning := make(chan *worker.NVCFWorker, 1)

		// Create a channel to signal when first batch is done
		firstBatchDone := make(chan struct{})

		workFunc := func(ctx context.Context, nc *nats.Conn) error {
			// Wait for worker to be ready
			var nvcfWorker *worker.NVCFWorker
			select {
			case nvcfWorker = <-workerRunning:
				t.Log("Worker is running, proceeding with test")
			case <-time.After(10 * time.Second):
				return fmt.Errorf("timeout waiting for worker to start")
			}

			// Create first batch of requests
			firstBatch := make([]Scenario, initialBatchSize)
			for i := range firstBatch {
				firstBatch[i] = Scenario{
					Region: "region-1",
					WorkerInvokeFunctionRequest: &pb.WorkerInvokeFunctionRequest{
						RequestId:                  uuid.New().String(),
						NcaId:                      ncaId,
						Subject:                    "test-invoker-123",
						LargeResponseUrl:           "http://localhost:8002",
						MaxDirectResponseSizeBytes: 1024,
						RequestBody:                []byte(fmt.Sprintf(`{"delay": 1000, "id": %d}`, i)), // 1 second delay
						RequestMethod:              http.MethodPost,
						RequestPath:                "/echo-with-delay",
						RequestTime:                timestamppb.Now(),
					},
				}
			}

			// Submit first batch - all should be accepted
			var wg sync.WaitGroup
			wg.Add(initialBatchSize)

			for i, req := range firstBatch {
				i, req := i, req // capture loop variables
				go func() {
					defer wg.Done()

					err := handleWorkRequest(ctx, nc, req.WorkerInvokeFunctionRequest, req.Region, workerConfig.FunctionVersionId)
					if err != nil {
						t.Logf("Request batch 1, #%d failed: %v", i, err)
						return
					}

					completedRequests.Add(1)
					t.Logf("Request batch 1, #%d completed successfully", i)

					if i == initialBatchSize-2 {
						t.Log("Triggering worker shutdown")

						// Call the Shutdown method on the worker
						nvcfWorker.Shutdown()

						// Signal that first batch is done and shutdown triggered
						close(firstBatchDone)

						t.Log("Worker shutdown triggered")
					}
				}()
			}

			// Wait until first batch is done and shutdown is triggered
			select {
			case <-firstBatchDone:
				t.Log("First batch completed, shutdown initiated")
			case <-time.After(15 * time.Second):
				t.Error("Timeout waiting for first batch to complete")
				return fmt.Errorf("test failed: timeout waiting for first batch")
			}

			// Wait a moment to let shutdown take effect
			// time.Sleep(500 * time.Millisecond)

			// Submit second batch after shutdown - these should be rejected naturally
			secondBatch := make([]Scenario, secondBatchSize)
			for i := range secondBatch {
				secondBatch[i] = Scenario{
					Region: "region-1",
					WorkerInvokeFunctionRequest: &pb.WorkerInvokeFunctionRequest{
						RequestId:                  uuid.New().String(),
						NcaId:                      ncaId,
						Subject:                    "test-invoker-123",
						LargeResponseUrl:           "http://localhost:8002",
						MaxDirectResponseSizeBytes: 1024,
						RequestBody:                []byte(fmt.Sprintf(`{"delay": 100, "id": %d}`, i+initialBatchSize)),
						RequestMethod:              http.MethodPost,
						RequestPath:                "/echo-with-delay",
						RequestTime:                timestamppb.Now(),
					},
				}
			}

			// Create a channel to hold results from each request
			results := make(chan error, secondBatchSize)

			batchStartTime := time.Now()

			// Submit each request using the main test context but with a reasonable timeout
			for i, req := range secondBatch {
				i, req := i, req
				go func() {
					reqCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					err := handleWorkRequest(reqCtx, nc, req.WorkerInvokeFunctionRequest, req.Region, workerConfig.FunctionVersionId)

					if err != nil {
						if strings.Contains(err.Error(), "nats: connection closed") ||
							strings.Contains(err.Error(), "context deadline exceeded") {
							t.Logf("Request batch 2, #%d properly rejected: %v", i, err)
							rejectedRequests.Add(1)
						} else {
							t.Logf("Request batch 2, #%d rejected with unexpected error: %v", i, err)
							rejectedRequests.Add(1)
						}
					} else {
						// If we get here, request somehow succeeded despite shutdown
						completedRequests.Add(1)
						t.Logf("Request batch 2, #%d unexpectedly completed", i)
					}

					// Signal this request is done
					results <- err
				}()
			}

			completedSecondBatch := make(chan struct{})
			go func() {
				for i := 0; i < secondBatchSize; i++ {
					<-results
				}
				close(completedSecondBatch)
			}()

			select {
			case <-completedSecondBatch:
				batchDuration := time.Since(batchStartTime)
				t.Logf("Second batch completed in %v", batchDuration)
			case <-time.After(15 * time.Second):
				// This is still a valid test outcome if the connections are being cleaned up
				t.Logf("Some requests timed out, but this is expected when connection is closed")
				// Check if we got any rejections
				if rejectedRequests.Load() > 0 {
					t.Logf("Got %d rejections before timeout - this confirms shutdown behavior",
						rejectedRequests.Load())
				}
			}

			return nil
		}

		// Start NVCF API mock
		workerServer, err := testutils.NewMockNVCFAPI(ctx, t, natsSuperCluster.Clusters[0].Servers[0].ClientURL(), workFunc, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer workerServer.Shutdown()

		// Run the worker in a goroutine
		go func() {
			nvcfWorker, err := worker.NewNVCFWorker(ctx, zapLogger, workerConfig)
			if err != nil {
				t.Error(err)
				return
			}

			err = nvcfWorker.SetupWorkDirs()
			if err != nil {
				t.Error(err)
				return
			}

			// Send the worker instance through the channel
			workerRunning <- nvcfWorker

			// Run the worker
			err = nvcfWorker.Run(false)
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}()

		// Wait for worker server to complete (which happens when all requests are processed)
		if err := workerServer.Wait(ctx); err != nil {
			t.Error(err)
		}

		// Verify results
		completedCount := completedRequests.Load()
		rejectedCount := rejectedRequests.Load()

		t.Logf("Completed %d requests, rejected %d requests", completedCount, rejectedCount)

		// Expectations:
		// 1. All first batch requests should complete (initialBatchSize)
		// 2. All second batch requests should be rejected (secondBatchSize)
		if completedCount != int32(initialBatchSize) {
			t.Errorf("Expected %d completed requests, got %d", initialBatchSize, completedCount)
		}

		if rejectedCount != int32(secondBatchSize) {
			t.Errorf("Expected %d rejected requests, got %d", secondBatchSize, rejectedCount)
		}
	})
}

func TestLargeRequestConcurrency(t *testing.T) {
	zapLogger := setupLogs(t)
	inferenceServerHost := "127.0.0.1:8000"
	assetServerHost := "127.0.0.1:8001"
	largeResponseServerHost := "127.0.0.1:8002"
	ncaId := "test-nca-function-owner"

	// Start inference mock
	server, err := testutils.NewInferenceServer(inferenceServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// start asset mock
	asset, err := testutils.NewAssetServer(assetServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()

	// start large response s3 mock
	largeResponse, err := testutils.NewS3Server(largeResponseServerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer largeResponse.Close()

	// start nats server
	natsSuperCluster, err := testutils.NewNatsSuperCluster(t)
	if err != nil {
		t.Fatal(err)
	}
	defer natsSuperCluster.Shutdown()

	// Start otel mock
	mc, err := testutils.NewMockCollector(zapLogger)
	if err != nil {
		t.Fatal(err)
	}

	defer mc.Shutdown()

	workerConfig := worker.Config{
		NVCFWorkerToken:          "fake-worker-token",
		NVCFFqdn:                 "http://127.0.0.1:9090",
		NVCFFqdnNATS:             natsSuperCluster.Clusters[0].Servers[0].ClientURL(),
		InferencePort:            8000,
		InferenceHealthEndpoint:  "/health",
		OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
		TracingAccessToken:       "fake-tracing-token",
		NcaId:                    ncaId,
		BillingNcaId:             ncaId,
		FunctionId:               "10b076eb-b6d2-4cd9-878b-a3614a931570",
		FunctionVersionId:        "f85f1808-966c-4ac5-8e19-1c6defadb891",
		FunctionName:             "test-echo-function",
		BaseAssetDir:             t.TempDir(),
		BaseResponseDir:          t.TempDir(),
		SharedConfigDir:          t.TempDir(),
		HealthPort:               9093,
		InstanceId:               "test-instance-id",
		MaxRequestConcurrency:    20_000, // by default NATS will only let you pull 5k messages at a time
	}

	nvcf.MetadataCredsTokenFile = filepath.Join(t.TempDir(), "self")

	t.Run("single work request", func(t *testing.T) {
		invokeFunctionRequest := defaultInferenceRequest(ncaId, assetServerHost)
		workFunc := serial(workerConfig.FunctionVersionId, invokeFunctionRequest)
		runWorkerTest(t, natsSuperCluster, workFunc, zapLogger, workerConfig)
	})

}
