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

package nvct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"

	"golang.org/x/oauth2"
)

const (
	taskId       = "mockTaskId"
	instanceId   = "mockInstanceId"
	instanceType = "mockInstanceType"
	nvctFQDN     = "http://localhost:9091"
)

var ctx context.Context

// ------------------------------------------------------------------------

// Entry of all tests
func TestMain(m *testing.M) {
	ctx = context.Background()

	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.InfoLevel))
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	zap.L().Info("======== NVCT Client Tests ========")
	mockServer := testutils.NewMockNvctServer(taskId, instanceId, instanceType, "", 1*time.Second)
	if err := mockServer.Run("0.0.0.0:9091"); err != nil {
		zap.L().Fatal("failed to start mock nvct server")
	}
	defer mockServer.Shutdown()

	exitCode := m.Run()

	zap.L().Info("======== NVCT Client Tests Complete ========")
	zapLogger.Close()

	os.Exit(exitCode)
}

// ------------------------------------------------------------------------

func TestConnect(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err = mockClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
}

// ------------------------------------------------------------------------

func TestRefreshToken(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Add(-90 * time.Minute).Round(time.Second).Unix())
	if err != nil {
		t.Fatal(err)
	}
	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	mockClient.StartWorkerTokenRefresher(ctx, false)

	time.Sleep(2 * time.Second)

	newToken, err := mockClient.NvctTokenProvider.Token()
	if err != nil {
		t.Fatal(err)
	}

	newTokenString := newToken.AccessToken
	if newTokenString == tokenString || newTokenString == "" {
		t.Fatalf("Invalid new token: %s\n", newTokenString)
	}

	tokenString = newTokenString

	time.Sleep(2 * time.Second)

	newToken, err = mockClient.NvctTokenProvider.Token()
	if err != nil {
		t.Fatal(err)
	}

	newTokenString = newToken.AccessToken
	if newTokenString == tokenString || newTokenString == "" {
		t.Fatalf("Invalid new token: %s\n", newTokenString)
	}
}

// ------------------------------------------------------------------------

func TestSendHeartbeat(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err = mockClient.SendInProgressHeartbeat(ctx, pb.ExecutionStatus_RUNNING); err != nil {
		t.Fatal(err)
	}

	if err = mockClient.SendErrorHeartbeat(ctx, "task execution timeout"); err != nil {
		t.Fatal(err)
	}

	if err = mockClient.SendWorkerTerminatedHeartbeat(ctx); err != nil {
		t.Fatal(err)
	}
}

// ------------------------------------------------------------------------

func TestSendResultMetadata(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	mockStreamingClient := NewStreamingClient(
		ctx,
		mockClient.Client,
		mockClient.NvctTokenProvider,
	)
	defer func() {
		if err := mockStreamingClient.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	var progress uint32
	execStatus := pb.ExecutionStatus_IN_PROGRESS
	for execStatus != pb.ExecutionStatus_COMPLETED {
		if progress == 100 {
			execStatus = pb.ExecutionStatus_COMPLETED
		}
		result := &pb.ResultMetadataRequest{
			TaskId:          taskId,
			InstanceId:      instanceId,
			InstanceType:    instanceType,
			Status:          execStatus,
			PercentComplete: &progress,
		}

		if err := mockStreamingClient.SendResult(result); err != nil {
			t.Fatal(err)
		}

		progress += 50
	}
}

// ------------------------------------------------------------------------

func TestGetArtifacts(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	artifacts, err := mockClient.GetArtifacts(ctx)
	if err != nil || artifacts == nil {
		t.Fatal(err)
	}

	if err = validateArtifacts(artifacts); err != nil {
		t.Fatal(err)
	}

}

// ------------------------------------------------------------------------

func TestRefreshAssertionToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	mockClient, err := CreateClient(nvctFQDN, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(t.TempDir(), "jwt.token")
	mockClient.StartAssertionTokenRefresher(ctx, tokenPath, false)
	prevToken := ""

	for i := 0; i < 2; i++ {
		time.Sleep(2 * time.Second)

		tokenBytes, err := os.ReadFile(tokenPath)
		if err != nil {
			t.Fatal(err)
		}

		token := string(tokenBytes)
		if !strings.HasPrefix(token, taskId) {
			t.Fatalf("invalid token format")
		}

		if token == prevToken {
			t.Fatalf("token is not refreshed")
		}

		prevToken = token
	}
}

// ------------------------------------------------------------------------

func TestWorkerTokenCaching(t *testing.T) {
	// Create a temp directory for token caching
	tempDir := t.TempDir()

	// Generate an initial token
	initialToken, err := testutils.GenerateJWT(time.Now().Add(-90 * time.Minute).Round(time.Second).Unix())
	if err != nil {
		t.Fatal(err)
	}

	// Create client with the temporary directory for token caching
	mockClient, err := CreateClient(nvctFQDN, initialToken, instanceId, taskId, instanceType, DefaultNvctClientTimeout, tempDir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify no token is cached yet
	tokenFilePath := filepath.Join(tempDir, cachedNvctTokenFilename)
	_, err = os.Stat(tokenFilePath)
	if errors.Is(err, os.ErrNotExist) {
		// Expected - file shouldn't exist yet
	} else if err != nil {
		t.Fatalf("Error checking token file: %v", err)
	} else {
		t.Fatalf("Token cache file should not exist initially")
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	mockClient.StartWorkerTokenRefresher(ctx, false)

	// Wait for token to be refreshed and cached
	time.Sleep(2 * time.Second)

	// Verify token is now cached
	_, err = os.Stat(tokenFilePath)
	if err != nil {
		t.Fatalf("Error: Token cache file not found or inaccessible after refresh: %v", err)
	}

	// Read token file directly
	tokenData, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}

	// Parse the token JSON
	var cachedToken oauth2.Token
	if err := json.Unmarshal(tokenData, &cachedToken); err != nil {
		t.Fatalf("Failed to parse cached token: %v", err)
	}

	// Verify the cached token differs from initial token and is valid
	if cachedToken.AccessToken == initialToken {
		t.Fatal("Cached token should be different from initial token")
	}

	if cachedToken.AccessToken == "" {
		t.Fatal("Cached token should not be empty")
	}
}

// ------------------------------------------------------------------------

func validateArtifacts(artifacts *types.ArtifactsList) error {
	artifactsCount := len(artifacts.Models)
	if artifactsCount != 2 {
		return fmt.Errorf("unexpected number of artifacts: %d, want: 2", artifactsCount)
	}

	modelPaths := []string{
		"config.pbtxt",
		"1/model.graphdef",
	}

	modelUrls := []string{
		"files/config.pbtxt",
		"files/1/model.graphdef",
	}

	for _, artifact := range artifacts.Models {
		if artifact.Name != "simple_int8" {
			return fmt.Errorf("unexpected artifact name: %s, want: simple_int8", artifact.Name)
		}

		if artifact.Version != "2" {
			return fmt.Errorf("unexpected artifact version: %s, want: 2", artifact.Version)
		}

		if !slices.Contains(modelPaths, artifact.Path) {
			return fmt.Errorf("unexpected artifact path: %s", artifact.Path)
		}

		if !slices.Contains(modelUrls, artifact.Url) {
			return fmt.Errorf("unexpected artifact url: %s", artifact.Url)
		}

	}

	return nil

}
