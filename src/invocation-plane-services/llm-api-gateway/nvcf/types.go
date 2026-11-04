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

package nvcf

import (
	"maps"
	"slices"
)

type ModelSpec struct {
	URIs           []string
	TokenRateLimit string
	RoutingMethod  string
}

type InvocationAuthResponse struct {
	RoutingKey   string
	ClientAuthID string
	ProjectID    string
	AuthContext  map[string]string
	RateLimitKey string
	ModelSpecs   map[string]ModelSpec
	// Priority is the caller priority resolved by NVCF API, or nil when no
	// priority config applies. Lower value is higher priority, 0 is highest.
	Priority *uint32
}

// clone returns a copy that shares no mutable state with the receiver, so a
// cached response cannot be altered by one request in a way that leaks into
// another.
func (r *InvocationAuthResponse) clone() *InvocationAuthResponse {
	if r == nil {
		return nil
	}
	out := *r
	out.AuthContext = maps.Clone(r.AuthContext)
	if r.ModelSpecs != nil {
		out.ModelSpecs = make(map[string]ModelSpec, len(r.ModelSpecs))
		for name, spec := range r.ModelSpecs {
			spec.URIs = slices.Clone(spec.URIs)
			out.ModelSpecs[name] = spec
		}
	}
	if r.Priority != nil {
		priority := *r.Priority
		out.Priority = &priority
	}
	return &out
}

func deriveRateLimitKey(authContext map[string]string) string {
	if authContext == nil {
		return ""
	}
	return authContext["ncaId"]
}

func deriveProjectID(authContext map[string]string) string {
	if authContext == nil {
		return ""
	}

	for _, key := range []string{"projectId", "projectID", "project_id"} {
		if value := authContext[key]; value != "" {
			return value
		}
	}

	return ""
}
