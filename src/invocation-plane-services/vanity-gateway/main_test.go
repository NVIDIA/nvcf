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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ai-api-gateway-service/gateway"
	gatewayConfig "ai-api-gateway-service/gateway_config"

	"github.com/go-logr/zapr"
	"github.com/goccy/go-json"
	"github.com/magiconair/properties/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"gopkg.in/yaml.v3"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"
)

const nvcfEndpointPort = ":1111"
const tempTestConfigName = "config-temp.yaml"
const testConfigName = "/config.yaml"

var shadowRequestCount atomic.Int32
var shadowRequestModel atomic.Value
var slowShadowStarted atomic.Bool
var slowShadowCanceled atomic.Bool
var slowShadowCompleted atomic.Bool
var slowShadowRelease atomic.Pointer[chan struct{}]
var slowShadowStartedSignal atomic.Pointer[chan struct{}]
var slowShadowCanceledSignal atomic.Pointer[chan struct{}]
var slowShadowCompletedSignal atomic.Pointer[chan struct{}]

type CustomLogger struct {
	routerSwapCount atomic.Uint64
	zapcore.Core
}

func (c *CustomLogger) Check(entry zapcore.Entry, checkedEntry *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	ce := c.Core.Check(entry, checkedEntry)
	if ce != nil && ce.Message == "Finished swapping the new ChiMux" {
		c.routerSwapCount.Add(1)
	}
	return ce
}

func waitForRouterSwap(t *testing.T, logger *CustomLogger, previousCount uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if logger.routerSwapCount.Load() > previousCount {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for router swap")
}

func moveConfigAndWaitForReload(t *testing.T, logger *CustomLogger, tempDir string) {
	t.Helper()
	previousCount := logger.routerSwapCount.Load()
	moveAtomicallyToConfigFile(t, tempDir)
	waitForRouterSwap(t, logger, previousCount)
}

func startGateway(customLogger *CustomLogger, zapLogger *logs.ZapLogger, testConfigPath string) error {
	c := gateway.Config{MappingPath: testConfigPath}
	c.NvcfApiEndpoint = "http://localhost" + nvcfEndpointPort
	w, err := gateway.NewNVCFGateway(zapLogger, c)
	if err != nil {
		return err
	}
	observedLogger := zap.New(customLogger)
	zap.ReplaceGlobals(observedLogger)
	zap.RedirectStdLog(observedLogger)
	ctrl.SetLogger(zapr.NewLogger(zapLogger.GetZapLogger()))

	err = w.Setup()
	if err != nil {
		return err
	}

	go func() {
		err := w.Run()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			zap.S().Panic(err)
		}
	}()
	return err
}

func startNvcfServer() *http.Server {
	mux := http.NewServeMux()
	nvcfServer := &http.Server{Addr: nvcfEndpointPort,
		Handler: h2c.NewHandler(mux, &http2.Server{
			MaxConcurrentStreams: math.MaxInt32,
		})}
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	// Route for the status endpoint
	mux.HandleFunc("/v2/nvcf/pexec/status/requestid", func(w http.ResponseWriter, r *http.Request) {
	})

	// Generic catch-all handler that checks headers for function IDs
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		functionID := r.Header.Get("function-id")
		functionVersionID := r.Header.Get("function-version-id")

		// Track shadow requests
		if r.Header.Get("NVCF-Shadow") == "true" {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var payload struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(body, &payload) == nil {
					shadowRequestModel.Store(payload.Model)
				}
			}
			// Increment count after storing the model so that tests polling
			// on shadowRequestCount > 0 can safely read shadowRequestModel.
			shadowRequestCount.Add(1)

			// Slow shadow handler for cancel-on-disconnect tests
			if functionID == "cancel-shadow-func" {
				release := *slowShadowRelease.Load()
				slowShadowStarted.Store(true)
				sendSlowShadowSignal(&slowShadowStartedSignal)
				select {
				case <-r.Context().Done():
					slowShadowCanceled.Store(true)
					sendSlowShadowSignal(&slowShadowCanceledSignal)
				case <-release:
					slowShadowCompleted.Store(true)
					sendSlowShadowSignal(&slowShadowCompletedSignal)
				}
				w.WriteHeader(http.StatusOK)
				return
			}

			w.WriteHeader(http.StatusOK)
			return
		}

		// Handle /v1/internal/sre/simple_int8/synthetic with functionVersionId
		if functionID == "7d199eeb-8af6-4588-ba60-3009773cd29d" && functionVersionID == "7d199eeb-8af6-4588-ba60-3009773cd29d" {
			_, _ = w.Write([]byte("success"))
			return
		}

		// Handle facebook/opt-125m with functionVersionId
		if functionID == "eb69df99-3fc9-4272-a1ce-1be218deef3a" && functionVersionID == "eb69df99-3fc9-4272-a1ce-1be218deef3a" {
			_, _ = w.Write([]byte("success"))
			return
		}

		// Handle microsoft/phi-2
		if functionID == "77c5acaf-b4a3-4794-b469-dce523cf4b51" {
			_, _ = w.Write([]byte("completion success"))
			return
		}

		// Handle thenlper/gte-base
		if functionID == "23b31377-fa3b-4658-bc4e-f99ad02e91ef" {
			_, _ = w.Write([]byte("embedding success"))
			return
		}

		// Handle deepseek-ai/deepseek-r1
		if functionID == "854db4e5-9be7-45a0-a730-183cadf87e50" {
			_, _ = w.Write([]byte("responses success"))
			return
		}

		// Handle qwen/qwen-image-gen
		if functionID == "aaaa1111-0000-0000-0000-000000000001" {
			_, _ = w.Write([]byte("image generation success"))
			return
		}

		// Handle qwen/qwen-image-edit-2511
		if functionID == "aaaa1111-0000-0000-0000-000000000002" {
			_, _ = w.Write([]byte("image edit success for " + r.URL.Path))
			return
		}

		// Handle qwen/qwen-image-var
		if functionID == "aaaa1111-0000-0000-0000-000000000003" {
			_, _ = w.Write([]byte("image variation success"))
			return
		}

		// Handle test/cancel-on-disconnect primary — streams with enough delay
		// that a client can disconnect mid-response, but completes within the
		// 3-second test client timeout for normal completion tests.
		if functionID == "cancel-primary-func" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			for range 10 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(50 * time.Millisecond):
					_, _ = w.Write([]byte("data: chunk\n\n"))
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	go func() {
		err := nvcfServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			zap.S().Panic(err)
		}
	}()
	return nvcfServer
}

func TestGatewayService(t *testing.T) {
	// set up gateway to watch a temporary test config file
	tempDir := t.TempDir()
	setUpTempConfigFile(t, tempDir)
	moveAtomicallyToConfigFile(t, tempDir)

	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	customLogger := &CustomLogger{Core: zapLogger.GetZapLogger().Core()}
	initialSwapCount := customLogger.routerSwapCount.Load()
	err := startGateway(customLogger, zapLogger, tempDir+testConfigName)
	if err != nil {
		t.Fatal(err)
	}
	// The reload helper treats the first file load as a new update.
	waitForRouterSwap(t, customLogger, initialSwapCount)

	nvcfServer := startNvcfServer()
	defer func(nvcfServer *http.Server, ctx context.Context) {
		err := nvcfServer.Shutdown(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}(nvcfServer, context.Background())
	t.Logf("Waiting for servers to start...")
	for {
		resp, err := http.Get("http://localhost:10081/health")
		if err != nil && !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatal(err)
		}
		if resp != nil && resp.StatusCode == http.StatusOK {
			break
		}
	}
	client := http.Client{
		Timeout: 3 * time.Second,
	}

	testVanityNonExistFunction(t, client)

	testVanityExistFunction(t, client)

	testVanityExistFunctionWithOverride(t, client)

	testVanityPollingFunction(t, client)

	testOpenAiCompletionsNonExistModel(t, client)

	testOpenAiCompletionsExistModel(t, client)

	testOpenAiEmbeddingsNonExistModel(t, client)

	testOpenAiEmbeddingsExistModel(t, client)

	testOpenAiResponsesNonExistModel(t, client)

	testOpenAiResponsesExistModel(t, client)

	testOpenAiChatCompletionsMissingModel(t, client)

	testOpenAiImageGenerationsNonExistModel(t, client)

	testOpenAiImageGenerationsExistModel(t, client)

	testOpenAiImageEditsNonExistModel(t, client)

	testOpenAiImageEditsMissingModel(t, client)

	testOpenAiImageEditsExistModel(t, client)

	testOpenAiImageEditsForwardsMultipartAndPreservesPath(t, client)

	testOpenAiImageVariationsNonExistModel(t, client)

	testOpenAiImageVariationsExistModel(t, client)

	testOpenAiListModels(t, client)

	testOpenAiGetModelsNonExistModel(t, client)

	testOpenAiGetModelsExistModel(t, client)

	testOpenAiPayloadTooLarge(t, client)

	testOpenAiChatCompletionsNonExistModel(t, client)

	testOpenAiChatCompletionsExistModel(t, client)

	testOpenAiCompletionsNonExistModel(t, client)

	testVanityAddingNewPath(t, client, customLogger, tempDir)

	testOpenAiAddingNewModel(t, client, customLogger, tempDir)

	// Traffic shadow tests
	testShadowTrafficSent(t, client)
	testShadowNotCancelledOnNormalCompletion(t, client)
	testShadowCancelledOnClientDisconnect(t, client)

	// CORS tests
	testCorsPreflightRequest(t, client)
	testCorsActualRequest(t, client)

	// EOL/Deprecation header tests - using pre-configured test models/paths in config.yaml
	testOpenAiModelWithoutEOL(t, client)
	testOpenAiModelAddingEOL(t, client, customLogger, tempDir)
	testOpenAiModelExpired410(t, client, customLogger, tempDir)
	testOpenAiExpiredModelNotInList(t, client, customLogger, tempDir)
	testOpenAiGetExpiredModel410(t, client, customLogger, tempDir)

	// Vanity URL EOL tests - using pre-configured test paths in config.yaml
	testVanityUrlNoDeprecationHeader(t, client)
	testVanityUrlWithFutureEOL(t, client, customLogger, tempDir)
	testVanityUrlWithExpiredEOL(t, client, customLogger, tempDir)

	// Offline message tests
	testOfflineMessageReturns418(t, client, customLogger, tempDir)
	testOfflineModelStillInModelsList(t, client)
	testOfflineMessageClearedResumesService(t, client, customLogger, tempDir)
	testVanityOfflineMessageReturns418(t, client, customLogger, tempDir)
	testVanityOfflineMessageClearedResumesService(t, client, customLogger, tempDir)

	testDeletingAllConfig(t, client, customLogger, tempDir)
}

func setUpTempConfigFile(t *testing.T, tempDir string) string {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	destination := stagedConfigPath(t, tempDir)
	err = os.WriteFile(destination, data, 0644)
	if err != nil {
		t.Fatal(err)
	}
	// ensure the config file has been copied to temp testing dir
	_, err = os.Stat(destination)
	assert.Equal(t, err, nil)
	return destination
}

func moveAtomicallyToConfigFile(t *testing.T, tempDir string) {
	tempConfigPath := stagedConfigPath(t, tempDir)
	testConfigPath := tempDir + testConfigName
	err := os.Rename(tempConfigPath, testConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	// ensure the temp config file has been moved to testConfigPath
	_, err = os.Stat(testConfigPath)
	assert.Equal(t, err, nil)
}

func stagedConfigPath(t *testing.T, tempDir string) string {
	t.Helper()
	stagingDir := filepath.Join(filepath.Dir(tempDir), "config-stage")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(stagingDir, tempTestConfigName)
}

func testVanityNonExistFunction(t *testing.T, client http.Client) {
	t.Run("Testing Vanity non-existing function", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/nonexist", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testVanityExistFunction(t *testing.T, client http.Client) {
	t.Run("Testing Vanity existing function", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testVanityExistFunctionWithOverride(t *testing.T, client http.Client) {
	t.Run("Testing Vanity existing function with override", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic_override", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testVanityPollingFunction(t *testing.T, client http.Client) {
	t.Run("Testing Vanity polling", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/status/requestid", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
	})
}

func testOpenAiChatCompletionsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve chat completions for non-existing model", func(t *testing.T) {
		body := `{"model":"nonexist"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiChatCompletionsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve chat completions for existing model", func(t *testing.T) {
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testOpenAiResponsesNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve responses for non-existing model", func(t *testing.T) {
		body := `{"model":"nonexist"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/responses", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiResponsesExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve responses for existing model", func(t *testing.T) {
		body := `{"model":"deepseek-ai/deepseek-r1"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/responses", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "responses success")
	})
}

func testOpenAiCompletionsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve completions for non-existing model", func(t *testing.T) {
		body := `{"model":"nonexist"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiCompletionsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve completions for existing model", func(t *testing.T) {
		body := `{"model":"microsoft/phi-2"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "completion success")
	})
}

func testOpenAiEmbeddingsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve embeddings for non-existing model", func(t *testing.T) {
		body := `{"model":"nonexist"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/embeddings", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiEmbeddingsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve embeddings for existing model", func(t *testing.T) {
		body := `{"model":"thenlper/gte-base"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/embeddings", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "embedding success")
	})
}

func buildImageEditsMultipart(t *testing.T, model string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField("model", model); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("prompt", "make sky pink"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("image", "input.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
}

func testOpenAiImageGenerationsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image generations for non-existing model", func(t *testing.T) {
		body := `{"model":"nonexist","prompt":"a cat"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/generations", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiImageGenerationsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image generations for existing model", func(t *testing.T) {
		body := `{"model":"qwen/qwen-image-gen","prompt":"a cat"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/generations", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "image generation success")
	})
}

func testOpenAiImageEditsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image edits for non-existing model", func(t *testing.T) {
		body, ct := buildImageEditsMultipart(t, "nonexist")
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/edits", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", ct)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiChatCompletionsMissingModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 chat completions without model returns 400", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusBadRequest)
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "model field is required") {
			t.Fatalf("expected body to mention missing model field, got %q", string(body))
		}
	})
}

func testOpenAiImageEditsMissingModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 image edits without model returns 400", func(t *testing.T) {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		if err := mw.WriteField("prompt", "make sky pink"); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}

		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/edits", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusBadRequest)
		respBody, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(respBody), "model field is required") {
			t.Fatalf("expected body to mention missing model field, got %q", string(respBody))
		}
	})
}

func testOpenAiImageEditsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image edits for existing model", func(t *testing.T) {
		body, ct := buildImageEditsMultipart(t, "qwen/qwen-image-edit-2511")
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/edits", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", ct)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "image edit success for /v1/images/edits")
	})
}

// testOpenAiImageEditsForwardsMultipartAndPreservesPath verifies two things end-to-end:
//  1. outgoingPathOverride (/v1/images/edits in config.yaml) is honored on the rewritten request.
//  2. The multipart body is forwarded successfully to the backend and still parses
//     as a well-formed multipart payload with the expected model and image parts.
func testOpenAiImageEditsForwardsMultipartAndPreservesPath(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 image edits forwards multipart body and honors outgoingPathOverride", func(t *testing.T) {
		body, ct := buildImageEditsMultipart(t, "qwen/qwen-image-edit-2511")
		originalBytes := bytes.Clone(body.Bytes())

		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/edits", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", ct)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		// Confirms the backend saw the override path from config.yaml and that
		// the request reached the image-edit function (any other function ID
		// would return a different body).
		assert.Equal(t, string(result), "image edit success for /v1/images/edits")

		// Parse the original multipart body to confirm model + file parts are intact.
		_, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatal(err)
		}
		mr := multipart.NewReader(bytes.NewReader(originalBytes), params["boundary"])
		foundModel := false
		foundImage := false
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			switch part.FormName() {
			case "model":
				value, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				assert.Equal(t, string(value), "qwen/qwen-image-edit-2511")
				foundModel = true
			case "image":
				foundImage = true
			}
			_ = part.Close()
		}
		if !foundModel || !foundImage {
			t.Fatalf("expected model and image parts in multipart body, got model=%v image=%v", foundModel, foundImage)
		}
	})
}

func testOpenAiImageVariationsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image variations for non-existing model", func(t *testing.T) {
		body, ct := buildImageEditsMultipart(t, "nonexist")
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/variations", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", ct)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiImageVariationsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 serve image variations for existing model", func(t *testing.T) {
		body, ct := buildImageEditsMultipart(t, "qwen/qwen-image-var")
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/images/variations", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		req.Header.Set("Content-Type", ct)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "image variation success")
	})
}

func testOpenAiListModels(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 ListModels", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		var m gateway.ModelListResponse
		err = json.NewDecoder(res.Body).Decode(&m)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, m.Object, "list")

		// Verify we have 9 models (test/model-with-past-eol should be filtered out)
		assert.Equal(t, len(m.Data), 9)

		// Create a map to check for expected models
		modelIds := make(map[string]bool)
		for _, model := range m.Data {
			modelIds[model.Id] = true
			assert.Equal(t, model.Object, "model")
		}

		// Verify all expected models are present
		expectedModels := []string{
			"deepseek-ai/deepseek-r1",
			"facebook/opt-125m",
			"test/model-with-future-eol",
			"microsoft/phi-2",
			"microsoft/phi-2-override",
			"thenlper/gte-base",
			"qwen/qwen-image-gen",
			"qwen/qwen-image-edit-2511",
			"qwen/qwen-image-var",
		}
		for _, expectedModel := range expectedModels {
			assert.Equal(t, modelIds[expectedModel], true)
		}

		// Verify expired model is NOT present
		if modelIds["test/model-with-past-eol"] {
			t.Fatal("Expired model 'test/model-with-past-eol' should not be in list")
		}
	})
}

func testDeletingAllConfig(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing deleting all config", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}

		tempTestConfigPath := setUpTempConfigFile(t, tempDir)

		// delete all mappings
		t.Logf("Deleting all config...")
		config := gatewayConfig.GatewayConfig{}
		data, err := yaml.Marshal(&config)
		if err != nil {
			t.Fatal("Error when marshaling to YAML:", err)
		}
		err = os.WriteFile(tempTestConfigPath, data, 0644)
		if err != nil {
			t.Fatal("Error writing file:", err)
		}
		// atomically move temp config file into final config file
		// wait till the reloadable config finishes loading the new changed config before moving on
		moveConfigAndWaitForReload(t, logger, tempDir)

		// Verify 1: OpenAI list models returns 404 (routes not registered when config is empty)
		t.Logf("Testing OpenAIV2 ListModels returns 404 after delete...")
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)

		// Verify 2: OpenAI chat completions returns 404
		t.Logf("Testing chat completions returns 404 after delete...")
		body := `{"model":"facebook/opt-125m"}`
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)

		// Verify 3: Vanity path returns 404
		t.Logf("Testing Vanity path returns 404 after delete...")
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiGetModelsNonExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 GetModels non-existing model", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/nonexist", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusNotFound)
	})
}

func testOpenAiGetModelsExistModel(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 GetModels existing model", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/facebook/opt-125m", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		var i gateway.ModelInfo
		err = json.NewDecoder(res.Body).Decode(&i)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, i.Id, "facebook/opt-125m")
		assert.Equal(t, i.Object, "model")
		assert.Equal(t, i.OwnedBy, "facebook")

		req, err = http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/microsoft/phi-2", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		err = json.NewDecoder(res.Body).Decode(&i)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, i.Id, "microsoft/phi-2")
		assert.Equal(t, i.Object, "model")
		assert.Equal(t, i.OwnedBy, "microsoft")

		req, err = http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/microsoft/phi-2-override", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		err = json.NewDecoder(res.Body).Decode(&i)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, i.Id, "microsoft/phi-2-override")
		assert.Equal(t, i.Object, "model")
		assert.Equal(t, i.OwnedBy, "microsoft")

		req, err = http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/thenlper/gte-base", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		err = json.NewDecoder(res.Body).Decode(&i)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, i.Id, "thenlper/gte-base")
		assert.Equal(t, i.Object, "model")
		assert.Equal(t, i.OwnedBy, "thenlper")

		req, err = http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/deepseek-ai/deepseek-r1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		err = json.NewDecoder(res.Body).Decode(&i)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, i.Id, "deepseek-ai/deepseek-r1")
		assert.Equal(t, i.Object, "model")
		assert.Equal(t, i.OwnedBy, "deepseek-ai")
	})
}

func testOpenAiPayloadTooLarge(t *testing.T, client http.Client) {
	t.Run("Testing OpenAIV2 too large of a payload", func(t *testing.T) {
		body := strings.Repeat("a", 30*1024*1024) // 30MB > 25MB
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusBadRequest)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Matches(t, string(result), `{"error":{"message":"Please make sure your payload is below 26214400 bytes in size. If larger assets are required please refer to our Assets API.","type":"invalid_request_error","param":null,"code":"invalid_image_format"}}`)
	})
}

func testVanityAddingNewPath(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing adding a new route path to Vanity", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		tempTestConfigPath := setUpTempConfigFile(t, tempDir)

		// test it does not exist first
		testVanityNonExistFunction(t, client)

		// add a new path to an empty mapping file
		t.Logf("Adding a new path...")
		config := gatewayConfig.GatewayConfig{}

		path := make(map[string]gatewayConfig.PathFunctionDetails)
		path["testAddName"] = gatewayConfig.PathFunctionDetails{Path: "/nonexist", FunctionID: "7d199eeb-8af6-4588-ba60-3009773cd29d", FunctionVersionID: "7d199eeb-8af6-4588-ba60-3009773cd29d"}
		newEntry := gatewayConfig.VanityEntry{
			Host:  "test.add.host",
			Paths: path,
		}

		config.Vanity = make(map[string]gatewayConfig.VanityEntry)
		config.Vanity["testAddHostName"] = newEntry
		data, err := yaml.Marshal(&config)
		if err != nil {
			t.Fatal("Error when marshaling to YAML:", err)
		}
		err = os.WriteFile(tempTestConfigPath, data, 0644)
		if err != nil {
			t.Fatal("Error writing file:", err)
		}
		// atomically move temp config file into final config file
		// wait till the reloadable config finishes loading the new changed config before moving on
		moveConfigAndWaitForReload(t, logger, tempDir)

		// test make sure the newly added function is serving correctly
		t.Logf("Testing newly added Vanity function...")
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/nonexist", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "test.add.host"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testOpenAiAddingNewModel(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing adding a new model to openAI", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		tempTestConfigPath := setUpTempConfigFile(t, tempDir)

		// make sure the model does not exist first
		testOpenAiChatCompletionsNonExistModel(t, client)

		// add a new model to an empty mapping file
		t.Logf("Adding a new model...")
		config := gatewayConfig.GatewayConfig{}

		newPath := gatewayConfig.ModelFunctionDetails{ModelName: "nonexist", FunctionID: "eb69df99-3fc9-4272-a1ce-1be218deef3a", FunctionVersionID: "eb69df99-3fc9-4272-a1ce-1be218deef3a"}
		config.OpenAI.ChatCompletions = make(map[string]gatewayConfig.ModelFunctionDetails)
		config.OpenAI.ChatCompletions["testAdd"] = newPath
		config.OpenAI.Host = "stg.integrate.api.nvidia.com"
		data, err := yaml.Marshal(&config)
		if err != nil {
			t.Fatal("Error when marshaling to YAML:", err)
		}
		err = os.WriteFile(tempTestConfigPath, data, 0644)
		if err != nil {
			t.Fatal("Error writing file:", err)
		}
		// atomically move temp config file into final config file
		// wait till the reloadable config finishes loading the new changed config before moving on
		moveConfigAndWaitForReload(t, logger, tempDir)

		// test the new model is added successfully
		t.Logf("Testing newly added openAI model...")
		body := `{"model":"nonexist"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")

		// Restore full config so subsequent tests have all models/paths
		t.Logf("Restoring full config...")
		tempTestConfigPath = setUpTempConfigFile(t, tempDir)
		moveConfigAndWaitForReload(t, logger, tempDir)
	})
}

func testCorsPreflightRequest(t *testing.T, client http.Client) {
	t.Run("Testing CORS preflight request", func(t *testing.T) {
		// Create a preflight OPTIONS request
		req, err := http.NewRequest(http.MethodOptions, "http://localhost:10081/v1/chat/completions", nil)
		if err != nil {
			t.Fatal(err)
		}

		// Set required CORS headers
		req.Header.Set("Origin", "vscode-file://vscode-app")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Content-Type,Authorization")
		req.Host = "stg.integrate.api.nvidia.com"

		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Check response status code
		assert.Equal(t, res.StatusCode, http.StatusOK)

		// Check CORS headers
		assert.Equal(t, res.Header.Get("Access-Control-Allow-Origin"), "vscode-file://vscode-app")
		assert.Equal(t, res.Header.Get("Access-Control-Allow-Methods"), "POST")

		// Ensure headers allow Authorization and Content-Type
		allowHeaders := res.Header.Get("Access-Control-Allow-Headers")
		assert.Matches(t, allowHeaders, ".*Content-Type.*")
		assert.Matches(t, allowHeaders, ".*Authorization.*")

		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	})
}

func testCorsActualRequest(t *testing.T, client http.Client) {
	t.Run("Testing CORS in actual request", func(t *testing.T) {
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}

		// Set Origin header to simulate cross-origin request
		req.Header.Set("Origin", "vscode-file://vscode-app")
		req.Host = "stg.integrate.api.nvidia.com"

		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Check the response has CORS headers
		assert.Equal(t, res.Header.Get("Access-Control-Allow-Origin"), "vscode-file://vscode-app")
		assert.Equal(t, res.Header.Get("Access-Control-Allow-Credentials"), "true")

		// Check response content
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testOpenAiModelWithoutEOL(t *testing.T, client http.Client) {
	t.Run("Testing OpenAI model without EOL has no Deprecation header", func(t *testing.T) {
		// Test with chat completions endpoint
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify no Deprecation header
		deprecationHeader := res.Header.Get("Deprecation")
		assert.Equal(t, deprecationHeader, "")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")
	})
}

func testOpenAiModelAddingEOL(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing OpenAI model with future EOL has Deprecation header", func(t *testing.T) {
		// Test with the pre-configured model that has a future EOL
		body := `{"model":"test/model-with-future-eol"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify Deprecation header exists and has correct value
		deprecationHeader := res.Header.Get("Deprecation")
		assert.Equal(t, deprecationHeader, "2099-12-31T23:59:59Z")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified Deprecation header: %s", deprecationHeader)
	})
}

func testOpenAiModelRemovingEOL(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing OpenAI model after removing EOL has no Deprecation header", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}

		tempTestConfigPath := stagedConfigPath(t, tempDir)
		testConfigPath := tempDir + testConfigName

		// Read the current config (which should have EOL from previous test)
		t.Logf("Reading current config...")
		data, err := os.ReadFile(testConfigPath)
		if err != nil {
			t.Fatal(err)
		}

		var config gatewayConfig.GatewayConfig
		err = yaml.Unmarshal(data, &config)
		if err != nil {
			t.Fatal("Error when unmarshaling YAML:", err)
		}

		// Remove EOL from facebook/opt-125m model
		t.Logf("Removing EOL from facebook/opt-125m model...")
		for key, model := range config.OpenAI.ChatCompletions {
			if model.ModelName == "facebook/opt-125m" {
				model.EOL = time.Time{}
				config.OpenAI.ChatCompletions[key] = model
				break
			}
		}

		// Write the updated config
		updatedData, err := yaml.Marshal(&config)
		if err != nil {
			t.Fatal("Error when marshaling to YAML:", err)
		}
		err = os.WriteFile(tempTestConfigPath, updatedData, 0644)
		if err != nil {
			t.Fatal("Error writing file:", err)
		}

		// Atomically move temp config file into final config file
		moveConfigAndWaitForReload(t, logger, tempDir)

		// Test the model without EOL
		t.Logf("Testing model without EOL...")
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify no Deprecation header
		deprecationHeader := res.Header.Get("Deprecation")
		assert.Equal(t, deprecationHeader, "")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified no Deprecation header after removal")
	})
}

func testOpenAiModelExpired410(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing OpenAI model with past EOL returns 410 Gone", func(t *testing.T) {
		// Test with the pre-configured model that has a past EOL
		body := `{"model":"test/model-with-past-eol"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify 410 Gone status
		assert.Equal(t, res.StatusCode, http.StatusGone)

		// Verify error message
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Parse the error response (now using Problem Details format)
		var problemDetails gateway.ProblemDetails
		err = json.Unmarshal(result, &problemDetails)
		if err != nil {
			t.Fatal(err)
		}

		// Verify the problem details structure
		assert.Equal(t, problemDetails.Status, http.StatusGone)
		assert.Equal(t, problemDetails.Title, "Gone")
		assert.Matches(t, problemDetails.Detail, ".*end of life.*")
		assert.Matches(t, problemDetails.Detail, ".*2020-01-01.*")

		t.Logf("Successfully verified 410 Gone for expired model")
	})
}

func testOpenAiExpiredModelNotInList(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing expired model is not in /v1/models list", func(t *testing.T) {
		// Get the models list
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, res.StatusCode, http.StatusOK)

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Parse the models list
		var modelsResponse gateway.ModelListResponse
		err = json.Unmarshal(result, &modelsResponse)
		if err != nil {
			t.Fatal(err)
		}

		// Check that test/model-with-past-eol is NOT in the list (filtered out because expired)
		foundExpiredModel := false
		for _, model := range modelsResponse.Data {
			if model.Id == "test/model-with-past-eol" {
				foundExpiredModel = true
				break
			}
		}

		assert.Equal(t, foundExpiredModel, false)
		t.Logf("Successfully verified expired model not in list")
	})
}

func testOpenAiGetExpiredModel410(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing GET /v1/models/{model} for expired model returns 410", func(t *testing.T) {
		// Get the specific expired model
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/test/model-with-past-eol", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify 410 Gone status
		assert.Equal(t, res.StatusCode, http.StatusGone)

		// Verify error message
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Parse the error response (now using Problem Details format)
		var problemDetails gateway.ProblemDetails
		err = json.Unmarshal(result, &problemDetails)
		if err != nil {
			t.Fatal(err)
		}

		// Verify the problem details structure
		assert.Equal(t, problemDetails.Status, http.StatusGone)
		assert.Equal(t, problemDetails.Title, "Gone")
		assert.Matches(t, problemDetails.Detail, ".*end of life.*")
		assert.Matches(t, problemDetails.Detail, ".*2020-01-01.*")

		t.Logf("Successfully verified 410 Gone for GET expired model")
	})
}

func testVanityUrlNoDeprecationHeader(t *testing.T, client http.Client) {
	t.Run("Testing Vanity URLs do not have Deprecation header", func(t *testing.T) {
		// Test existing vanity function to ensure no Deprecation header
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, res.StatusCode, http.StatusOK)

		// Verify no Deprecation header (vanity URLs don't support EOL)
		deprecationHeader := res.Header.Get("Deprecation")
		assert.Equal(t, deprecationHeader, "")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified vanity URL has no Deprecation header")
	})
}

func testVanityUrlWithFutureEOL(t *testing.T, client http.Client, customLogger *CustomLogger, tempDir string) {
	t.Run("Testing Vanity URL with future EOL adds Deprecation header", func(t *testing.T) {
		// Test the pre-configured vanity path with future EOL
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/test/with-future-eol", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, res.StatusCode, http.StatusOK)

		// Verify Deprecation header is present and matches the EOL date
		deprecationHeader := res.Header.Get("Deprecation")
		assert.Equal(t, deprecationHeader, "2099-12-31T23:59:59Z")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified vanity URL with future EOL has Deprecation header: %s", deprecationHeader)
	})
}

func testVanityUrlWithExpiredEOL(t *testing.T, client http.Client, customLogger *CustomLogger, tempDir string) {
	t.Run("Testing Vanity URL with expired EOL returns 410 Gone", func(t *testing.T) {
		// Test the pre-configured vanity path with past EOL
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/test/with-past-eol", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Verify 410 Gone status
		assert.Equal(t, res.StatusCode, http.StatusGone)

		// Verify error message
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		err = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		// The vanity director now returns Problem Details JSON for 410
		var problemDetails gateway.ProblemDetails
		err = json.Unmarshal(result, &problemDetails)
		if err != nil {
			t.Fatal(err)
		}

		// Verify the problem details structure
		assert.Equal(t, problemDetails.Status, http.StatusGone)
		assert.Equal(t, problemDetails.Title, "Gone")
		assert.Matches(t, problemDetails.Detail, ".*end of life.*")
		assert.Matches(t, problemDetails.Detail, ".*2020-01-01.*")

		t.Logf("Successfully verified 410 Gone for expired vanity URL")
	})
}

func testOfflineMessageReturns418(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing per-model offline message returns 503 only for that model", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		tempTestConfigPath := setUpTempConfigFile(t, tempDir)

		data, err := os.ReadFile(tempTestConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		var rawConfig map[string]interface{}
		err = yaml.Unmarshal(data, &rawConfig)
		if err != nil {
			t.Fatal(err)
		}

		// Set offline message on a single chat completions model
		if v2, ok := rawConfig["v2config"].(map[string]interface{}); ok {
			if openai, ok := v2["openai"].(map[string]interface{}); ok {
				if cc, ok := openai["chatCompletions"].(map[string]interface{}); ok {
					if model, ok := cc["facebook/opt-125m"].(map[string]interface{}); ok {
						model["offlineMessage"] = "Model is down for maintenance"
					}
				}
			}
		}
		updatedData, err := yaml.Marshal(rawConfig)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(tempTestConfigPath, updatedData, 0644)
		if err != nil {
			t.Fatal(err)
		}
		moveConfigAndWaitForReload(t, logger, tempDir)

		// The offline model should return 503
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusServiceUnavailable)
		assert.Equal(t, res.Header.Get("Retry-After"), "10800")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		var problemDetails gateway.ProblemDetails
		err = json.Unmarshal(result, &problemDetails)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, problemDetails.Status, http.StatusServiceUnavailable)
		assert.Equal(t, problemDetails.Title, "Service Unavailable")
		assert.Equal(t, problemDetails.Detail, "Model is down for maintenance")

		// GET /v1/models/{model} for offline model should also return 503
		req, err = http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models/facebook/opt-125m", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusServiceUnavailable)
		assert.Equal(t, res.Header.Get("Retry-After"), "10800")
		_ = res.Body.Close()

		// Other models should still work (completions endpoint)
		body = `{"model":"microsoft/phi-2"}`
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err = io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "completion success")

		// Vanity endpoint should still work (no offline message set on it)
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err = io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified 503 only for the offline model, others still serve")
	})
}

func testOfflineMessageClearedResumesService(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing clearing offline message resumes normal service for that model", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		// Restore the original config (no offline messages)
		tempTestConfigPath := setUpTempConfigFile(t, tempDir)
		_ = tempTestConfigPath
		moveConfigAndWaitForReload(t, logger, tempDir)

		// The previously-offline model should work again
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified model resumed after clearing offline message")
	})
}

func testOfflineModelStillInModelsList(t *testing.T, client http.Client) {
	t.Run("Testing offline model still appears in /v1/models list", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		// The previous test set facebook/opt-125m offline.
		// Offline models are temporarily down, not removed, so they should still appear in the list.
		req, err := http.NewRequest(http.MethodGet, "http://localhost:10081/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)

		var m gateway.ModelListResponse
		err = json.NewDecoder(res.Body).Decode(&m)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		foundOfflineModel := false
		for _, model := range m.Data {
			if model.Id == "facebook/opt-125m" {
				foundOfflineModel = true
				break
			}
		}
		if !foundOfflineModel {
			t.Fatal("Offline model 'facebook/opt-125m' should still appear in /v1/models list")
		}

		t.Logf("Successfully verified offline model still in models list")
	})
}

func testVanityOfflineMessageReturns418(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing vanity path offline message returns 503", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		tempTestConfigPath := setUpTempConfigFile(t, tempDir)

		data, err := os.ReadFile(tempTestConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		var rawConfig map[string]interface{}
		err = yaml.Unmarshal(data, &rawConfig)
		if err != nil {
			t.Fatal(err)
		}

		// Set offline message on a single vanity path
		if v2, ok := rawConfig["v2config"].(map[string]interface{}); ok {
			if vanity, ok := v2["vanity"].(map[string]interface{}); ok {
				if stgAi, ok := vanity["stg_ai"].(map[string]interface{}); ok {
					if paths, ok := stgAi["paths"].(map[string]interface{}); ok {
						if path, ok := paths["sre_synthetic"].(map[string]interface{}); ok {
							path["offlineMessage"] = "Vanity endpoint is under maintenance"
						}
					}
				}
			}
		}
		updatedData, err := yaml.Marshal(rawConfig)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(tempTestConfigPath, updatedData, 0644)
		if err != nil {
			t.Fatal(err)
		}
		moveConfigAndWaitForReload(t, logger, tempDir)

		// The offline vanity path should return 503
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusServiceUnavailable)
		assert.Equal(t, res.Header.Get("Retry-After"), "10800")

		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		var problemDetails gateway.ProblemDetails
		err = json.Unmarshal(result, &problemDetails)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, problemDetails.Status, http.StatusServiceUnavailable)
		assert.Equal(t, problemDetails.Title, "Service Unavailable")
		assert.Equal(t, problemDetails.Detail, "Vanity endpoint is under maintenance")

		// Other vanity path on the same host should still work
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic_override", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err = io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		// OpenAI endpoints should still work
		body := `{"model":"facebook/opt-125m"}`
		req, err = http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err = io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified 503 only for the offline vanity path, others still serve")
	})
}

func testVanityOfflineMessageClearedResumesService(t *testing.T, client http.Client, logger *CustomLogger, tempDir string) {
	t.Run("Testing clearing vanity offline message resumes service", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("file updates are too slow on macOS (30 seconds) because we poll and don't get events")
		}
		// Restore the original config (no offline messages)
		_ = setUpTempConfigFile(t, tempDir)
		moveConfigAndWaitForReload(t, logger, tempDir)

		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/internal/sre/simple_int8/synthetic", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.ai.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		t.Logf("Successfully verified vanity endpoint resumed after clearing offline message")
	})
}

func testShadowTrafficSent(t *testing.T, client http.Client) {
	t.Run("Testing shadow traffic is sent for configured model", func(t *testing.T) {
		const expectedShadowModel = "private/facebook/opt-125m-shadow"

		// Reset counter
		shadowRequestCount.Store(0)
		shadowRequestModel.Store("")

		// Send a primary request to facebook/opt-125m which has shadow configured
		body := `{"model":"facebook/opt-125m"}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		// Primary response should still succeed
		assert.Equal(t, res.StatusCode, http.StatusOK)
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		assert.Equal(t, string(result), "success")

		// Wait for shadow request to arrive (async)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if shadowRequestModel.Load() == expectedShadowModel {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if shadowRequestCount.Load() == 0 {
			t.Error("expected shadow request to be sent, but none received")
		}
		assert.Equal(t, expectedShadowModel, shadowRequestModel.Load())
	})
}

func resetSlowShadowState() {
	slowShadowStarted.Store(false)
	slowShadowCanceled.Store(false)
	slowShadowCompleted.Store(false)
	ch := make(chan struct{})
	startedCh := make(chan struct{}, 1)
	canceledCh := make(chan struct{}, 1)
	completedCh := make(chan struct{}, 1)
	slowShadowRelease.Store(&ch)
	slowShadowStartedSignal.Store(&startedCh)
	slowShadowCanceledSignal.Store(&canceledCh)
	slowShadowCompletedSignal.Store(&completedCh)
}

func sendSlowShadowSignal(signal *atomic.Pointer[chan struct{}]) {
	ch := signal.Load()
	if ch == nil {
		return
	}
	select {
	case *ch <- struct{}{}:
	default:
	}
}

func waitForSlowShadowSignal(t *testing.T, signal *atomic.Pointer[chan struct{}], failureMessage string) {
	t.Helper()
	ch := signal.Load()
	if ch == nil {
		t.Fatal(failureMessage)
	}
	select {
	case <-*ch:
	case <-time.After(5 * time.Second):
		t.Fatal(failureMessage)
	}
}

func testShadowNotCancelledOnNormalCompletion(t *testing.T, client http.Client) {
	t.Run("Shadow keeps running when primary completes normally", func(t *testing.T) {
		resetSlowShadowState()

		body := `{"model":"private/test/cancel-on-disconnect","stream":true}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"

		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, res.StatusCode, http.StatusOK)
		// Read the entire response: normal completion must include the SSE terminator.
		responseBody, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(responseBody), "data: [DONE]\n\n") {
			t.Fatalf("expected primary stream to complete normally, got %q", string(responseBody))
		}

		waitForSlowShadowSignal(t, &slowShadowStartedSignal, "slow shadow request did not start")

		release := *slowShadowRelease.Load()
		close(release)

		// Shadow should not be cancelled just because the primary request completed.
		completedCh := slowShadowCompletedSignal.Load()
		canceledCh := slowShadowCanceledSignal.Load()
		select {
		case <-*completedCh:
		case <-*canceledCh:
			t.Error("shadow was cancelled after normal primary completion")
		case <-time.After(5 * time.Second):
			t.Error("shadow did not complete after normal primary completion")
		}
	})
}

func testShadowCancelledOnClientDisconnect(t *testing.T, _ http.Client) {
	t.Run("Shadow is cancelled when client disconnects mid-stream", func(t *testing.T) {
		resetSlowShadowState()

		body := `{"model":"private/test/cancel-on-disconnect","stream":true}`
		req, err := http.NewRequest(http.MethodPost, "http://localhost:10081/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "stg.integrate.api.nvidia.com"

		// Use a context we can cancel to simulate client disconnect mid-stream.
		// Cancel BEFORE reading the full response so the server sees the disconnect.
		ctx, cancel := context.WithCancel(context.Background())
		req = req.WithContext(ctx)

		// Use a client without a global timeout so the cancel is the only signal
		disconnectClient := http.Client{}
		res, err := disconnectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		// Read one chunk to confirm streaming started, then cancel context to
		// close the connection while the server is still writing.
		buf := make([]byte, 64)
		n, readErr := res.Body.Read(buf)
		t.Logf("read %d bytes, err: %v", n, readErr)
		cancel()
		_ = res.Body.Close()
		t.Log("client disconnected")

		waitForSlowShadowSignal(t, &slowShadowStartedSignal, "slow shadow request did not start")

		// Shadow should be cancelled because the client disconnected
		waitForSlowShadowSignal(t, &slowShadowCanceledSignal, "shadow was not cancelled after client disconnect")
	})
}
