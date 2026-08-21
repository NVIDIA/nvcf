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
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	config "ai-api-gateway-service/gateway_config"

	"go.opentelemetry.io/otel/trace"
)

// LLMGatewayDirector proxies vanity routes to the LLM Gateway without altering
// the request body. The LLM Gateway resolves the target function from the model
// field the client already supplies, so the gateway forwards the request as-is
// rather than stamping function-id headers or rewriting the path.
type LLMGatewayDirector struct {
	rp     *httputil.ReverseProxy
	host   string
	scheme string
}

type LLMGatewayRequest struct {
	CustomHeaders  config.CustomHeaders
	EOL            time.Time
	OfflineMessage string
}

func NewLLMGatewayDirector(endpoint string, transport http.RoundTripper) (*LLMGatewayDirector, error) {
	endpointUrl, err := url.Parse(endpoint)
	if err != nil || endpointUrl.Scheme == "" || endpointUrl.Host == "" {
		return nil, fmt.Errorf("invalid LLM Gateway endpoint: %s", endpoint)
	}
	return &LLMGatewayDirector{
		rp:     newGatewayReverseProxy(transport),
		host:   endpointUrl.Host,
		scheme: endpointUrl.Scheme,
	}, nil
}

// UpstreamHostname is the LLM Gateway host without its port, used to reject a
// configured host that would make the gateway proxy to itself.
func (d *LLMGatewayDirector) UpstreamHostname() string {
	return hostWithoutPort(d.host)
}

func hostWithoutPort(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		return hostname
	}
	return host
}

func (d *LLMGatewayDirector) ServeProxy(target LLMGatewayRequest, writer http.ResponseWriter, request *http.Request) error {
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(traceAttrEndpointType.String(traceAttrValueEndpointLLMGateway))

	if writeFunctionStatusError(writer, target.OfflineMessage, target.EOL, "") {
		return nil
	}

	request.URL.Host = d.host
	request.URL.Scheme = d.scheme
	request.Host = ""
	applyCustomHeaders(request, target.CustomHeaders)

	if !target.EOL.IsZero() {
		writer.Header().Set("Deprecation", target.EOL.Format(time.RFC3339))
	}

	var proxyErr error
	rp := *d.rp
	rp.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		proxyErr = err
		writeProxyError(writer, request, err)
	}
	rp.ServeHTTP(writer, request)
	return proxyErr
}
