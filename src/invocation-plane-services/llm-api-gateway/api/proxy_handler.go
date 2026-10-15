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

package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/provider"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

func (h *OpenAIProxyHandlers) RegisterRoutes(group *echo.Group) {
	group.POST("/v1/embeddings", h.Embeddings)
}

func (h *OpenAIProxyHandlers) forwardJSONRequest(
	c *GatewayContext,
	reqCtx *requestctx.RequestContext,
	body []byte,
	inputTokens int,
) error {
	outboundBody := body
	if reqCtx != nil && reqCtx.Model != "" {
		rewrittenBody, changed, err := rewriteJSONModel(body, reqCtx.Model)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if changed {
			outboundBody = rewrittenBody
		}
	}

	resp, err := h.dispatchProxyRequest(
		c,
		reqCtx,
		c.Request().Header.Clone(),
		io.NopCloser(bytes.NewReader(outboundBody)),
		int64(len(outboundBody)),
		inputTokens,
	)
	if err != nil {
		return err
	}
	if resp == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "proxy provider returned no response")
	}

	return writeProxyResponse(c, resp)
}

func (h *OpenAIProxyHandlers) requireFunctionRequestContext(
	c *GatewayContext,
) (*requestctx.RequestContext, error) {
	if c == nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "request context is required")
	}
	if c.RequestContext() != nil {
		return c.RequestContext(), nil
	}
	return nil, echo.NewHTTPError(
		http.StatusBadRequest,
		"model prefix is required",
	)
}

func (h *OpenAIProxyHandlers) dispatchProxyRequest(
	c *GatewayContext,
	reqCtx *requestctx.RequestContext,
	headers http.Header,
	body io.ReadCloser,
	contentLength int64,
	inputTokens int,
) (*provider.ProxyResponse, error) {
	if h.handlers.proxyProvider == nil {
		return nil, echo.NewHTTPError(http.StatusNotImplemented, "proxy endpoint is not configured")
	}

	if headers == nil {
		headers = make(http.Header)
	}
	if contentLength >= 0 {
		headers.Set(echo.HeaderContentLength, strconv.FormatInt(contentLength, 10))
	} else {
		headers.Del(echo.HeaderContentLength)
	}

	resp, err := h.handlers.proxyProvider.Proxy(
		c.UserContext(),
		reqCtx,
		&provider.ProxyRequest{
			Method:        c.Request().Method,
			Path:          stargatePath(c.Request().URL.Path),
			RawQuery:      c.Request().URL.RawQuery,
			Header:        headers,
			Body:          body,
			InputTokens:   inputTokens,
			TokenEstimate: inputTokens,
		},
	)
	if err != nil {
		if errors.Is(err, provider.ErrProxyNotSupported) {
			return nil, echo.NewHTTPError(http.StatusNotImplemented, err.Error())
		}
		return nil, providerHTTPError(err)
	}
	return resp, nil
}

func writeProxyResponse(c *GatewayContext, resp *provider.ProxyResponse) error {
	if resp == nil {
		return echo.NewHTTPError(http.StatusBadGateway, "proxy provider returned no response")
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	copyProxyHeaders(c.Response().Header(), resp.Header)
	c.Response().WriteHeader(resp.StatusCode)
	if resp.Body == nil {
		return nil
	}
	_, err := io.Copy(c.Response().Writer, resp.Body)
	return err
}

func copyProxyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(key)]; skip {
			continue
		}
		if strings.EqualFold(key, echo.HeaderContentLength) {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
