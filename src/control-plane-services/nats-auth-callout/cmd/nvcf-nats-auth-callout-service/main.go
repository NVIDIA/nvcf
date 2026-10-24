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

// Package main nvcf-nats-auth-callout-service Service API Documentation
//
//	@title						nvcf-nats-auth-callout-service Service API
//	@version					1.0
//	@description				This is a simple nvcf-nats-auth-callout-service service API built with Gin framework, nSpectId: NSPECT-QG33-Y8G6
//	@termsOfService				http://swagger.io/terms/
//
//	@contact.name				API Support
//	@contact.url				http://www.swagger.io/support
//	@contact.email				support@swagger.io
//
//	@license.name				Apache 2.0
//	@license.url				http://www.apache.org/licenses/LICENSE-2.0.html
//
//	@host						localhost:8080
//	@BasePath					/
//
//	@schemes					http
//
//	@securityDefinitions.basic	BasicAuth
//
//	@externalDocs.description	OpenAPI
//	@externalDocs.url			https://swagger.io/resources/open-api/
package main

import (
	_ "embed"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/cmd/nvcf-nats-auth-callout-service/cli"
)

//go:embed config.yaml
var defaultConfigYAML []byte

func main() {
	// Set the embedded config for the config package
	cli.SetEmbeddedConfig(defaultConfigYAML)

	if err := cli.Execute(); err != nil {
		panic(err)
	}
}
