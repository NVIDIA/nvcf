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

package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/configs"
)

// testWorkerToken is the token the mock NVCF server hands back. Matching the
// helper in internal/worker/worker_test.go.
const testWorkerToken = "test-worker-token"

// mockNVCFServer is a minimal NVCF gRPC server that only implements the
// ConnectOnce RPC the worker needs to bootstrap. Copied from the worker package
// test helper because that type lives in package worker and is not importable.
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

// startMockNVCFServer starts the mock NVCF server on an ephemeral loopback port
// and returns the "http://127.0.0.1:<port>" address the worker dials.
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

// setWorkerEnv populates the environment with a valid worker configuration that
// points NVCF_FQDN_GRPC at the supplied mock address. It returns the worker
// token path so callers can poll for proof the worker is running.
func setWorkerEnv(t *testing.T, addr string) string {
	t.Helper()
	tmpDir := t.TempDir()
	workerTokenPath := filepath.Join(tmpDir, "worker-token")

	t.Setenv("NVCF_FQDN_GRPC", addr)
	t.Setenv("NVCF_WORKER_TOKEN", "initial-token")
	t.Setenv("FUNCTION_ID", "test-function-id")
	t.Setenv("FUNCTION_VERSION_ID", "test-function-version-id")
	t.Setenv("NCA_ID", "test-nca-id")
	t.Setenv("INSTANCE_ID", "test-instance-id")
	t.Setenv("SHARED_CONFIG_DIR", tmpDir)
	t.Setenv("WORKER_TOKEN_PATH", workerTokenPath)

	return workerTokenPath
}

// waitForFile polls until path exists or the deadline elapses. It returns true
// if the file appeared. No fixed sleep is used to gate progress; the poll loop
// is bounded by the deadline.
func waitForFile(path string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func TestNewRootCommand_Structure(t *testing.T) {
	cmd := NewRootCommand(context.Background())
	if cmd == nil {
		t.Fatal("NewRootCommand returned nil")
	}

	if cmd.Use != "llm-credentials" {
		t.Errorf("Use = %q, want %q", cmd.Use, "llm-credentials")
	}

	// Persistent "config" flag must exist.
	if cmd.PersistentFlags().Lookup("config") == nil {
		t.Error("expected a persistent --config flag")
	}

	// One flag per Config mapstructure tag.
	configType := reflect.TypeOf(configs.Config{})
	for i := 0; i < configType.NumField(); i++ {
		envName := configType.Field(i).Tag.Get("mapstructure")
		if envName == "" {
			t.Fatalf("field %d missing mapstructure tag", i)
		}
		if cmd.Flags().Lookup(envName) == nil {
			t.Errorf("expected a flag for config tag %q", envName)
		}
	}
}

func TestPersistentPreRunE_Success(t *testing.T) {
	addr := startMockNVCFServer(t)
	setWorkerEnv(t, addr)

	cmd := NewRootCommand(context.Background())
	if err := cmd.PersistentPreRunE(cmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
}

func TestPersistentPreRunE_InitConfigError(t *testing.T) {
	tmpDir := t.TempDir()
	badConfig := filepath.Join(tmpDir, "config.yaml")
	// Malformed YAML so viper.ReadInConfig fails with a parse error (not a
	// ConfigFileNotFoundError), forcing config.InitConfig to return an error.
	if err := os.WriteFile(badConfig, []byte("::: not: valid: yaml: ["), 0600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	cmd := NewRootCommand(context.Background())
	if err := cmd.PersistentFlags().Set("config", badConfig); err != nil {
		t.Fatalf("set config flag: %v", err)
	}

	if err := cmd.PersistentPreRunE(cmd, nil); err == nil {
		t.Fatal("expected PersistentPreRunE to fail on malformed config, got nil")
	}
}

func TestRunE_GracefulShutdownOnSignal(t *testing.T) {
	addr := startMockNVCFServer(t)
	workerTokenPath := setWorkerEnv(t, addr)

	cmd := NewRootCommand(context.Background())
	cmd.SetArgs([]string{})

	// Note: this test does not call signal.Notify itself; the SIGTERM handler
	// is registered (and unregistered) entirely inside RunE in service.go.
	// There is no test-owned signalChan to signal.Stop here.
	execErr := make(chan error, 1)
	go func() {
		// Execute runs PersistentPreRunE (config + worker.New) then RunE, which
		// connects to the mock, starts the token refresher, registers the
		// signal handler, and blocks on ctx.Done().
		execErr <- cmd.Execute()
	}()

	// Only signal AFTER the worker is up. The token file is written by the
	// refresher once the worker is running, which also means signal.Notify is
	// already registered, so the default SIGTERM disposition cannot kill us.
	if !waitForFile(workerTokenPath, time.Now().Add(10*time.Second)) {
		t.Fatal("worker token file was never written; worker did not start")
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case err := <-execErr:
		if err != nil {
			t.Fatalf("Execute should return nil after graceful shutdown, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return after SIGTERM within deadline")
	}
}

// TestRun_GracefulShutdown exercises the package-level Run entrypoint on its
// success path: it builds the production logger, runs NewRootCommand().Execute()
// against the mock NVCF server, and returns cleanly once SIGTERM cancels the
// worker context. The error/panic branch (utils.ExitReason + zap panic) is not
// exercised because graceful shutdown returns a nil (or "received signal
// interrupt") error.
func TestRun_GracefulShutdown(t *testing.T) {
	addr := startMockNVCFServer(t)
	workerTokenPath := setWorkerEnv(t, addr)

	// Note: this test does not call signal.Notify itself; the SIGTERM handler
	// is registered (and unregistered) entirely inside RunE in service.go.
	// There is no test-owned signalChan to signal.Stop here.
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run()
	}()

	// Wait until the worker is up (token file written) before signaling, so the
	// signal handler RunE registered is already in place.
	if !waitForFile(workerTokenPath, time.Now().Add(10*time.Second)) {
		t.Fatal("worker token file was never written; worker did not start")
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGTERM within deadline")
	}
}
