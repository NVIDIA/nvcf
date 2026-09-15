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
	"errors"
	"fmt"
	"net/http"

	"github.com/mitchellh/mapstructure"
)

func (c *Sys) ListAuth() (map[string]*AuthMount, error) {
	return c.ListAuthWithContext(context.Background())
}

func (c *Sys) ListAuthWithContext(ctx context.Context) (map[string]*AuthMount, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodGet, "/v1/sys/auth")

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	secret, err := ParseSecret(resp.Body)
	if err != nil {
		return nil, err
	}
	if secret == nil || secret.Data == nil {
		return nil, errors.New("data from server response is empty")
	}

	mounts := map[string]*AuthMount{}
	err = mapstructure.Decode(secret.Data, &mounts)
	if err != nil {
		return nil, err
	}

	return mounts, nil
}

// DEPRECATED: Use EnableAuthWithOptions instead
func (c *Sys) EnableAuth(path, authType, desc string) error {
	return c.EnableAuthWithOptions(path, &EnableAuthOptions{
		Type:        authType,
		Description: desc,
	})
}

func (c *Sys) EnableAuthWithOptions(path string, options *EnableAuthOptions) error {
	return c.EnableAuthWithOptionsWithContext(context.Background(), path, options)
}

func (c *Sys) EnableAuthWithOptionsWithContext(ctx context.Context, path string, options *EnableAuthOptions) error {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodPost, fmt.Sprintf("/v1/sys/auth/%s", path))
	if err := r.SetJSONBody(options); err != nil {
		return err
	}

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func (c *Sys) DisableAuth(path string) error {
	return c.DisableAuthWithContext(context.Background(), path)
}

func (c *Sys) DisableAuthWithContext(ctx context.Context, path string) error {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/sys/auth/%s", path))

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err == nil {
		defer resp.Body.Close()
	}
	return err
}

// Rather than duplicate, we can use modern Go's type aliasing
type (
	EnableAuthOptions = MountInput
	AuthConfigInput   = MountConfigInput
	AuthMount         = MountOutput
	AuthConfigOutput  = MountConfigOutput
)
