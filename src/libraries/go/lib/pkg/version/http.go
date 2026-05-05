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

package version

import (
	"fmt"
	"net/http"
)

type Transport struct {
	rt http.RoundTripper

	userAgent string
}

// NewTransport creates a RoundTripper that appends User-Agent for the current
// application (e.g. nvca/1.0.0)
func NewTransport(base http.RoundTripper, appName string) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &Transport{
		rt: base,
		// userAgent version should be statically compiled so doing this once at startup
		// should be fine
		userAgent: fmt.Sprintf("%s/%s", appName, ReleaseString()),
	}
}

// RoundTrip adds a User-Agent header with the product name and version
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.userAgent)
	// call downstream roundtrip chain
	return t.rt.RoundTrip(r)
}
