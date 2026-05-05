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

package authorizers

import (
	"errors"
	"net/http"
	"strings"
)

// ErrNoTokenInRequest indicates the Authorization header is missing or not a Bearer token.
var ErrNoTokenInRequest = errors.New("no token in request")

// BearerTokenFromRequest returns the bearer token from the Authorization header.
func BearerTokenFromRequest(r *http.Request) (string, error) {
	return ExtractBearerToken(r.Header.Get("Authorization"))
}

// ExtractBearerToken parses "Bearer <token>" from the Authorization header value.
// Mirrors github.com/golang-jwt/jwt/v5/request.BearerExtractor: the "Bearer"
// scheme is matched case-insensitively per the robustness principle.
func ExtractBearerToken(authHeader string) (string, error) {
	if len(authHeader) < 7 || !strings.EqualFold(authHeader[:7], "bearer ") {
		return "", ErrNoTokenInRequest
	}
	return authHeader[7:], nil
}
