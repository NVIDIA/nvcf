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

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"

	"github.com/NVIDIA/nvcf/src/uis/nvcf-spark-ui/backend/internal/utils"
)

// Where and how the BFF reaches the LLM API Gateway. The defaults suit the BFF
// running in the stack's namespace, where the gateway's TLS certificate covers
// the Service's short name.
const (
	defaultGatewayURL            = "https://llm-api-gateway:8080"
	defaultGatewayTimeoutSeconds = 15
	defaultChatTimeoutSeconds    = 600
	defaultGatewayAPIKeyPath     = "/var/run/secrets/demo-ui/api-key"

	gatewayURL            = "GATEWAY_URL"
	gatewayCAPath         = "GATEWAY_CA_PATH"
	gatewayTimeoutSeconds = "GATEWAY_TIMEOUT_SECONDS"
	chatTimeoutSeconds    = "CHAT_TIMEOUT_SECONDS"
	gatewayAPIKeyPath     = "GATEWAY_API_KEY_PATH"
)

// gatewayConfig holds what the forwarding routes need.
type gatewayConfig struct {
	url       *url.URL
	transport http.RoundTripper
	// key is the demo-ui caller key, read once at startup.
	key string
	// timeout bounds the registry and model reads, so the UI's polling fails
	// fast when the gateway hangs.
	timeout time.Duration
	// chatTimeout replaces the server's write deadline for one chat request, so
	// a stream can outlive WRITE_TIMEOUT_SECONDS.
	chatTimeout time.Duration
}

// gatewayFromEnv resolves the gateway settings. GATEWAY_CA_PATH names a PEM
// bundle to trust, normally the stack CA; without it the system roots apply.
// The key is read once, from GATEWAY_API_KEY_PATH. The Helm chart rolls the
// pod when the key's Secret changes, so there is nothing to reload.
func gatewayFromEnv() (gatewayConfig, error) {
	raw := utils.GetEnvOr(gatewayURL, defaultGatewayURL)
	target, err := url.Parse(raw)
	if err != nil {
		return gatewayConfig{}, fmt.Errorf("%s: %w", gatewayURL, err)
	}
	if (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" ||
		(target.Path != "" && target.Path != "/") || target.RawQuery != "" {
		return gatewayConfig{}, fmt.Errorf("%s: %q must be just a scheme and host, such as %s",
			gatewayURL, raw, defaultGatewayURL)
	}
	target.Path = ""
	transport, err := gatewayTransport(os.Getenv(gatewayCAPath))
	if err != nil {
		return gatewayConfig{}, err
	}
	timeout, err := secondsFromEnv(gatewayTimeoutSeconds, defaultGatewayTimeoutSeconds)
	if err != nil {
		return gatewayConfig{}, err
	}
	chatTimeout, err := secondsFromEnv(chatTimeoutSeconds, defaultChatTimeoutSeconds)
	if err != nil {
		return gatewayConfig{}, err
	}
	key, err := readKey(utils.GetEnvOr(gatewayAPIKeyPath, defaultGatewayAPIKeyPath))
	if err != nil {
		return gatewayConfig{}, err
	}
	return gatewayConfig{url: target, transport: transport, key: key, timeout: timeout, chatTimeout: chatTimeout}, nil
}

// readKey returns the trimmed key at path. It must be a single token, because
// it goes into an Authorization header.
func readKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", gatewayAPIKeyPath, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", fmt.Errorf("%s: %s must hold a single token", gatewayAPIKeyPath, path)
	}
	return key, nil
}

// gatewayTransport returns the transport for gateway requests, trusting the
// PEM certificates at caPath when it is set.
func gatewayTransport(caPath string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Every open browser polls the registry every 5 s; keep those connections warm.
	transport.MaxIdleConnsPerHost = 16
	if caPath == "" {
		return transport, nil
	}
	bundle, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gatewayCAPath, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		return nil, fmt.Errorf("%s: %s holds no PEM certificate", gatewayCAPath, caPath)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return transport, nil
}

// registerGateway forwards the LLM API Gateway routes the UI uses. Each route
// is registered with its method, so the BFF attaches its key to these requests
// and no others; anything else under /v1/ reaches the JSON 404 catch-all.
func registerGateway(router *http.ServeMux, gw gatewayConfig, logger zerolog.Logger) {
	proxy := newProxy(gw.url, gw.transport, gw.key, logger)
	read := withTimeout(proxy, gw.timeout)
	router.Handle("GET /v1/registry", read)
	router.Handle("GET /v1/models", read)
	// Model names may contain "/", so the id is the rest of the path.
	router.Handle("GET /v1/models/{id...}", read)
	router.Handle("POST /v1/chat/completions", limitBody(streaming(proxy, gw.chatTimeout), maxChatBody))
}

// maxChatBody caps a chat request body. A long conversation is tens of KB, so
// 1 MiB is generous; the cap keeps the open playground from relaying
// arbitrarily large uploads to the gateway under the BFF's key.
const maxChatBody = 1 << 20

// tooLarge is the body the gateway itself sends for an oversized request.
const tooLarge = "Request Entity Too Large"

// limitBody answers 413 for a body over max: at once when Content-Length
// says so, and from the proxy's error handler when a body without one runs
// over while it is forwarded.
func limitBody(h http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > max {
			writeError(w, http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, max)
		h.ServeHTTP(w, r)
	})
}

// newProxy returns a handler that reverse-proxies requests unchanged to target,
// as nvcf-ui does for its upstreams, with three differences:
//   - The browser's Authorization and Cookie headers never reach the gateway;
//     the BFF's key replaces them.
//   - Errors are JSON in the gateway's {"message": ...} shape.
//   - The gateway's X-Request-Id joins the request's log fields.
func newProxy(target *url.URL, transport http.RoundTripper, key string, logger zerolog.Logger) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+key)
		},
		Transport: transport,
		ErrorLog:  stdlog.New(logger, "", 0),
		ModifyResponse: func(resp *http.Response) error {
			if id := resp.Header.Get("X-Request-Id"); id != "" {
				hlog.FromRequest(resp.Request).UpdateContext(func(c zerolog.Context) zerolog.Context {
					return c.Str("gateway_request_id", id)
				})
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var overLimit *http.MaxBytesError
			switch {
			case errors.Is(err, context.Canceled): // client went away; nothing to send
				return
			case errors.As(err, &overLimit):
				writeError(w, http.StatusRequestEntityTooLarge, tooLarge)
			case errors.Is(err, context.DeadlineExceeded):
				hlog.FromRequest(r).Warn().Err(err).Msgf("upstream %s timed out", target.Host)
				writeError(w, http.StatusGatewayTimeout, "LLM API Gateway timed out")
			default:
				hlog.FromRequest(r).Error().Err(err).Msgf("upstream %s error", target.Host)
				writeError(w, http.StatusBadGateway, "LLM API Gateway unavailable")
			}
		},
	}
}

// withTimeout bounds a forwarded request with d; d <= 0 leaves it unbounded.
func withTimeout(h http.Handler, d time.Duration) http.Handler {
	if d <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// streaming replaces the server-wide write deadline with d for one request, so
// a chat stream can outlive WRITE_TIMEOUT_SECONDS without raising it for every
// route. Reading the request body keeps READ_TIMEOUT_SECONDS; net/http clears
// the read deadline once the body is read, so it never cuts a stream. The
// gateway request ends at the same deadline. d <= 0 removes the write deadline.
func streaming(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var deadline time.Time
		if d > 0 {
			deadline = time.Now().Add(d)
		}
		if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
			hlog.FromRequest(r).Warn().Err(err).Msg("Chat stream keeps the server-wide write deadline")
		}
		if d > 0 {
			ctx, cancel := context.WithDeadline(r.Context(), deadline)
			defer cancel()
			r = r.WithContext(ctx)
		}
		h.ServeHTTP(w, r)
	})
}
