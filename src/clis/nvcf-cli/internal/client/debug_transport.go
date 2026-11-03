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

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
)

const redactedValue = "[REDACTED]"

// debugTransport is an HTTP transport that logs requests and responses
type debugTransport struct {
	transport http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface with debug logging
func (d *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Log the request (this should include auth headers added by underlying transports)
	log.Println("DEBUG: HTTP Request")
	log.Println("---")

	// Log request basics
	log.Printf("Method: %s", req.Method)
	log.Printf("URL: %s", req.URL.String())

	// Log Host header explicitly (it's stored in req.Host, not req.Header)
	if req.Host != "" {
		log.Printf("Host: %s", req.Host)
	}

	// Log headers (mask sensitive ones)
	log.Println("Headers:")
	if len(req.Header) == 0 && req.Host == "" {
		log.Println("  [No headers set]")
	}
	for name, values := range req.Header {
		for _, value := range values {
			if isSensitiveHeader(name) {
				log.Printf("  %s: [REDACTED]", name)
			} else {
				log.Printf("  %s: %s", name, value)
			}
		}
	}

	// Log request body if present, with credential values masked
	if req.Body != nil {
		reqDump, err := httputil.DumpRequestOut(req, true)
		if err == nil {
			bodyStart := bytes.Index(reqDump, []byte("\r\n\r\n"))
			if bodyStart != -1 && bodyStart+4 < len(reqDump) {
				body := reqDump[bodyStart+4:]
				if len(bytes.TrimSpace(body)) != 0 {
					log.Printf("Request Body:\n%s", redactRequestBody(body))
				}
			}
		}
	}

	log.Println("---")

	// Make the actual request
	resp, err := d.transport.RoundTrip(req)
	if err != nil {
		log.Printf("DEBUG: HTTP Request failed: %v", err)
		return nil, err
	}

	// Log the response
	log.Println("DEBUG: HTTP Response")
	log.Println("---")
	log.Printf("Status: %s", resp.Status)

	// Log response headers (mask sensitive ones)
	log.Println("Headers:")
	for name, values := range resp.Header {
		for _, value := range values {
			if isSensitiveHeader(name) {
				log.Printf("  %s: [REDACTED]", name)
			} else {
				log.Printf("  %s: %s", name, value)
			}
		}
	}

	// Log response body if it's reasonably small
	if resp.Body != nil {
		respDump, err := httputil.DumpResponse(resp, true)
		if err == nil {
			bodyStart := strings.Index(string(respDump), "\r\n\r\n")
			if bodyStart != -1 && bodyStart+4 < len(respDump) {
				body := string(respDump[bodyStart+4:])
				if strings.TrimSpace(body) != "" && len(body) < 10000 { // Don't log huge responses
					log.Printf("Response Body:\n%s", body)
				} else if len(body) >= 10000 {
					log.Printf("Response Body: [%d bytes - too large to display]", len(body))
				}
			}
		}
	}

	log.Println("---")

	return resp, nil
}

// isSensitiveHeader checks if a header contains sensitive information
func isSensitiveHeader(name string) bool {
	sensitiveHeaders := []string{
		"authorization",
		"cookie",
		"set-cookie",
		"x-api-key",
		"bearer",
		"token",
		"secret",
		"password",
	}

	lowerName := strings.ToLower(name)
	for _, sensitive := range sensitiveHeaders {
		if strings.Contains(lowerName, sensitive) {
			return true
		}
	}
	return false
}

// redactRequestBody returns a request body that is safe to log. Every value
// under a "secret" or "secrets" key is masked; only string "name" fields are
// kept so the log still shows which secret a request sets. A body that is not
// JSON cannot be inspected for credentials, so only its size is returned.
func redactRequestBody(body []byte) string {
	if !json.Valid(body) {
		return fmt.Sprintf("[%d bytes, not JSON, omitted]", len(body))
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var parsed interface{}
	if err := dec.Decode(&parsed); err != nil {
		return fmt.Sprintf("[%d bytes, omitted]", len(body))
	}
	redacted, changed := redactSecretFields(parsed)
	if !changed {
		return string(body)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redacted); err != nil {
		return fmt.Sprintf("[%d bytes, omitted]", len(body))
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// redactSecretFields walks a decoded JSON value and masks the subtree of each
// "secret" or "secrets" key. It reports whether any such key was found.
func redactSecretFields(v interface{}) (interface{}, bool) {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			if strings.EqualFold(k, "secret") || strings.EqualFold(k, "secrets") {
				t[k] = maskSecretValue(child)
				changed = true
				continue
			}
			if masked, c := redactSecretFields(child); c {
				t[k] = masked
				changed = true
			}
		}
	case []interface{}:
		for i, child := range t {
			if masked, c := redactSecretFields(child); c {
				t[i] = masked
				changed = true
			}
		}
	}
	return v, changed
}

// maskSecretValue masks a secret subtree, keeping only string "name" fields
// of objects. Arrays are masked element by element.
func maskSecretValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			if _, isString := child.(string); isString && strings.EqualFold(k, "name") {
				continue
			}
			t[k] = redactedValue
		}
		return t
	case []interface{}:
		for i, child := range t {
			t[i] = maskSecretValue(child)
		}
		return t
	default:
		return redactedValue
	}
}

// newDebugTransport creates a new debug transport wrapper
func newDebugTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &debugTransport{transport: base}
}

// hostHeaderTransport is an HTTP transport that sets the Host header for
// hostname-based routing in self-hosted deployments where multiple NVCF
// services (api, invocation, sis, ...) share a single ELB / ingress and are
// disambiguated by the Host header.
//
// The transport is configured with:
//   - apiURLHost:  the host part of base_http_url (the ELB hostname). The
//     override only applies to requests whose URL points here.
//     Requests to other hosts (e.g. SIS) are left alone.
//   - hostHeader:  the value to send as Host (typically "api.<elb>").
type hostHeaderTransport struct {
	apiURLHost string
	hostHeader string
	debug      bool
	base       http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface.
//
// The decision tree (important: http.NewRequestWithContext always pre-populates
// req.Host with URL.Host, so we cannot use "req.Host != \"\"" to detect an
// explicit override):
//
//  1. Request targets a different host than apiURLHost (e.g. SIS at
//     sis.<elb>) → leave Host alone.
//  2. Request targets apiURLHost and req.Host == req.URL.Host → this is the
//     Go default; the caller did not override → rewrite to hostHeader.
//  3. Request targets apiURLHost and req.Host differs from req.URL.Host →
//     caller explicitly set req.Host (e.g. invoke_host) → preserve.
func (t *hostHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiURLHost == "" || req.URL == nil || req.URL.Host != t.apiURLHost {
		if t.debug {
			urlHost := ""
			if req.URL != nil {
				urlHost = req.URL.Host
			}
			log.Printf("DEBUG: Host header transport: request URL host %q != configured api host %q, leaving Host=%q untouched", urlHost, t.apiURLHost, req.Host)
		}
		return t.base.RoundTrip(req)
	}

	if req.Host != "" && req.Host != req.URL.Host {
		if t.debug {
			log.Printf("DEBUG: Host header explicitly overridden by caller to %q (skipping api_host override %q)", req.Host, t.hostHeader)
		}
		return t.base.RoundTrip(req)
	}

	req.Host = t.hostHeader
	if t.debug {
		log.Printf("DEBUG: Using Host header override: %s", t.hostHeader)
	}
	return t.base.RoundTrip(req)
}

// newHostHeaderTransport creates a new host header transport wrapper.
// apiURLHost is the host portion of base_http_url; the Host header is only
// rewritten for requests targeting that host.
func newHostHeaderTransport(apiURLHost, hostHeader string, debug bool, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &hostHeaderTransport{
		apiURLHost: apiURLHost,
		hostHeader: hostHeader,
		debug:      debug,
		base:       base,
	}
}
