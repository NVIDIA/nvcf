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

package mock

import (
	"context"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

// pathInternal is used to test viewing internal backend values. In this case,
// it is used to test the invalidate func.
func pathInternal(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "internal",
		Fields: map[string]*framework.FieldSchema{
			"value": {Type: framework.TypeString},
		},
		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.UpdateOperation: b.pathInternalUpdate,
			logical.ReadOperation:   b.pathInternalRead,
		},
	}
}

func (b *backend) pathInternalUpdate(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	value := data.Get("value").(string)
	b.internal = value
	// Return the secret
	return nil, nil
}

func (b *backend) pathInternalRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	// Return the secret
	return &logical.Response{
		Data: map[string]interface{}{
			"value": b.internal,
		},
	}, nil
}
