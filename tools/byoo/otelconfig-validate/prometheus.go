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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// PrometheusClient queries Grafana Cloud Prometheus for metrics.
type PrometheusClient struct {
	baseURL  string
	username string
	password string
	client   *http.Client
}

// PrometheusResponse represents the JSON response from the Prometheus query_range API.
type PrometheusResponse struct {
	Data struct {
		Result []MetricResult `json:"result"`
	} `json:"data"`
}

// MetricResult represents a single time-series result from Prometheus.
type MetricResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

// NewPrometheusClient creates a client from environment variables. It returns an
// error if any of the required variables are missing.
func NewPrometheusClient() (*PrometheusClient, error) {
	baseURL := os.Getenv("GRAFANA_CLOUD_PROMETHEUS_URL")
	username := os.Getenv("GRAFANA_CLOUD_PROMETHEUS_USERNAME")
	password := os.Getenv("GRAFANA_CLOUD_PROMETHEUS_PASSWORD")

	if baseURL == "" || username == "" || password == "" {
		return nil, fmt.Errorf("missing required environment variables: GRAFANA_CLOUD_PROMETHEUS_URL, GRAFANA_CLOUD_PROMETHEUS_USERNAME, GRAFANA_CLOUD_PROMETHEUS_PASSWORD")
	}

	return &PrometheusClient{
		baseURL:  baseURL,
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// QueryMetrics sends a query_range request to Prometheus and returns the parsed response.
func (p *PrometheusClient) QueryMetrics(wrapperType WrapperType, id, start, end, extraFilters string) (*PrometheusResponse, error) {
	queryURL := fmt.Sprintf("https://%s/api/v1/query_range", p.baseURL)

	queryString := fmt.Sprintf(`{ %s_id="%s", %s }`, string(wrapperType), id, extraFilters)
	log.Printf("Prometheus query string: `%s`", queryString)

	form := url.Values{}
	form.Set("query", queryString)
	form.Set("start", start)
	form.Set("end", end)
	form.Set("step", "30s")

	req, err := http.NewRequest(http.MethodPost, queryURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.SetBasicAuth(p.username, p.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("error status code: %d, message: %s", resp.StatusCode, string(body))
	}

	var promResp PrometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("parsing response JSON: %w", err)
	}

	return &promResp, nil
}
