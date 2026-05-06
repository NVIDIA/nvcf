// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package authorizers defines the pluggable authorization model for ReVal (POR §5.1).
package authorizers

import (
	"context"
	"net/http"
)

// AuthzInput is the immutable per-request slice of the authorization context (HTTP surface).
type AuthzInput struct {
	Method string
	Path   string
	// BearerToken is the raw token from the Authorization header (without "Bearer ").
	BearerToken string
}

// AuthzContext carries request authorization state across a chain of Authorizers.
// Extra holds free-form fields (e.g. subject, scopes) populated by earlier steps.
type AuthzContext struct {
	Request *http.Request
	Input   AuthzInput
	Extra   map[string]any
}

// AuthzResult is the outcome of a single authorization evaluation step.
type AuthzResult struct {
	Allow bool
}

// Authorizer is the POR contract: pluggable policy / verification steps.
// Implementations may read Input and Request and update Extra for downstream authorizers.
type Authorizer interface {
	Evaluate(ctx context.Context, ac *AuthzContext) (AuthzResult, error)
}
