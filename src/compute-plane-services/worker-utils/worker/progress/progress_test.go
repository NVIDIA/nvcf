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

package progress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
)

// TestMonitor waits for each progress update before overwriting the file.
func TestMonitor(t *testing.T) {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	t.Cleanup(zap.ReplaceGlobals(zapLogger.GetZapLogger()))
	t.Cleanup(zap.RedirectStdLog(zapLogger.GetZapLogger()))

	testDir := t.TempDir()
	monitor := New(testDir)
	require.NoError(t, monitor.Start())
	responseChan := make(chan *http.Response, 3)
	t.Cleanup(func() {
		// Close without holding monitor.lock so an in-flight event can finish.
		// Stop's lifecycle behavior is covered separately.
		require.NoError(t, monitor.watcher.Close())
		select {
		case <-monitor.monitorClosed:
			if extra := len(responseChan); extra != 0 {
				t.Errorf("received %d unexpected extra progress messages", extra)
			}
		case <-time.After(5 * time.Second):
			t.Error("progress monitor did not stop after closing its watcher")
		}
	})

	requestId := "reqid-abc-123"
	meteringConfig := metering.Config{
		Backend:           "LocalBackend",
		NcaId:             "nca-abc-123",
		FunctionId:        "fid-abc-123",
		FunctionVersionId: "fvid-abc-123",
		ICMSEnvironment:   "LocalEnvironment",
		ZoneName:          "LocalZone",
	}
	meteringEvent := metering.New(&meteringConfig, requestId, "sub-abc-123", "invoker-nca-id", nil)

	monitoredWork := &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: meteringEvent,
		RespondToWork: responseChan,
	}

	monitor.Monitor(requestId, monitoredWork)

	requestDir := filepath.Join(testDir, requestId)
	require.NoError(t, os.Mkdir(requestDir, 0777))
	require.Eventually(t, func() bool {
		return slices.Contains(monitor.watcher.WatchList(), requestDir)
	}, 5*time.Second, time.Millisecond, "request directory was not watched")

	progressFile := filepath.Join(requestDir, "progress")
	progressToSend := []uint32{15, 45, 75}
	for _, p := range progressToSend {
		t.Logf("writing file for %d", p)
		require.NoError(t, writeProgressMessage(progressFile, requestId, p))
		var progressMessage *http.Response
		select {
		case progressMessage = <-responseChan:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for progress update %d", p)
		}

		// Use direct map access for the custom header
		percentCompleteHeader := progressMessage.Header.Get("Nvcf-Percent-Complete")
		// Parse the percent complete from header
		percentComplete, err := strconv.Atoi(percentCompleteHeader)
		if err != nil {
			t.Fatalf("failed to parse percent complete from header: %v", err)
		}
		if percentComplete != int(p) {
			t.Fatalf("percent complete = %d, want = %d", percentComplete, p)
		}
		if progressMessage.StatusCode != http.StatusAccepted {
			t.Fatalf("message status code = %d, want = %d", progressMessage.StatusCode, http.StatusAccepted)
		}
		// Read the body to verify the message
		body, err := io.ReadAll(progressMessage.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		_ = progressMessage.Body.Close()
		expectedMessage := fmt.Sprintf("{\"message\":\"i'm %d complete\"}", percentComplete)
		receivedMessage := string(body)
		if receivedMessage != expectedMessage {
			t.Fatalf("received message = %s, want = %s", receivedMessage, expectedMessage)
		}
	}
}

func writeProgressMessage(progressFile string, requestId string, progress uint32) error {
	progressMessage := fmt.Sprintf("{\"id\": \"%s\", \"progress\": %d, \"partialResponse\": {\"message\": \"i'm %d complete\"}}", requestId, progress, progress)
	// Publish a complete update atomically so the watcher never reads a
	// truncated file while the next message is being written.
	temporaryFile := progressFile + ".tmp"
	if err := os.WriteFile(temporaryFile, []byte(progressMessage), 0644); err != nil {
		return err
	}
	return os.Rename(temporaryFile, progressFile)
}
