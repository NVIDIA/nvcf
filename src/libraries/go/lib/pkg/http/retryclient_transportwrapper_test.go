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
	"context"
	"net/http"
	"testing"
)

type sentinelRoundTripper struct{ inner http.RoundTripper }

func (s sentinelRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.inner.RoundTrip(req)
}

// TestWithTransportWrapper_AppliedAsOutermostLayer verifies the wrapper option
// is applied to the client's transport as the outermost layer.
func TestWithTransportWrapper_AppliedAsOutermostLayer(t *testing.T) {
	var called bool
	client := NewRetryableClient(context.Background(),
		WithAppVersionUserAgent("test"),
		WithTransportWrapper(func(inner http.RoundTripper) http.RoundTripper {
			called = true
			return sentinelRoundTripper{inner: inner}
		}),
	)
	if !called {
		t.Fatalf("expected transport wrapper to be invoked")
	}
	if _, ok := client.Transport.(sentinelRoundTripper); !ok {
		t.Fatalf("expected outermost transport to be sentinelRoundTripper, got %T", client.Transport)
	}
}

// TestWithTransportWrapper_NilIsIgnored verifies a nil wrapper does not alter
// the transport chain.
func TestWithTransportWrapper_NilIsIgnored(t *testing.T) {
	client := NewRetryableClient(context.Background(), WithTransportWrapper(nil))
	if _, ok := client.Transport.(sentinelRoundTripper); ok {
		t.Fatalf("nil wrapper must not wrap the transport")
	}
}
