/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package harness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const httpTimeout = 30 * time.Second

// HTTPResponse is the status code and body of one HTTP exchange.
type HTTPResponse struct {
	Status int
	Body   string
}

// HTTPClient sends one HTTP request. Header values may carry credentials,
// so implementations must not log them.
type HTTPClient interface {
	Do(ctx context.Context, method, url string, headers map[string]string, body string) (HTTPResponse, error)
}

type netHTTPClient struct {
	client *http.Client
}

// NewHTTPClient returns an HTTPClient backed by net/http. Redirects are not
// followed so a test can assert on a 301 directly.
func NewHTTPClient() HTTPClient {
	return &netHTTPClient{client: &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Do sends the request and returns the status and full body. The error text
// never includes header values.
func (c *netHTTPClient) Do(ctx context.Context, method, url string, headers map[string]string, body string) (HTTPResponse, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("build %s request to %s: %w", method, url, err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("read %s %s response: %w", method, url, err)
	}
	return HTTPResponse{Status: resp.StatusCode, Body: string(data)}, nil
}
