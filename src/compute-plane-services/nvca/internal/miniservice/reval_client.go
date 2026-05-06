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

package mscontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/otel/propagation"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

type ReValClient interface {
	Render(ctx context.Context, input HelmReValRenderInput) (HelmReValRenderOutput, error)
}

type tokenFetcher interface {
	FetchToken(ctx context.Context) (string, error)
}

type revalClient struct {
	endpoint     string
	tokenFetcher tokenFetcher
	httpClient   *http.Client
	metrics      *metrics.Metrics
}

func NewReValClient(
	endpoint string,
	tf tokenFetcher,
	httpClient *http.Client,
	m *metrics.Metrics,
) ReValClient {
	return &revalClient{
		endpoint:     endpoint,
		tokenFetcher: tf,
		httpClient:   httpClient,
		metrics:      m,
	}
}

func (c *revalClient) Render(ctx context.Context, input HelmReValRenderInput) (HelmReValRenderOutput, error) {
	url := fmt.Sprintf("%s/v1/render", c.endpoint)
	method := http.MethodPost

	log := logf.FromContext(ctx).WithValues(
		"rpc", "reval.Render",
		"url", url,
		"method", method,
	)

	log.V(1).Info("Do ReVal request")
	log.V(2).WithValues("input", input).Info("Payload")

	apiKey, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		if hce := core.HTTPCodeError(0); errors.As(err, &hce) && hce >= 400 && hce < 500 {
			log.Error(err, "Failed to fetch token")
			err = reconcile.TerminalError(err)
		}
		return HelmReValRenderOutput{}, err
	}

	payload, err := json.Marshal(input)
	if err != nil {
		log.Error(err, "Failed to encode input as JSON")
		return HelmReValRenderOutput{}, err
	}

	body := bytes.NewReader(payload)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		log.Error(err, "Failed to create request")
		return HelmReValRenderOutput{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	// Add trace parent and state from context.
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))

	var statusCode int
	resp, err := c.httpClient.Do(req)
	if resp != nil {
		statusCode = resp.StatusCode
		defer resp.Body.Close()
	}

	c.metrics.RecordMiniServiceReValRequest(input.NCAID, req.URL.Path, fmt.Sprint(statusCode))

	if err != nil && resp == nil {
		log.Error(err, "Do request failed")
		return HelmReValRenderOutput{}, reconcile.TerminalError(err)
	}
	if resp.StatusCode != http.StatusOK {
		shouldRetry, retryErr := retryablehttp.ErrorPropagatedRetryPolicy(ctx, resp, err)
		if retryErr == nil && err == nil {
			err = fmt.Errorf("unexpected HTTP status: %s", resp.Status)
		} else if err == nil {
			err = retryErr
		}
		if b, rerr := io.ReadAll(resp.Body); rerr != nil {
			log.Error(err, "Bad response status (read body failed on bad status code)",
				"read_error", rerr, "status", resp.Status)
		} else {
			log.Error(err, "Bad response status", "status", resp.Status, "body", string(b))
		}
		if !shouldRetry {
			err = reconcile.TerminalError(err)
		}
		return HelmReValRenderOutput{}, err
	}

	output := HelmReValRenderOutput{}
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		log.Error(err, "Failed to decode output body")
		return HelmReValRenderOutput{}, err
	}

	log.V(1).Info("Got ReVal response")
	log.V(2).WithValues("output", output).Info("Response")

	return output, nil
}
