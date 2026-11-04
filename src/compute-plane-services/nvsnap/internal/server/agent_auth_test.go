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

package server

import (
	"net/http"
	"testing"
)

// captureRT records the request it is handed and returns a canned response.
// A fake round tripper rather than httptest: this exercises the header logic
// with no listener, which also keeps the test runnable in sandboxes that
// block loopback TCP.
type captureRT struct{ got *http.Request }

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.got = r
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
}

func TestWithAgentAuth(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		preset   string // Authorization already on the request
		wantAuth string
	}{
		{
			name:     "token is sent as a bearer credential",
			token:    "s3cret",
			wantAuth: "Bearer s3cret",
		},
		{
			// The no-auth deployment must behave exactly as before, so an
			// empty token has to mean "no header", not "Bearer ".
			name:     "empty token sends no header",
			token:    "",
			wantAuth: "",
		},
		{
			// Preserves an explicit credential a caller already chose.
			name:     "existing Authorization is not overwritten",
			token:    "s3cret",
			preset:   "Bearer caller-supplied",
			wantAuth: "Bearer caller-supplied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &captureRT{}
			c := withAgentAuth(&http.Client{Transport: rt}, tt.token)

			req, err := http.NewRequest(http.MethodGet, "http://agent.invalid:8081/v1/checkpoints", http.NoBody)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tt.preset != "" {
				req.Header.Set("Authorization", tt.preset)
			}
			if _, err := c.Do(req); err != nil {
				t.Fatalf("do: %v", err)
			}

			if got := rt.got.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			// RoundTrip must not mutate the caller's request.
			if tt.preset == "" && req.Header.Get("Authorization") != "" {
				t.Errorf("caller request was mutated: %q", req.Header.Get("Authorization"))
			}
		})
	}
}

// An empty token must leave the client untouched, so a no-auth deployment
// keeps whatever transport it was constructed with.
func TestWithAgentAuthEmptyTokenLeavesTransportAlone(t *testing.T) {
	rt := &captureRT{}
	in := &http.Client{Transport: rt}
	out := withAgentAuth(in, "")

	if out != in {
		t.Errorf("client was replaced for an empty token")
	}
	if out.Transport != http.RoundTripper(rt) {
		t.Errorf("transport was wrapped for an empty token: %T", out.Transport)
	}
	if out.CheckRedirect != nil {
		t.Error("CheckRedirect was set for an empty token")
	}
}

// net/http strips Authorization when a redirect crosses origins, but a
// header-adding RoundTripper runs on the redirected request too and puts it
// back. Without CheckRedirect a compromised agent could answer any call with a
// 302 to a host it controls and be handed the shared token. The client must
// stop at the 3xx and hand it to the caller, who treats a non-200 as an error.
func TestWithAgentAuthDoesNotFollowRedirects(t *testing.T) {
	c := withAgentAuth(&http.Client{Transport: &captureRT{}}, "tok")

	if c.CheckRedirect == nil {
		t.Fatal("CheckRedirect not set; redirects would be followed with the token attached")
	}
	req, err := http.NewRequest(http.MethodGet, "http://evil.example/steal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckRedirect(req, nil); err != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}
