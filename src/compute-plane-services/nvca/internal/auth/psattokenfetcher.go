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

package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Ensure PSATTokenFetcher implements the TokenFetcher interface.
var _ TokenFetcher = &PSATTokenFetcher{}

// PSATTokenFetcher reads a Kubernetes projected service account token from a file.
// The token is auto-rotated by kubelet and re-read on each call.
type PSATTokenFetcher struct {
	tokenFilePath string
}

// NewPSATTokenFetcher creates a new PSATTokenFetcher that reads a PSAT JWT from the given file path.
func NewPSATTokenFetcher(tokenFilePath string) (*PSATTokenFetcher, error) {
	if tokenFilePath == "" {
		return nil, fmt.Errorf("token file path is required")
	}
	return &PSATTokenFetcher{tokenFilePath: tokenFilePath}, nil
}

// FetchToken reads the current projected SA token from disk.
// The token is re-read on every call so kubelet-rotated tokens are picked up automatically.
func (f *PSATTokenFetcher) FetchToken(_ context.Context) (string, error) {
	data, err := os.ReadFile(f.tokenFilePath)
	if err != nil {
		return "", fmt.Errorf("read PSAT token from %s: %w", f.tokenFilePath, err)
	}
	return strings.TrimSpace(string(data)), nil
}
