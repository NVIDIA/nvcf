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

package requestctx

import (
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
)

type RequestContext struct {
	RequestID        string
	APIKeyID         string // client auth subject; reserved for attribution, auditing, or future API-key policy hooks
	OrgID            string // auth-derived rate limit key; currently used to scope rate limiting
	ProjectID        string // used to further scope rate limiting when auth provides a project ID
	BearerToken      string
	RoutingKey       string
	Model            string
	RoutingMethod    string
	ModelSpecs       map[string]nvcf.ModelSpec
	TargetRegion     string
	SessionID        string
	SessionSource    string
	CacheAffinityKey string
	// Priority is the caller priority resolved by NVCF API at auth time, or nil
	// when no priority config applies. Lower value is higher priority, 0 is
	// highest. Forwarded to Stargate as the X-Priority header when set.
	Priority *uint32
}
