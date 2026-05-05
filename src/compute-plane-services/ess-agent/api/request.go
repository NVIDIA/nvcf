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
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/hashicorp/vault/sdk/helper/consts"

	retryablehttp "github.com/hashicorp/go-retryablehttp"
)

// Request is a raw request configuration structure used to initiate
// API requests to the Vault server.
type Request struct {
	Method        string
	URL           *url.URL
	Host          string
	Params        url.Values
	Headers       http.Header
	ClientToken   string
	MFAHeaderVals []string
	WrapTTL       string
	Obj           interface{}

	// When possible, use BodyBytes as it is more efficient due to how the
	// retry logic works
	BodyBytes []byte

	// Fallback
	Body     io.Reader
	BodySize int64

	// Whether to request overriding soft-mandatory Sentinel policies (RGPs and
	// EGPs). If set, the override flag will take effect for all policies
	// evaluated during the request.
	PolicyOverride bool
}

// SetJSONBody is used to set a request body that is a JSON-encoded value.
func (r *Request) SetJSONBody(val interface{}) error {
	buf, err := json.Marshal(val)
	if err != nil {
		return err
	}

	r.Obj = val
	r.BodyBytes = buf
	return nil
}

// ResetJSONBody is used to reset the body for a redirect
func (r *Request) ResetJSONBody() error {
	if r.BodyBytes == nil {
		return nil
	}
	return r.SetJSONBody(r.Obj)
}

// DEPRECATED: ToHTTP turns this request into a valid *http.Request for use
// with the net/http package.
func (r *Request) ToHTTP() (*http.Request, error) {
	req, err := r.toRetryableHTTP()
	if err != nil {
		return nil, err
	}

	switch {
	case r.BodyBytes == nil && r.Body == nil:
		// No body

	case r.BodyBytes != nil:
		req.Request.Body = ioutil.NopCloser(bytes.NewReader(r.BodyBytes))

	default:
		if c, ok := r.Body.(io.ReadCloser); ok {
			req.Request.Body = c
		} else {
			req.Request.Body = ioutil.NopCloser(r.Body)
		}
	}

	return req.Request, nil
}

func (r *Request) toRetryableHTTP() (*retryablehttp.Request, error) {
	// Encode the query parameters
	r.URL.RawQuery = r.Params.Encode()

	// Create the HTTP request, defaulting to retryable
	var req *retryablehttp.Request

	var err error
	var body interface{}

	switch {
	case r.BodyBytes == nil && r.Body == nil:
		// No body

	case r.BodyBytes != nil:
		// Use bytes, it's more efficient
		body = r.BodyBytes

	default:
		body = r.Body
	}

	req, err = retryablehttp.NewRequest(r.Method, r.URL.RequestURI(), body)
	if err != nil {
		return nil, err
	}

	req.URL.User = r.URL.User
	req.URL.Scheme = r.URL.Scheme
	req.URL.Host = r.URL.Host
	req.Host = r.Host

	if r.Headers != nil {
		for header, vals := range r.Headers {
			for _, val := range vals {
				req.Header.Add(header, val)
			}
		}
	}

	if len(r.ClientToken) != 0 {
		req.Header.Set(consts.AuthHeaderName, r.ClientToken)
		// added for nv
		ShimVaultTokenHeader(req.Request, r.ClientToken)
	}

	if len(r.WrapTTL) != 0 {
		req.Header.Set("X-ESS-Wrap-TTL", r.WrapTTL)
	}

	if len(r.MFAHeaderVals) != 0 {
		for _, mfaHeaderVal := range r.MFAHeaderVals {
			req.Header.Add("X-ESS-MFA", mfaHeaderVal)
		}
	}

	if r.PolicyOverride {
		req.Header.Set("X-ESS-Policy-Override", "true")
	}

	return req, nil
}
