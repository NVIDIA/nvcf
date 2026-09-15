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

import "net/http"

// agentAuthTransport adds the shared bearer token to every request the server
// sends to an agent.
//
// The server is an agent client: cascadeDeleteCheckpoint drops the L1 dump,
// dispatch posts captures, and the checkpoint poller lists them. Once the agent
// runs with --auth-mode=required it rejects all of those, and the delete path
// fails in the worst possible way -- it returns 204, deletes the catalog row,
// and orphans the on-disk dump with nothing left pointing at it (nvsnap#736).
//
// Wrapping the transport rather than editing call sites means an agent endpoint
// added later is authenticated without anyone remembering to do it, matching
// the agent's own outbound wrapper in internal/agent/auth.go.
//
// This is deliberately simpler than the agent's version, which stores the token
// in an atomic pointer because its peer client is built at import time, before
// the token is known. The server builds its client in New() with config already
// parsed, so a plain field is enough.
//
// An empty token installs no wrapper at all (see New), so a deployment without
// agent.auth.enabled behaves exactly as before.
type agentAuthTransport struct {
	base  http.RoundTripper
	token string
}

func (t *agentAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// RoundTrip must not modify the request it is given.
	if t.token != "" && r.Header.Get("Authorization") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// withAgentAuth wraps c so its requests carry the token. Returns c untouched
// when there is no token, keeping the no-auth deployment byte-identical.
//
// It also stops the client following redirects. net/http strips Authorization
// when a redirect crosses origins, but a header-adding RoundTripper runs on the
// redirected request too and puts it straight back -- so a compromised or
// spoofed agent could bounce the server at any host and harvest the token. No
// agent endpoint redirects, so refusing outright costs nothing.
func withAgentAuth(c *http.Client, token string) *http.Client {
	if token == "" {
		return c
	}
	c.Transport = &agentAuthTransport{base: c.Transport, token: token}
	c.CheckRedirect = refuseRedirect
	return c
}

// refuseRedirect surfaces the 3xx to the caller instead of following it.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
