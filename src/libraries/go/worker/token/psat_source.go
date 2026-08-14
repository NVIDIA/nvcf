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

// Package token provides token caching and refresh utilities for worker clients.
package token

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// ErrNoMountedToken is returned by NewMountedJWTSource when NVCF_TOKEN_FILE_PATH
// is unset or the file does not exist. Callers should fall back to the bootstrap token.
var ErrNoMountedToken = errors.New("no mounted JWT token available")

// MountedJWTTokenSource reads a projected ServiceAccount Token (PSAT) from the path
// given by the NVCF_TOKEN_FILE_PATH environment variable. It implements oauth2.TokenSource.
//
// The file is re-read on every Token() call so that kubelet token rotation is
// picked up automatically without a process restart.
type MountedJWTTokenSource struct {
	path string
}

// NewMountedJWTSource returns a MountedJWTTokenSource for the file at NVCF_TOKEN_FILE_PATH,
// or ErrNoMountedToken if the env var is unset or the file does not exist.
func NewMountedJWTSource() (*MountedJWTTokenSource, error) {
	path := os.Getenv("NVCF_TOKEN_FILE_PATH")
	if path == "" {
		return nil, ErrNoMountedToken
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoMountedToken
	}
	return &MountedJWTTokenSource{path: path}, nil
}

// Token reads the mounted JWT and returns it as an oauth2.Token.
// The expiry is parsed from the JWT exp claim so the token source signals
// rotation at the right time.
func (s *MountedJWTTokenSource) Token() (*oauth2.Token, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read mounted JWT: %w", err)
	}
	jwtStr := strings.TrimSpace(string(raw))

	expiry, err := parseJWTExpiry(jwtStr)
	if err != nil {
		// Non-fatal: return the token without a meaningful expiry.
		// The NVCF API verifies the JWT independently.
		expiry = time.Now().Add(15 * time.Minute)
	}

	return &oauth2.Token{
		AccessToken: jwtStr,
		Expiry:      expiry,
	}, nil
}

// parseJWTExpiry extracts the exp claim from a JWT without verifying the signature.
func parseJWTExpiry(jwtStr string) (time.Time, error) {
	parts := strings.Split(jwtStr, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT: expected 3 parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal JWT claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT has no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}
