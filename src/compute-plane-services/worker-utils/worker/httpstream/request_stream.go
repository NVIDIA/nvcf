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

package httpstream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type proxyURLKeyType string

const proxyURLKey proxyURLKeyType = "proxyURL"

type ProxyClient *http.Client // new type to force usage of NewProxiedClient

func NewProxiedClient() ProxyClient {
	protocols := &http.Protocols{}
	// go's http2 proxy support is terrible. it currently does not allow http upstreams on https
	// proxy connections because it is hardcoded to reject http schemes with the http1+http2
	// compatible http.Transport for http2 requests. the http2.Transport has no proxy configuration.
	// additionally, the bundled http2 transport, even if set with a debugger to allow http,
	// ignores proxy settings. https://github.com/golang/go/issues/25793
	// for the record, curl supports this just fine. https://curl.se/docs/manpage.html#--proxy-http2
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(false)
	protocols.SetUnencryptedHTTP2(false)
	return &http.Client{
		Transport: tracing.NewOtelTransport(&http.Transport{
			MaxIdleConnsPerHost: 256,
			// envoy is set to 1 hour 5 seconds.
			// ensure we time out first so we never send a request on a dead connection.
			IdleConnTimeout:    5 * time.Minute,
			DisableCompression: true,
			Protocols:          protocols,
			HTTP2: &http.HTTP2Config{
				MaxConcurrentStreams: 16, // try to avoid head of line blocking as much as possible
			},
			Proxy: func(r *http.Request) (*url.URL, error) {
				// proxy url from request context with static typed key
				value := r.Context().Value(proxyURLKey)
				if value == nil {
					return nil, nil
				}
				proxyURL, ok := value.(*url.URL)
				if !ok {
					return nil, fmt.Errorf("proxyURL is not a *url.URL")
				}
				return proxyURL, nil
			},
		}),
	}
}

const h1ContentType = "application/octet-stream+h1"

// RequestStreamHandler handles HTTP streams using separate GET and POST requests
// for receiving client request data and sending client responses respectively
type RequestStreamHandler struct {
	TargetURI                  string
	ProxyURI                   *url.URL
	ResponseAuthorizationToken string // For POST request to send responses
	RequestAuthorizationToken  string // For GET request to receive client data
	httpClient                 ProxyClient

	oneResponse                 sync.Once
	cancelGetClientResponseBody context.CancelFunc
	getClientRequestBody        func() (io.ReadCloser, error)
}

// NewRequestStreamHandler connects to the target URI using separate GET and POST connections
func NewRequestStreamHandler(ctx context.Context, sharedProxyClient ProxyClient, statelessConfig *pb.WorkerInvokeFunctionRequest_StatelessConfig, requestDataWritable *pb.WorkerInvokeFunctionRequest) (*RequestStreamHandler, error) {
	handler := RequestStreamHandler{
		httpClient: sharedProxyClient,
	}

	// We'll use the first connection config that has HTTP1ProtocolConfig
	if statelessConfig == nil || len(statelessConfig.GetConnectionConfigs()) == 0 {
		return nil, fmt.Errorf("invalid or empty StatelessConfig")
	}
	for _, conn := range statelessConfig.GetConnectionConfigs() {
		http1Config := conn.GetHttp1Config()
		if http1Config != nil {
			handler.TargetURI = http1Config.GetTargetURI()
			if proxyURIStr := http1Config.GetProxyURI(); proxyURIStr != "" {
				proxyURI, err := url.Parse(proxyURIStr)
				if err != nil {
					return nil, fmt.Errorf("failed to parse proxy URI: %w", err)
				}
				handler.ProxyURI = proxyURI
			}
			handler.ResponseAuthorizationToken = http1Config.GetResponseAuthorizationToken()
			handler.RequestAuthorizationToken = http1Config.GetRequestAuthorizationToken()
			break
		}
	}
	if handler.TargetURI == "" {
		return nil, fmt.Errorf("no HTTP1ProtocolConfig found in StatelessConfig")
	}

	// Set up proxy in context if needed
	if handler.ProxyURI != nil {
		ctx = context.WithValue(ctx, proxyURLKey, handler.ProxyURI)
	}

	// If a request auth token is provided, initiate the GET request to receive client data
	if handler.RequestAuthorizationToken != "" {
		// Create and send the GET request for client data in the background
		getTarget, err := url.JoinPath(handler.TargetURI, "/v2/nvcf/worker/request-attach")
		if err != nil {
			return nil, fmt.Errorf("failed to create attach GET request url: %w", err)
		}
		ctx, cancel := context.WithCancel(ctx)
		handler.cancelGetClientResponseBody = cancel
		getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getTarget, nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create GET request: %w", err)
		}

		getReq.Header.Set("Authorization", "Bearer "+handler.RequestAuthorizationToken)
		if requestDataWritable.RequestMethod == "" {
			// we need to populate the request data with values from the GET request
			getReq.Header.Set("Accept", h1ContentType)
		} else {
			// requesting just the body
			getReq.Header.Set("Accept", "application/octet-stream")
		}

		// Use BackgroundOnce to fetch client request body
		handler.getClientRequestBody = BackgroundOnce(func() (io.ReadCloser, error) {
			zap.L().Debug("sending GET request for client data", zap.String("url", getReq.URL.String()))
			dontCancel := time.AfterFunc(5*time.Second, cancel) // 5s timeout but only for the initial request
			resp, err := ((*http.Client)(handler.httpClient)).Do(getReq)
			if err != nil {
				dontCancel.Stop()
				return nil, fmt.Errorf("failed to send GET request: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				_ = resp.Body.Close()
				dontCancel.Stop()
				zap.L().Error("GET request failed", zap.Int("status", resp.StatusCode), zap.ByteString("body", bodyBytes))
				return nil, fmt.Errorf("GET request failed with status code %d: %s", resp.StatusCode, string(bodyBytes))
			}
			dontCancel.Stop()

			zap.L().Debug("GET request succeeded for client data", zap.String("url", getReq.URL.String()))
			return resp.Body, nil
		})
	} else {
		// If no request auth token is provided there is no more body to be read
		handler.getClientRequestBody = func() (io.ReadCloser, error) {
			return http.NoBody, nil
		}
	}

	if requestDataWritable.RequestMethod == "" {
		zap.L().Debug("missing request method, retrieving full request from GET body")
		// we need to populate the request data with values from the GET request
		body, err := handler.getClientRequestBody()
		if err != nil {
			return nil, fmt.Errorf("failed to get client request: %w", err)
		}
		// http.ReadRequest does not accept http/1.0 style request bodies with unknown length and no transfer encoding
		// so we need to read the headers and then read the body manually
		bufReader := bufio.NewReader(body)
		request, err := http.ReadRequest(bufReader)
		if err != nil {
			_ = body.Close()
			return nil, fmt.Errorf("failed to read client request: %w", err)
		}
		if len(request.TransferEncoding) > 0 && (len(request.TransferEncoding) != 1 || request.TransferEncoding[0] != "identity") {
			// we only support identity transfer encoding
			_ = body.Close()
			return nil, fmt.Errorf("unexpected transfer encoding: %v", request.TransferEncoding)
		}
		requestDataWritable.RequestMethod = request.Method
		requestDataWritable.RequestPath = request.URL.EscapedPath()
		if request.URL.RawQuery != "" {
			requestDataWritable.RequestPath += "?" + request.URL.RawQuery
		}
		if request.URL.Fragment != "" {
			requestDataWritable.RequestPath += "#" + request.URL.EscapedFragment()
		}
		// preserve any headers the invocation service sent, such as "nvcf-feature-disable-worker-compatibility", by appending
		for k, v := range request.Header {
			for _, vv := range v {
				requestDataWritable.RequestHeaders = append(requestDataWritable.RequestHeaders, &pb.StringKV{Key: k, Value: vv})
			}
		}
		handler.getClientRequestBody = func() (io.ReadCloser, error) {
			return struct {
				io.Reader
				io.Closer
			}{
				// read anything that was buffered by http.ReadRequest and then swap to the unbuffered original body for the remainder
				Reader: io.MultiReader(io.LimitReader(bufReader, int64(bufReader.Buffered())), body),
				Closer: body,
			}, nil
		}
	}
	return &handler, nil
}

var errAlreadySent = fmt.Errorf("response already sent")

// SendResponse writes the HTTP response to the invocation service
// it is only valid to call this function once
func (s *RequestStreamHandler) SendResponse(ctx context.Context, resp *http.Response, onFinishWrite func()) error {
	defer resp.Body.Close()
	err := errAlreadySent
	s.oneResponse.Do(func() {
		err = nil
		resp.ProtoMajor = 1
		resp.ProtoMinor = 1
		resp.TransferEncoding = nil
		removeHopByHopHeaders(resp.Header)
		// fix content length if it is not set
		// we are manually writing headers, so we need to set the content length
		resp.ContentLength = getContentTrueLength(resp)
		if resp.ContentLength > -1 && resp.Header.Get("Content-Length") == "" {
			if resp.Header == nil {
				resp.Header = make(http.Header, 1)
			}
			resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		}
		zap.L().Debug("sending response to invocation service")
		// Create and send the POST request for responses
		postTarget, urlErr := url.JoinPath(s.TargetURI, "/v2/nvcf/worker/request-attach")
		if urlErr != nil {
			err = fmt.Errorf("failed to create attach POST request url: %w", urlErr)
			return
		}

		// Manually serialise the HTTP response headers to a buffer
		headerBuf := bytebufferpool.Get()
		defer bytebufferpool.Put(headerBuf)

		text := httpStatusText(resp)
		if _, err = fmt.Fprintf(headerBuf, "HTTP/%d.%d %03d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode, text); err != nil {
			err = fmt.Errorf("failed to write HTTP status line in response: %w", err)
			return
		}
		if err = resp.Header.Write(headerBuf); err != nil {
			err = fmt.Errorf("failed to write HTTP headers in response: %w", err)
			return
		}
		if _, err = io.WriteString(headerBuf, "\r\n"); err != nil {
			err = fmt.Errorf("failed to write HTTP end of headers in response: %w", err)
			return
		}

		// calculate the content length if we can to skip transfer encoding
		attachRequestBodyLength := resp.ContentLength
		if attachRequestBodyLength > -1 {
			attachRequestBodyLength += int64(headerBuf.Len())
		}
		attachRequestBody := struct {
			io.Reader
			io.Closer
		}{
			// send headers first, then the body
			Reader: io.MultiReader(bytes.NewReader(headerBuf.Bytes()), resp.Body),
			Closer: utils.CloserFunc(func() error {
				err := resp.Body.Close()
				if onFinishWrite != nil {
					onFinishWrite()
				}
				return err
			}),
		}
		// Create the POST request with the pipe reader as the body
		if s.ProxyURI != nil {
			ctx = context.WithValue(ctx, proxyURLKey, s.ProxyURI)
		}

		postReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, postTarget, attachRequestBody)
		if reqErr != nil {
			err = fmt.Errorf("failed to create POST request: %w", reqErr)
			return
		}

		postReq.Header.Set("Authorization", "Bearer "+s.ResponseAuthorizationToken)
		postReq.Header.Set("Content-Type", h1ContentType)
		// this will help to reuse connections. sometimes clients read to exactly the content length
		// instead of to EOF. when sending a fixed length response inside a transfer encoded request
		// (which we were doing without setting this content length field) the last EOF chunk is
		// sometimes left unread which prevents connection reuse.
		postReq.ContentLength = attachRequestBodyLength

		// Send the POST request directly
		zap.L().Debug("sending POST request", zap.String("url", postReq.URL.String()))
		postResp, doErr := ((*http.Client)(s.httpClient)).Do(postReq)
		if doErr != nil {
			err = fmt.Errorf("failed to send POST request: %w", doErr)
			return
		}
		defer postResp.Body.Close()

		if postResp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(io.LimitReader(postResp.Body, 1024))
			err = fmt.Errorf("POST request failed with status code %d: %s", postResp.StatusCode, string(bodyBytes))
			zap.L().Error("POST request failed", zap.Int("status", postResp.StatusCode), zap.ByteString("body", bodyBytes))
			return
		}

		zap.L().Debug("POST request succeeded for sending responses", zap.String("url", postReq.URL.String()))
	})

	return err
}

// httpStatusText lifted from http.Response.Write() because we don't want its body handling
func httpStatusText(resp *http.Response) string {
	// Status line
	text := resp.Status
	if text == "" {
		text = http.StatusText(resp.StatusCode)
		if text == "" {
			text = "status code " + strconv.Itoa(resp.StatusCode)
		}
	} else {
		// Just to reduce stutter, if user set r.Status to "200 OK" and StatusCode to 200.
		// Not important.
		text = strings.TrimPrefix(text, strconv.Itoa(resp.StatusCode)+" ")
	}
	return text
}

func getContentTrueLength(resp *http.Response) int64 {
	if resp.ContentLength != 0 {
		return resp.ContentLength
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		return 0
	}
	if resp.Header.Get("Content-Length") != "" {
		contentLength, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return -1
		}
		return contentLength
	}
	return -1
}

// GetClientRequestBody returns the upstream (ie the end user client) request body and any error encountered during request setup
// it is only valid to call this function once as the returned reader is shared.
func (s *RequestStreamHandler) GetClientRequestBody() (io.ReadCloser, error) {
	return s.getClientRequestBody()
}

// Close closes the pipe writer and response body
func (s *RequestStreamHandler) Close() error {
	// Cancel context to abort any in-progress requests
	if s.cancelGetClientResponseBody != nil {
		s.cancelGetClientResponseBody()
	}
	// Close client request body if it exists
	body, _ := s.getClientRequestBody()
	if body != nil {
		return body.Close()
	}
	return nil
}
