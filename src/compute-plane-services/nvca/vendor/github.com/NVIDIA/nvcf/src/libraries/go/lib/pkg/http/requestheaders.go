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

package http

import (
	"net/http"
)

type clientRequestHeadersTransport struct {
	rt http.RoundTripper

	headers map[string]string
}

// newClientRequestHeadersTransport creates a RoundTripper that sets a default header.
// note if this header is provided in the request itself the request itself will take precedence
func newClientRequestHeadersTransport(base http.RoundTripper, reqHeaders map[string]string) *clientRequestHeadersTransport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &clientRequestHeadersTransport{
		rt:      base,
		headers: reqHeaders,
	}
}

// RoundTrip adds a User-Agent header with the product name and version
func (t *clientRequestHeadersTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	// call downstream roundtrip chain
	return t.rt.RoundTrip(r)
}
