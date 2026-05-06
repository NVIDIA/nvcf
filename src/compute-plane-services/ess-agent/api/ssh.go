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
	"fmt"
	"net/http"
)

// SSH is used to return a client to invoke operations on SSH backend.
type SSH struct {
	c          *Client
	MountPoint string
}

// SSH returns the client for logical-backend API calls.
func (c *Client) SSH() *SSH {
	return c.SSHWithMountPoint(SSHHelperDefaultMountPoint)
}

// SSHWithMountPoint returns the client with specific SSH mount point.
func (c *Client) SSHWithMountPoint(mountPoint string) *SSH {
	return &SSH{
		c:          c,
		MountPoint: mountPoint,
	}
}

// Credential wraps CredentialWithContext using context.Background.
func (c *SSH) Credential(role string, data map[string]interface{}) (*Secret, error) {
	return c.CredentialWithContext(context.Background(), role, data)
}

// CredentialWithContext invokes the SSH backend API to create a credential to establish an SSH session.
func (c *SSH) CredentialWithContext(ctx context.Context, role string, data map[string]interface{}) (*Secret, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodPut, fmt.Sprintf("/v1/%s/creds/%s", c.MountPoint, role))
	if err := r.SetJSONBody(data); err != nil {
		return nil, err
	}

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ParseSecret(resp.Body)
}

// SignKey wraps SignKeyWithContext using context.Background.
func (c *SSH) SignKey(role string, data map[string]interface{}) (*Secret, error) {
	return c.SignKeyWithContext(context.Background(), role, data)
}

// SignKeyWithContext signs the given public key and returns a signed public key to pass
// along with the SSH request.
func (c *SSH) SignKeyWithContext(ctx context.Context, role string, data map[string]interface{}) (*Secret, error) {
	ctx, cancelFunc := c.c.withConfiguredTimeout(ctx)
	defer cancelFunc()

	r := c.c.NewRequest(http.MethodPut, fmt.Sprintf("/v1/%s/sign/%s", c.MountPoint, role))
	if err := r.SetJSONBody(data); err != nil {
		return nil, err
	}

	resp, err := c.c.rawRequestWithContext(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ParseSecret(resp.Body)
}
