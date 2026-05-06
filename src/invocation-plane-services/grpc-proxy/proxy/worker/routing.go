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
package worker

import (
	"golang.org/x/net/http2"
	"net/http"
	"nvcf-grpc-proxy/proxy/middleware"
)

type ProtoRoutingTransport struct {
	h1 http.RoundTripper
	h2 http.RoundTripper
}

func NewProtoRoutingTransport(h1 *http.Transport, h2 *http2.Transport) *ProtoRoutingTransport {
	return &ProtoRoutingTransport{h1: middleware.TracedRoundTripper(h1), h2: middleware.TracedRoundTripper(h2)}
}

func (t ProtoRoutingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.ProtoMajor != 2 {
		return t.h1.RoundTrip(request)
	}
	return t.h2.RoundTrip(request)
}
