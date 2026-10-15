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

package worker

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/configs"
)

const testWorkerToken = "test-worker-token"

type mockNVCFServer struct {
	pb.UnimplementedWorkerServer
}

func (s *mockNVCFServer) ConnectOnce(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
	return &pb.WorkerConnectOnceResponse{
		NvcfWorkerToken: testWorkerToken,
		ConnectedRegion: "test-region",
		Expiration:      timestamppb.New(time.Now().Add(time.Hour)),
	}, nil
}

func startMockNVCFServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterWorkerServer(srv, &mockNVCFServer{})
	go srv.Serve(lis)
	t.Cleanup(srv.GracefulStop)
	return fmt.Sprintf("http://%s", lis.Addr().String())
}

func TestRun_WritesTokenToDisk(t *testing.T) {
	addr := startMockNVCFServer(t)
	tmpDir := t.TempDir()
	workerTokenPath := filepath.Join(tmpDir, "worker-token")

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   tmpDir,
		WorkerTokenPath:   workerTokenPath,
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	runErr := lo.Async(func() error {
		return w.Run(ctx)
	})

	// Wait for the token file to be written
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(workerTokenPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	content, err := os.ReadFile(workerTokenPath)
	if err != nil {
		t.Fatalf("token file not written: %v", err)
	}
	if string(content) != testWorkerToken {
		t.Fatalf("expected token %q, got %q", testWorkerToken, string(content))
	}
}
