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

package tracing

type Provider string

const (
	Jaeger    Provider = "jaeger"
	Lightstep Provider = "lightstep"
	OTLP      Provider = "otlp"
)

type TracingConfig struct {
	Enabled   bool
	Provider  Provider
	Https     bool
	Level     string
	Jaeger    JaegerConfig
	Lightstep LightstepConfig
	OTLP      OTLPConfig
}

type JaegerConfig struct {
	Endpoint string
}

type LightstepConfig struct {
	Endpoint string
	Token    string
}

type OTLPConfig struct {
	Endpoint string
}
