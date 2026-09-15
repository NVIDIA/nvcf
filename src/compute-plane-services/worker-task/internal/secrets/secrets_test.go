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

package secrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"go.uber.org/zap"
)

func TestSecretsRotation(t *testing.T) {
	zapLogger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.ReplaceGlobals(zapLogger.GetZapLogger())
	zap.RedirectStdLog(zapLogger.GetZapLogger())

	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secrets.json")
	apiKey := "mock"
	if err := writeSecretsToFile(apiKey, secretFile); err != nil {
		t.Fatal(err)
	}

	secrets, err := New(context.TODO(), secretFile)
	if err != nil {
		t.Fatal(err)
	}

	if secrets.NgcApiKey() != apiKey {
		t.Fatalf("Got secret: %s, expect secret: %s\n", secrets.NgcApiKey(), apiKey)
	}

	time.Sleep(50 * time.Millisecond)

	newApiKey := "new-key"
	if err := writeSecretsToFile(newApiKey, secretFile); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	if secrets.NgcApiKey() != newApiKey {
		t.Fatalf("Got secret: %s, expect secret: %s\n", secrets.NgcApiKey(), newApiKey)
	}
}

func writeSecretsToFile(apiKey, secretFile string) error {
	secretData := secretsData{
		NgcApiKey: apiKey,
	}
	secretJson, err := json.Marshal(secretData)
	if err != nil {
		return err
	}

	return os.WriteFile(secretFile, secretJson, 0644)
}
