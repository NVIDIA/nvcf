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

package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/cenkalti/backoff/v4"
	"github.com/hellofresh/health-go/v5"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	healthgrpc "github.com/hellofresh/health-go/v5/checks/grpc"

	nvcfMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const healthCheckTimeInterval = 10 * time.Second
const defaultInferenceHealthEndpoint = "/v2/health/ready"
const defaultInferenceHealthProtocol = "http"                         // Default protocol for health checks (http/grpc)
const defaultInferenceHealthTimeout = time.Duration(31) * time.Second // Default timeout for health check requests
const defaultInferenceHealthExpectedResponseCode = 200                // Default expected HTTP response code for successful health checks

type InferenceHealthStatus struct {
	isHealthy   bool                   // Current health state
	mu          sync.Mutex             // Mutex for protecting health and work state
	healthyCond *sync.Cond             // Condition variable for signaling healthy state
	CallBackFn  atomic.Pointer[func()] // Callback function to execute when inference container becomes unhealthy
}

func NewInferenceHealthStatus() *InferenceHealthStatus {
	infHealth := &InferenceHealthStatus{
		isHealthy: false,
		mu:        sync.Mutex{},
	}

	infHealth.healthyCond = sync.NewCond(&infHealth.mu)
	return infHealth
}

func (infHealth *InferenceHealthStatus) findHealth(ctx context.Context, workerHealth *health.Health) {
	result := workerHealth.Measure(ctx)

	infHealth.mu.Lock()
	newHealthVal := result.Status == health.StatusOK
	infHealth.isHealthy = newHealthVal
	if infHealth.isHealthy {
		nvcfMetrics.HealthcheckCounter.WithLabelValues("success").Inc()
		infHealth.healthyCond.Broadcast()
	} else {
		nvcfMetrics.HealthcheckCounter.WithLabelValues("failure").Inc()
		callBackFn := infHealth.CallBackFn.Load()
		if callBackFn != nil {
			(*callBackFn)()
		}
	}

	infHealth.mu.Unlock()
}

func (infHealth *InferenceHealthStatus) RunHealthCheckRoutine(ctx context.Context, workerHealth *health.Health) {
	// TODO: Once we move to go 1.23, use Tick() instead of NewTicker(), remove initial findHealth() and
	// run findHealth() in a loop. Ref: https://go.dev/src/time/tick.go
	infHealth.findHealth(ctx, workerHealth)
	ticker := time.NewTicker(healthCheckTimeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			infHealth.findHealth(ctx, workerHealth)
		}
	}
}

func (infHealth *InferenceHealthStatus) WaitForHealthyState(ctx context.Context) error {
	infHealth.mu.Lock()
	if infHealth.isHealthy {
		infHealth.mu.Unlock()
		return nil
	}

	infHealth.mu.Unlock()
	errCh := lo.Async(
		func() error {
			infHealth.mu.Lock()
			defer infHealth.mu.Unlock()
			for !infHealth.isHealthy {
				if ctx.Err() != nil {
					return ctx.Err()

				}
				infHealth.healthyCond.Wait()
			}
			return nil
		})
	select {
	case <-ctx.Done():
		infHealth.mu.Lock()
		infHealth.healthyCond.Broadcast()
		infHealth.mu.Unlock()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// This custom handler will be used until we require that users expose a proper health endpoint.
// Once that happens, we can remove this and use the standard http handler.
func inferenceHealthcheckHandlerWithExpectedCode(baseDomain, path string, expectedResponseCode int) (func(context.Context) error, error) {
	client, err := clients.DefaultHTTPClient(
		&clients.HTTPClientConfig{
			BaseClientConfig: &clients.BaseClientConfig{Addr: baseDomain},
			NumRetries:       0,
		},
		func(string, *http.Request) string {
			return "Inference Health Check"
		})
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) error {
		operation := func() error {
			healthUrl, err := url.JoinPath(baseDomain, path)
			if err != nil {
				return backoff.Permanent(err)
			}

			request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthUrl, nil)
			if err != nil {
				return backoff.Permanent(err)
			}
			resp, err := client.Client(ctx).Do(request)
			if err != nil {
				return backoff.Permanent(err)
			}
			defer resp.Body.Close()

			switch resp.StatusCode {
			case expectedResponseCode:
				return nil
			case http.StatusNotFound:
				// We know that their container is up, but doesn't support Triton endpoints.
				// Giving them upto 30 seconds as a buffer to finish initialization.
				return errors.New("received 404, retrying")
			default:
				return backoff.Permanent(fmt.Errorf("received unexpected status code: %d, expected: %d", resp.StatusCode, expectedResponseCode))
			}
		}

		err := backoff.Retry(operation, backoff.WithContext(
			backoff.NewExponentialBackOff(
				backoff.WithInitialInterval(2*time.Second), backoff.WithMaxElapsedTime(30*time.Second)),
			ctx))
		if err != nil {
			return err
		}
		return nil
	}, nil
}

func HealthCheckConfig(baseDomain, protocol, path string, timeout time.Duration, expectedResponseCode, port, fallbackPort int) (health.Config, error) {
	if protocol == "" {
		protocol = defaultInferenceHealthProtocol
	}
	protocol = strings.ToLower(protocol)
	if timeout == 0 {
		timeout = defaultInferenceHealthTimeout
	}
	if expectedResponseCode == 0 {
		expectedResponseCode = defaultInferenceHealthExpectedResponseCode
	}
	if port <= 0 {
		port = fallbackPort
	}
	if path == "" {
		path = defaultInferenceHealthEndpoint
	}

	inferenceHealthConfig := health.Config{
		Name:      "inference",
		Timeout:   timeout,
		SkipOnErr: false,
	}

	switch protocol {
	case "http":
		baseInferenceHealthUrl := fmt.Sprintf("%s://%s:%d", protocol, baseDomain, port)
		inferenceHealthCheckFunc, err := inferenceHealthcheckHandlerWithExpectedCode(baseInferenceHealthUrl, path, expectedResponseCode)
		if err != nil {
			return health.Config{}, err
		}
		inferenceHealthConfig.Check = inferenceHealthCheckFunc
		zap.L().Info("healthcheck configuration",
			utils.PublicLogMarker,
			zap.String("protocol", protocol),
			zap.String("domain", baseDomain),
			zap.Int("port", port),
			zap.String("url", path),
			zap.Duration("timeout", timeout),
			zap.Int("expected_response_code", expectedResponseCode))
	case "grpc":
		// grpc doesn't support path or status. we use the native grpc health check instead.
		inferenceHealthConfig.Check = healthgrpc.New(healthgrpc.Config{
			Target: fmt.Sprintf("%s:%d", baseDomain, port),
			DialOptions: []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
		})
		zap.L().Info("healthcheck configuration",
			utils.PublicLogMarker,
			zap.String("protocol", protocol),
			zap.String("domain", baseDomain),
			zap.Int("port", port),
			zap.Duration("timeout", timeout))
	default:
		return health.Config{}, fmt.Errorf("unknown health protocol %s", protocol)
	}
	return inferenceHealthConfig, nil
}
