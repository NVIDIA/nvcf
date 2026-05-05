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
//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ./echo.proto
package echo

import (
	"context"
	"io"
)

type Server struct {
	UnimplementedEchoServer
}

func (s *Server) EchoMessage(ctx context.Context, request *EchoRequest) (*EchoReply, error) {
	return &EchoReply{Message: request.Message}, nil
}

func (s *Server) EchoMessageStreaming(server Echo_EchoMessageStreamingServer) error {
	for {
		request, err := server.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		err = server.Send(&EchoReply{Message: request.Message})
		if err != nil {
			return err
		}
	}
}
