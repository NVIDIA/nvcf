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
package echo

import (
	"context"
	"crypto/x509"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"io"
	"os"
	"testing"
	"time"
)

func TestRealGrpcProxyGrpc(t *testing.T) {
	t.SkipNow() // comment me to test locally.

	logger := lo.Must(zap.NewDevelopment())
	zap.ReplaceGlobals(logger)

	ctx := context.Background()
	functionId := "d0cd976c-f708-4ca8-89ee-80a8a43bf5bb"
	apiKey := os.Getenv("API_KEY")
	pool, _ := x509.SystemCertPool()
	creds := credentials.NewClientTLSFromCert(pool, "")
	conn, err := grpc.NewClient("stg.grpc.nvcf.nvidia.com:443", grpc.WithTransportCredentials(creds))
	if err != nil {
		panic(err)
	}
	newEchoClient := NewEchoClient(conn)
	ctx = metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+apiKey,
		"function-id", functionId,
	)
	for i := 0; i < 10; i++ {
		message, err := newEchoClient.EchoMessage(ctx, &EchoRequest{Message: "unary"})
		if err != nil {
			panic(err)
		}
		zap.L().Info("received", zap.Stringer("response", message))
	}

	echoMessageStreaming, err := newEchoClient.EchoMessageStreaming(ctx)
	if err != nil {
		panic(err)
	}

	read := lo.Async(func() error {
		for {
			zap.L().Info("receiving message")
			response, err := echoMessageStreaming.Recv()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			zap.L().Info("received streaming", zap.Stringer("response", response))
		}
	})
	//for i := 0; i < 10; i++ {
	zap.L().Info("sending message")
	err = echoMessageStreaming.Send(&EchoRequest{Message: "delayed echo message", Delay: lo.ToPtr(float32(35)), Repeats: lo.ToPtr(int32(10))})
	if err != nil {
		panic(err)
	}
	//}

	zap.L().Info("closing send")
	err = echoMessageStreaming.CloseSend()
	if err != nil {
		panic(err)
	}

	// 4 req resp
	// 1 req -> streaming resp for the rest of the conn
	// 20 req resp, sleeping between
	for i := 0; i < 19; i++ {
		message, err := newEchoClient.EchoMessage(ctx, &EchoRequest{Message: "unary"})
		if err != nil {
			panic(err)
		}
		zap.L().Info("received", zap.Stringer("response", message))
		time.Sleep(15 * time.Second)
	}

	err = <-read
	if err != nil {
		panic(err)
	}
}
