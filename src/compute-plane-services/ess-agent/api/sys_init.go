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

package api

import (
	"context"
	"net/http"
)

func (c *Sys) InitStatus() (bool, error) {
	return c.InitStatusWithContext(context.Background())
}

func (c *Sys) InitStatusWithContext(ctx context.Context) (bool, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodGet, "/v1/sys/init")

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result InitStatusResponse
	err = resp.DecodeJSON(&result)
	return result.Initialized, err
}

func (c *Sys) Init(opts *InitRequest) (*InitResponse, error) {
	return c.InitWithContext(context.Background(), opts)
}

func (c *Sys) InitWithContext(ctx context.Context, opts *InitRequest) (*InitResponse, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodPut, "/v1/sys/init")
	if err := r.SetJSONBody(opts); err != nil {
		return nil, err
	}

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result InitResponse
	err = resp.DecodeJSON(&result)
	return &result, err
}

type InitRequest struct {
	SecretShares      int      `json:"secret_shares"`
	SecretThreshold   int      `json:"secret_threshold"`
	StoredShares      int      `json:"stored_shares"`
	PGPKeys           []string `json:"pgp_keys"`
	RecoveryShares    int      `json:"recovery_shares"`
	RecoveryThreshold int      `json:"recovery_threshold"`
	RecoveryPGPKeys   []string `json:"recovery_pgp_keys"`
	RootTokenPGPKey   string   `json:"root_token_pgp_key"`
}

type InitStatusResponse struct {
	Initialized bool
}

type InitResponse struct {
	Keys            []string `json:"keys"`
	KeysB64         []string `json:"keys_base64"`
	RecoveryKeys    []string `json:"recovery_keys"`
	RecoveryKeysB64 []string `json:"recovery_keys_base64"`
	RootToken       string   `json:"root_token"`
}
