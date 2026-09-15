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
	"sync/atomic"
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
	requestSecretCredentials func(context.Context, *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error)
}

func (s *mockNVCFServer) ConnectOnce(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
	return &pb.WorkerConnectOnceResponse{
		NvcfWorkerToken: testWorkerToken,
		ConnectedRegion: "test-region",
		Expiration:      timestamppb.New(time.Now().Add(time.Hour)),
	}, nil
}

func (s *mockNVCFServer) RequestSecretCredentials(
	ctx context.Context,
	req *pb.SecretCredentialsRequest,
) (*pb.SecretCredentialsResponse, error) {
	if s.requestSecretCredentials == nil {
		return s.UnimplementedWorkerServer.RequestSecretCredentials(ctx, req)
	}
	return s.requestSecretCredentials(ctx, req)
}

func startMockNVCFServer(t *testing.T) string {
	return startMockNVCFServerWithImplementation(t, &mockNVCFServer{})
}

func startMockNVCFServerWithImplementation(t *testing.T, implementation *mockNVCFServer) string {
	t.Helper()
	var listenConfig net.ListenConfig
	lis, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterWorkerServer(srv, implementation)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("serve mock NVCF server: %v", err)
		}
	})
	return fmt.Sprintf("http://%s", lis.Addr().String())
}

func waitForFileContent(path, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && string(content) == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	content, err := os.ReadFile(path)
	return err == nil && string(content) == want
}

func cancelAndWaitForRun(t *testing.T, cancel context.CancelFunc, runErr <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Log("Run did not stop within the cleanup timeout")
	}
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

func TestRun_RefreshesESSAssertionTokenUntilCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	assertionTokenPath := filepath.Join(tmpDir, "ess", "jwt.token")
	if err := os.MkdirAll(filepath.Dir(assertionTokenPath), 0755); err != nil {
		t.Fatalf("create assertion token directory: %v", err)
	}
	if err := os.WriteFile(assertionTokenPath, []byte("worker-init-token"), 0600); err != nil {
		t.Fatalf("write initial assertion token: %v", err)
	}

	var requestCount atomic.Int32
	addr := startMockNVCFServerWithImplementation(t, &mockNVCFServer{
		requestSecretCredentials: func(
			_ context.Context,
			_ *pb.SecretCredentialsRequest,
		) (*pb.SecretCredentialsResponse, error) {
			call := requestCount.Add(1)
			tokenValue := "refreshed-assertion-1"
			expiration := time.Now().Add(300 * time.Millisecond)
			if call > 1 {
				tokenValue = "refreshed-assertion-2"
				expiration = time.Now().Add(time.Hour)
			}
			return &pb.SecretCredentialsResponse{
				SecretCredentialsToken: tokenValue,
				Expiration:             timestamppb.New(expiration),
			}, nil
		},
	})

	cfg := configs.Config{
		NvcfFqdnGrpc:          addr,
		NvcfWorkerToken:       "initial-token",
		FunctionId:            "test-function-id",
		FunctionVersionId:     "test-function-version-id",
		NcaId:                 "test-nca-id",
		InstanceId:            "test-instance-id",
		SharedConfigDir:       tmpDir,
		WorkerTokenPath:       filepath.Join(tmpDir, "worker-token"),
		ESSAssertionTokenPath: assertionTokenPath,
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := lo.Async(func() error { return w.Run(ctx) })

	if !waitForFileContent(assertionTokenPath, "refreshed-assertion-1", 5*time.Second) {
		cancelAndWaitForRun(t, cancel, runErr)
		t.Fatal("existing assertion token was not replaced by the immediate refresh")
	}
	info, err := os.Stat(assertionTokenPath)
	if err != nil {
		cancelAndWaitForRun(t, cancel, runErr)
		t.Fatalf("stat refreshed assertion token: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		cancelAndWaitForRun(t, cancel, runErr)
		t.Fatalf("assertion token mode = %o, want 0644", got)
	}
	if !waitForFileContent(assertionTokenPath, "refreshed-assertion-2", 5*time.Second) {
		cancelAndWaitForRun(t, cancel, runErr)
		t.Fatal("assertion token was not rotated on the next refresh cycle")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop cleanly after cancellation")
	}
}

func TestRun_WithoutESSAssertionTokenPathSkipsSecretCredentialRefresh(t *testing.T) {
	var requestCount atomic.Int32
	addr := startMockNVCFServerWithImplementation(t, &mockNVCFServer{
		requestSecretCredentials: func(
			_ context.Context,
			_ *pb.SecretCredentialsRequest,
		) (*pb.SecretCredentialsResponse, error) {
			requestCount.Add(1)
			return &pb.SecretCredentialsResponse{
				SecretCredentialsToken: "unexpected-token",
				Expiration:             timestamppb.New(time.Now().Add(time.Hour)),
			}, nil
		},
	})

	tmpDir := t.TempDir()
	assertionTokenPath := filepath.Join(tmpDir, "ess", "jwt.token")
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
	runErr := lo.Async(func() error { return w.Run(ctx) })
	if !waitForFileContent(workerTokenPath, testWorkerToken, 5*time.Second) {
		cancelAndWaitForRun(t, cancel, runErr)
		t.Fatal("worker token was not written")
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("RequestSecretCredentials calls = %d, want 0", got)
	}
	if _, err := os.Stat(assertionTokenPath); !os.IsNotExist(err) {
		t.Fatalf("assertion token file should not exist without ESS_ASSERTION_TOKEN_PATH, stat error: %v", err)
	}
}
