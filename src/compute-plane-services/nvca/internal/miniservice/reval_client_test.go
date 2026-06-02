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
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReValClientHostHeader(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantHost string
	}{
		{
			name:     "uses URL host by default",
			wantHost: "bare-elb.example.invalid",
		},
		{
			name:     "uses configured host override",
			host:     "reval.bare-elb.example.invalid",
			wantHost: "reval.bare-elb.example.invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHost string
			httpClient := &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					gotHost = req.Host
					if gotHost == "" {
						gotHost = req.URL.Host
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"valid":true}`)),
						Request:    req,
					}, nil
				}),
			}

			client := NewReValClient(
				"http://bare-elb.example.invalid",
				testTokenFetcher{},
				httpClient,
				nil,
				WithReValHostHeaderOverride(tt.host),
			)
			_, err := client.Render(t.Context(), HelmReValRenderInput{NCAID: "test-nca"})
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, gotHost)
		})
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
