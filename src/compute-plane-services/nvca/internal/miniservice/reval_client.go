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
	neturl "net/url"

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

// redactedHelmChartURL strips userinfo and the query string from a Helm
// chart URL so it is safe to log; neither is needed to identify the chart.
func redactedHelmChartURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "<unparseable>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

type revalClient struct {
	endpoint     string
	host         string
	tokenFetcher tokenFetcher
	httpClient   *http.Client
	metrics      *metrics.Metrics
}

// ReValClientOption configures a ReVal client.
type ReValClientOption func(*revalClient)

// WithReValHostHeaderOverride sets the HTTP Host header override for ReVal requests.
func WithReValHostHeaderOverride(host string) ReValClientOption {
	return func(c *revalClient) {
		c.host = host
	}
}

func NewReValClient(
	endpoint string,
	tf tokenFetcher,
	httpClient *http.Client,
	m *metrics.Metrics,
	opts ...ReValClientOption,
) ReValClient {
	c := &revalClient{
		endpoint:     endpoint,
		tokenFetcher: tf,
		httpClient:   httpClient,
		metrics:      m,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *revalClient) Render(ctx context.Context, input HelmReValRenderInput) (HelmReValRenderOutput, error) {
	const revalEndpoint = "/v1/render"
	url := fmt.Sprintf("%s%s", c.endpoint, revalEndpoint)
	method := http.MethodPost

	log := logf.FromContext(ctx).WithValues(
		"rpc", "reval.Render",
		"url", url,
		"method", method,
	)

	log.V(1).Info("Do ReVal request")
	// input.APIKey, input.HelmRegistryAuthConfig, and input.ImageRegistryAuthConfig
	// carry credentials and must not be logged. HelmChartURL is redacted too, in
	// case it embeds userinfo or a signed query string.
	log.V(2).WithValues(
		"helmChart", redactedHelmChartURL(input.HelmChartURL),
		"releaseName", input.ReleaseName,
		"instanceType", input.InstanceType,
		"gpu", input.GPUName,
		"k8sVersion", input.K8sVersion,
	).Info("Payload")

	// httpCode tracks the label value for the metric. It defaults to "error" (network failure),
	// is set to stage-specific values on pre-call failures, and to the HTTP status code on success.
	httpCode := "error"
	defer func() {
		c.metrics.RecordMiniServiceReValRequest(input.NCAID, revalEndpoint, httpCode)
	}()

	apiKey, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		httpCode = "token_fetch_error"
		if hce := core.HTTPCodeError(0); errors.As(err, &hce) && hce >= 400 && hce < 500 {
			log.Error(err, "Failed to fetch token")
			err = reconcile.TerminalError(err)
		}
		return HelmReValRenderOutput{}, err
	}

	//nolint:gosec // input.APIKey belongs in this request body, sent to the ReVal service itself
	payload, err := json.Marshal(input)
	if err != nil {
		httpCode = "marshal_error"
		log.Error(err, "Failed to encode input as JSON")
		return HelmReValRenderOutput{}, err
	}

	body := bytes.NewReader(payload)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		httpCode = "build_error"
		log.Error(err, "Failed to create request")
		return HelmReValRenderOutput{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if c.host != "" {
		req.Host = c.host
	}
	// Add trace parent and state from context.
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.httpClient.Do(req)
	if resp != nil {
		httpCode = fmt.Sprint(resp.StatusCode)
		defer resp.Body.Close()
	}

	if err != nil && resp == nil {
		// httpCode stays "error" — no HTTP response received.
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
