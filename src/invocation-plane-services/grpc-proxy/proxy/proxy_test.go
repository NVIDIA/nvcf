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
package proxy

import "testing"

func TestTracingEnabledCondition(t *testing.T) {
	tests := []struct {
		name                     string
		otelExporterOTLPEndpoint string
		wantEnabled              bool
	}{
		{
			name:                     "tracing enabled when endpoint is set",
			otelExporterOTLPEndpoint: "http://otel-collector:4317",
			wantEnabled:              true,
		},
		{
			name:                     "tracing disabled when endpoint is empty",
			otelExporterOTLPEndpoint: "",
			wantEnabled:              false,
		},
		{
			name:                     "tracing enabled with https endpoint",
			otelExporterOTLPEndpoint: "https://otel-collector:4317",
			wantEnabled:              true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{OTELExporterOTLPEndpoint: tt.otelExporterOTLPEndpoint}
			// This mirrors the condition used in NewNVCFProxy for setting Tracing.Enabled
			enabled := config.OTELExporterOTLPEndpoint != ""
			if enabled != tt.wantEnabled {
				t.Errorf("tracing enabled = %v, want %v", enabled, tt.wantEnabled)
			}
		})
	}
}

func Test_getProxyPath(t *testing.T) {
	tests := []struct {
		name    string
		args    Config
		want    string
		wantErr bool
	}{
		{
			name:    "correct ip",
			args:    Config{PodIP: "1.2.3.4", SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com", EnableHTTP3Connect: true},
			want:    "https://1-2-3-4.proxy.grpc.nvcf.nvidia.com/v1/proxy",
			wantErr: false,
		},
		{
			name:    "no ip",
			args:    Config{SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com", EnableHTTP3Connect: true},
			want:    "https://proxy.grpc.nvcf.nvidia.com/v1/proxy",
			wantErr: false,
		},
		{
			name:    "ipv6",
			args:    Config{PodIP: "::1", SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com", EnableHTTP3Connect: true},
			want:    "",
			wantErr: true,
		},
		{
			name:    "bad ip",
			args:    Config{PodIP: "not an ip", SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com", EnableHTTP3Connect: true},
			want:    "",
			wantErr: true,
		},
		{
			name:    "http3 disabled",
			args:    Config{PodIP: "1.2.3.4", SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com", EnableHTTP1Connect: true},
			want:    "",
			wantErr: false,
		},
		{
			name:    "at least one connect path",
			args:    Config{PodIP: "1.2.3.4", SelfWorkerFqdn: "https://proxy.grpc.nvcf.nvidia.com"},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getConnectPaths(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("getProxyPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got.HTTP3 != tt.want {
				t.Errorf("getProxyPath() got = %v, want %v", got, tt.want)
			}
		})
	}
}
