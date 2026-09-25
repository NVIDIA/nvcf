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

package proxy

import "testing"

func TestTCPTunnelDialHost(t *testing.T) {
	// The pod label is dropped on purpose: it does not resolve on the TCP side.
	// Pod targeting travels in the CONNECT authority, which the edge proxy
	// rewrites.
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "regional host with pod label",
			in:   "10-0-0-1.region-1.proxy.example.com",
			want: "region-1.tcp-proxy.example.com",
		},
		{
			name: "deeper domain",
			in:   "10-0-0-1.region-1.proxy.sub.example.com",
			want: "region-1.tcp-proxy.sub.example.com",
		},
		{
			name:    "service label is not proxy",
			in:      "10-0-0-1.region-1.something.example.com",
			wantErr: true,
		},
		{
			name:    "too few labels",
			in:      "proxy.local",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tcpTunnelDialHost(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("tcpTunnelDialHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTCPTunnelDialHostOverride(t *testing.T) {
	// The override exists so a test can point at an arbitrary endpoint without
	// depending on the naming convention holding.
	old := tcpTunnelHostOverride
	t.Cleanup(func() { tcpTunnelHostOverride = old })

	tcpTunnelHostOverride = "explicit.example.com"
	got, err := tcpTunnelDialHost("10-0-0-1.region-1.proxy.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "explicit.example.com" {
		t.Errorf("override ignored: got %q", got)
	}
}
