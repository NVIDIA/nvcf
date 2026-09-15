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
	"errors"
	"net/rpc"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/errutil"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/sdk/plugin/pb"
)

// pathInternal is used to test viewing internal backend values. In this case,
// it is used to test the invalidate func.
func errorPaths(b *backend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "errors/rpc",
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ReadOperation: b.pathErrorRPCRead,
			},
		},
		{
			Pattern: "errors/kill",
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ReadOperation: b.pathErrorRPCRead,
			},
		},
		{
			Pattern: "errors/type",
			Fields: map[string]*framework.FieldSchema{
				"err_type": {Type: framework.TypeInt},
			},
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.CreateOperation: b.pathErrorRPCRead,
				logical.UpdateOperation: b.pathErrorRPCRead,
			},
		},
	}
}

func (b *backend) pathErrorRPCRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	errTypeRaw, ok := data.GetOk("err_type")
	if !ok {
		return nil, rpc.ErrShutdown
	}

	var err error
	switch uint32(errTypeRaw.(int)) {
	case pb.ErrTypeUnknown:
		err = errors.New("test")
	case pb.ErrTypeUserError:
		err = errutil.UserError{Err: "test"}
	case pb.ErrTypeInternalError:
		err = errutil.InternalError{Err: "test"}
	case pb.ErrTypeCodedError:
		err = logical.CodedError(403, "test")
	case pb.ErrTypeStatusBadRequest:
		err = &logical.StatusBadRequest{Err: "test"}
	case pb.ErrTypeUnsupportedOperation:
		err = logical.ErrUnsupportedOperation
	case pb.ErrTypeUnsupportedPath:
		err = logical.ErrUnsupportedPath
	case pb.ErrTypeInvalidRequest:
		err = logical.ErrInvalidRequest
	case pb.ErrTypePermissionDenied:
		err = logical.ErrPermissionDenied
	case pb.ErrTypeMultiAuthzPending:
		err = logical.ErrMultiAuthzPending
	}

	return nil, err
}
