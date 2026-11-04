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

package otelconfig

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

var (
	_ json.Unmarshaler = (*Provider)(nil)
	_ json.Unmarshaler = (*Protocol)(nil)
)

type Provider string

const (
	ProviderSplunk       Provider = "SPLUNK"
	ProviderGrafana      Provider = "GRAFANA_CLOUD"
	ProviderServiceNow   Provider = "SERVICENOW"
	ProviderThanos       Provider = "KRATOS_THANOS"
	ProviderPrometheus   Provider = "PROMETHEUS"
	ProviderDatadog      Provider = "DATADOG"
	ProviderKratosLogs   Provider = "KRATOS"
	ProviderAzureMonitor Provider = "AZURE_MONITOR"
)

// Exporter type constants
const (
	exporterTypeOTLPHTTP     = "otlphttp"
	exporterTypeBasicAuth    = "basicauth"
	exporterTypeDatadog      = "datadog"
	exporterTypeAzureMonitor = "azuremonitor"
)

func (t *Provider) UnmarshalJSON(data []byte) error {
	ds := string(data)
	s, err := strconv.Unquote(ds)
	if err != nil {
		s = ds
	}
	switch s {
	case string(ProviderSplunk),
		string(ProviderGrafana),
		string(ProviderServiceNow),
		string(ProviderThanos),
		string(ProviderPrometheus),
		string(ProviderDatadog),
		string(ProviderKratosLogs),
		string(ProviderAzureMonitor):
		*t = Provider(s)
	default:
		return fmt.Errorf("unknown provider: %s", s)
	}
	return nil
}

type Protocol string

const (
	ProtocolHTTP Protocol = "http"
	ProtocolGRPC Protocol = "grpc"
)

func (t *Protocol) UnmarshalJSON(data []byte) error {
	ds := string(data)
	s, err := strconv.Unquote(ds)
	if err != nil {
		s = ds
	}
	s = strings.ToLower(s)
	switch s {
	case string(ProtocolHTTP),
		string(ProtocolGRPC):
		*t = Protocol(s)
	default:
		return fmt.Errorf("unknown protocol: %s", s)
	}
	return nil
}
