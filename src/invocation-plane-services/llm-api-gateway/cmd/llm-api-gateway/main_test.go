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

package main

import (
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
)

type fakeStarter struct {
	startAddr     string
	tlsAddr       string
	tlsCertFile   string
	tlsKeyFile    string
	tlsReload     time.Duration
	startCalls    int
	startTLSCalls int
}

func (f *fakeStarter) Start(address string) error {
	f.startCalls++
	f.startAddr = address
	return nil
}

func (f *fakeStarter) StartTLS(address, certFile, keyFile string, reloadInterval time.Duration) error {
	f.startTLSCalls++
	f.tlsAddr = address
	f.tlsCertFile = certFile
	f.tlsKeyFile = keyFile
	f.tlsReload = reloadInterval
	return nil
}

func TestGatewayStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		certFile string
		keyFile  string
		wantTLS  bool
	}{
		{name: "plaintext without tls files"},
		{name: "tls with both files", certFile: "/tls/tls.crt", keyFile: "/tls/tls.key", wantTLS: true},
		{name: "half pair still selects tls", certFile: "/tls/tls.crt", wantTLS: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			starter := &fakeStarter{}
			start := gatewayStart(starter, config.ServerConfig{
				TLSCertFile:       tc.certFile,
				TLSKeyFile:        tc.keyFile,
				TLSReloadInterval: 7 * time.Second,
			})
			if err := start(":8443"); err != nil {
				t.Fatalf("start() error = %v", err)
			}

			if !tc.wantTLS {
				if starter.startCalls != 1 || starter.startTLSCalls != 0 || starter.startAddr != ":8443" {
					t.Fatalf("starter = %+v, want one plaintext Start on :8443", starter)
				}
				return
			}
			if starter.startTLSCalls != 1 || starter.startCalls != 0 || starter.tlsAddr != ":8443" {
				t.Fatalf("starter = %+v, want one StartTLS on :8443", starter)
			}
			if starter.tlsCertFile != tc.certFile || starter.tlsKeyFile != tc.keyFile {
				t.Fatalf("tls files = %v, %v, want %q, %q", starter.tlsCertFile, starter.tlsKeyFile, tc.certFile, tc.keyFile)
			}
			if starter.tlsReload != 7*time.Second {
				t.Fatalf("tls reload interval = %s, want 7s", starter.tlsReload)
			}
		})
	}
}
