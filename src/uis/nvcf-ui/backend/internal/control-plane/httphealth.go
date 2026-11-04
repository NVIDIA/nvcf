// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func validateFQDN(fqdn string) error {
	if strings.HasPrefix(fqdn, "http://") || strings.HasPrefix(fqdn, "https://") {
		return fmt.Errorf("fqdn %q must not include a scheme", fqdn)
	}
	u, err := url.Parse("http://" + fqdn)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid fqdn %q", fqdn)
	}
	return nil
}

// checkEndpoints returns nil if every endpoint returns a 2xx response,
// otherwise an error describing the first failure.
func (m *Monitor) checkEndpoints(ctx context.Context, index int) error {
	for _, ep := range m.components[index].endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
		if err != nil {
			return fmt.Errorf("create request for %s: %w", ep, err)
		}
		resp, err := m.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("request %s: %w", ep, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("endpoint %s returned status %d", ep, resp.StatusCode)
		}
	}
	return nil
}
