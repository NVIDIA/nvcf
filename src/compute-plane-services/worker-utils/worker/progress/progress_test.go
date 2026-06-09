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
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"go.uber.org/zap"
)

func TestMonitor(t *testing.T) {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	testDir := t.TempDir()
	monitor := New(testDir)
	err := monitor.Start()
	if err != nil {
		t.Fatalf("failed to start progress monitor")
	}

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

	responseChan := make(chan *http.Response, 100)
	monitoredWork := &MonitoredWork{
		Context:       context.Background(),
		MeteringEvent: meteringEvent,
		RespondToWork: responseChan,
	}

	monitor.Monitor(requestId, monitoredWork)

	requestDir := filepath.Join(testDir, requestId)
	err = os.Mkdir(requestDir, 0777)
	if err != nil {
		t.Fatalf("failed to create request directory: %v", err)
	}

	// Small breather for the watcher to catch up.
	time.Sleep(10 * time.Millisecond)

	progressFile := filepath.Join(requestDir, "progress")
	progressToSend := []uint32{15, 45, 75}
	for _, p := range progressToSend {
		t.Logf("writing file for %d", p)
		err := writeProgressMessage(progressFile, requestId, p)
		if err != nil {
			t.Error(err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	err = monitor.Stop()
	if err != nil {
		t.Fatalf("failed to stop progress monitor")
	}

	close(responseChan)

	progressMessagesCount := 0
	for progressMessage := range responseChan {
		// Use direct map access for the custom header
		percentCompleteHeader := progressMessage.Header.Get("Nvcf-Percent-Complete")
		// Parse the percent complete from header
		percentComplete, err := strconv.Atoi(percentCompleteHeader)
		if err != nil {
			t.Fatalf("failed to parse percent complete from header: %v", err)
		}
		if !slices.Contains(progressToSend, uint32(percentComplete)) {
			t.Fatalf("unexpected percent complete = %d", percentComplete)
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
		progressMessagesCount++
	}
	if progressMessagesCount != len(progressToSend) {
		t.Fatalf("num received messages = %d, want = %d", progressMessagesCount, len(progressToSend))
	}
}

func writeProgressMessage(progressFile string, requestId string, progress uint32) error {
	progressMessage := fmt.Sprintf("{\"id\": \"%s\", \"progress\": %d, \"partialResponse\": {\"message\": \"i'm %d complete\"}}", requestId, progress, progress)
	return os.WriteFile(progressFile, []byte(progressMessage), 0644)
}
