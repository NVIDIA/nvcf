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

// Package api holds the GDS API contract: the protobuf sources under proto/,
// the generated Go and OpenAPI code under gen/, the well-known Kubernetes
// label and annotation keys, and the derived Service name of an import.
package api

// buf generate runs from the module root, where buf.yaml and buf.gen.yaml
// live; hack/proto.sh also lints the sources and adds license headers.
//go:generate sh -c "cd .. && PATH=\"$(go env GOPATH)/bin:$PATH\" hack/proto.sh"
