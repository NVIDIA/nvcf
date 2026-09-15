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

package framework

import (
	"context"

	"github.com/hashicorp/vault/sdk/logical"
	"github.com/ryanuber/go-glob"
)

// GlobListFilter wraps an OperationFunc with an optional filter which excludes listed entries
// which don't match a glob style pattern
func GlobListFilter(fieldName string, callback OperationFunc) OperationFunc {
	return func(ctx context.Context, req *logical.Request, data *FieldData) (*logical.Response, error) {
		resp, err := callback(ctx, req, data)
		if err != nil {
			return nil, err
		}

		if keys, ok := resp.Data["keys"]; ok {
			if entries, ok := keys.([]string); ok {
				filter, ok := data.GetOk(fieldName)
				if ok && filter != "" && filter != "*" {
					var filteredEntries []string
					for _, e := range entries {
						if glob.Glob(filter.(string), e) {
							filteredEntries = append(filteredEntries, e)
						}
					}
					resp.Data["keys"] = filteredEntries
				}
			}
		}
		return resp, nil
	}
}
