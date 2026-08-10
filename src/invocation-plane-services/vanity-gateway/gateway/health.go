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

package gateway

import (
	"context"
	"fmt"
	"github.com/hellofresh/health-go/v5"
	"net/http"
	"net/url"
	"time"
)

func healthManager(nvcfApiHost string, transport http.RoundTripper) (*health.Health, error) {
	client := http.Client{Timeout: 5 * time.Second, Transport: transport}
	healthUrl, err := url.JoinPath(nvcfApiHost, "/health")
	if err != nil {
		return nil, err
	}
	return health.New(health.WithComponent(health.Component{
		Name: "vanity gateway",
	}), health.WithChecks(health.Config{
		Name:    "nvcf api",
		Timeout: 5 * time.Second,
		Check: func(ctx context.Context) error {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthUrl, nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(request)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
			return fmt.Errorf("invalid nvcf api health response %d", resp.StatusCode)
		},
	}))
}
