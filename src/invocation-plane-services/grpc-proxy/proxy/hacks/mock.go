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
package hacks

import (
	"context"
	"google.golang.org/grpc"
	"nvcf-grpc-proxy/nvcf/pb"
)

type MockedNVCFClient struct{}

func (m MockedNVCFClient) AuthStatefulWork(_ context.Context, in *pb.ProxyAuthRequest, _ ...grpc.CallOption) (*pb.ProxyAuthResponse, error) {
	var functionVersionId string
	if in.FunctionVersionId != nil {
		functionVersionId = *in.FunctionVersionId
	} else {
		functionVersionId = in.GetFunctionId()
	}
	return &pb.ProxyAuthResponse{
		FunctionId: in.FunctionId,
		FunctionVersions: []*pb.ProxyAuthResponse_FunctionVersion{{
			FunctionVersionId: functionVersionId,
			Type:              pb.ProxyAuthResponse_FunctionVersion_DEFAULT,
		}},
		ClientAuthSubject: "test-client-subject",
		ClientNcaId:       "test-nca-id",
	}, nil
}
