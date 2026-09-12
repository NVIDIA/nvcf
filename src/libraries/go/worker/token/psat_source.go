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
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// MountedTokenPathEnvKey is the environment variable that may override the mounted JWT path.
const MountedTokenPathEnvKey = "NVCF_TOKEN_FILE_PATH"

// DefaultMountedTokenPath is where the cluster agent projects the worker ServiceAccount token.
const DefaultMountedTokenPath = "/var/run/secrets/tokens/token"

// MountedTokenRoot is the only directory a mounted JWT may be read from. An override of
// MountedTokenPathEnvKey that resolves outside this directory is ignored. Tests may replace it.
var MountedTokenRoot = "/var/run/secrets/tokens/"

// ErrNoMountedToken is returned by NewMountedJWTSource when no usable mounted JWT exists:
// the file is missing, is not a regular file under MountedTokenRoot, or does not contain a JWS.
// Callers should fall back to the bootstrap token.
var ErrNoMountedToken = errors.New("no mounted JWT token available")

// MountedJWTTokenSource reads a projected ServiceAccount Token (PSAT) from a fixed mount
// path. It implements oauth2.TokenSource.
//
// The file is re-read on every Token() call so that kubelet token rotation is
// picked up automatically without a process restart.
type MountedJWTTokenSource struct {
	path string
}

// NewMountedJWTSource returns a MountedJWTTokenSource for the mounted JWT, or
// ErrNoMountedToken when none is usable. Any other error is an I/O failure on an
// otherwise valid path and should not be treated as "no token mounted".
func NewMountedJWTSource() (*MountedJWTTokenSource, error) {
	path, err := resolveMountedTokenPath(os.Getenv(MountedTokenPathEnvKey))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoMountedToken
		}
		return nil, fmt.Errorf("stat mounted JWT: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNoMountedToken
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mounted JWT: %w", err)
	}
	if _, err := parseJWTExpiry(strings.TrimSpace(string(raw))); err != nil {
		zap.L().Warn("mounted token file does not contain a JWT; treating as not mounted",
			zap.String("path", path), zap.Error(err))
		return nil, ErrNoMountedToken
	}
	return &MountedJWTTokenSource{path: path}, nil
}

// resolveMountedTokenPath returns the path to read, enforcing that any override stays
// within MountedTokenRoot after symlink resolution.
func resolveMountedTokenPath(override string) (string, error) {
	if override == "" {
		return DefaultMountedTokenPath, nil
	}
	cleaned := filepath.Clean(override)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoMountedToken
		}
		return "", fmt.Errorf("resolve mounted JWT path: %w", err)
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(MountedTokenRoot))
	if err != nil {
		root = filepath.Clean(MountedTokenRoot)
	}
	if !strings.HasPrefix(resolved, strings.TrimSuffix(root, "/")+"/") {
		zap.L().Warn("ignoring mounted token path outside the allowed directory",
			zap.String("path", override), zap.String("allowedRoot", MountedTokenRoot))
		return "", ErrNoMountedToken
	}
	return resolved, nil
}

// Token reads the mounted JWT and returns it as an oauth2.Token.
// The expiry is parsed from the JWT exp claim so the token source signals
// rotation at the right time. Content that is not a JWT is an error.
func (s *MountedJWTTokenSource) Token() (*oauth2.Token, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read mounted JWT: %w", err)
	}
	jwtStr := strings.TrimSpace(string(raw))

	expiry, err := parseJWTExpiry(jwtStr)
	if err != nil {
		return nil, fmt.Errorf("mounted JWT is not a valid token: %w", err)
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
