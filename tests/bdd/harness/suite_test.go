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

package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSignalSafeStepsPreserveNormalContinuation(t *testing.T) {
	suite := &Suite{}
	stopSignalCleanup := suite.InstallSignalCleanup()
	defer stopSignalCleanup()

	type contextKey string
	parent := context.WithValue(context.Background(), contextKey("scenario"), "retained")
	first, err := suite.BeginSignalSafeStep(parent)
	if err != nil {
		t.Fatalf("begin first signal-safe step: %v", err)
	}

	continuation := suite.EndSignalSafeStep(first)
	if err := continuation.Err(); err != nil {
		t.Fatalf("normal continuation was canceled: %v", err)
	}
	if got := continuation.Value(contextKey("scenario")); got != "retained" {
		t.Fatalf("normal continuation lost scenario value: got %v", got)
	}
	if repeated := suite.EndSignalSafeStep(first); repeated != continuation {
		t.Fatal("repeated end did not return the same continuation context")
	}

	second, err := suite.BeginSignalSafeStep(continuation)
	if err != nil {
		t.Fatalf("begin second signal-safe step: %v", err)
	}
	defer suite.EndSignalSafeStep(second)
	if err := second.Err(); err != nil {
		t.Fatalf("second step inherited canceled context: %v", err)
	}
}

func TestSuiteRestoresGeneratedSecretOnSignals(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		signal   os.Signal
		exitCode int
	}{
		{name: "SIGINT", signal: os.Interrupt, exitCode: 130},
		{name: "SIGTERM", signal: syscall.SIGTERM, exitCode: 143},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testDir := t.TempDir()
			secretPath := filepath.Join(testDir, "local-bdd-pki-secrets.yaml")
			readyPath := filepath.Join(testDir, "ready")
			var output bytes.Buffer
			cmd := exec.Command(os.Args[0], "-test.run=^TestSuiteSignalCleanupHelper$")
			cmd.Env = append(os.Environ(),
				"NVCF_BDD_SIGNAL_HELPER=1",
				"NVCF_BDD_SIGNAL_SECRET="+secretPath,
				"NVCF_BDD_SIGNAL_READY="+readyPath,
			)
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatalf("start signal cleanup helper: %v", err)
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(readyPath); err == nil {
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stat helper readiness file: %v", err)
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("signal cleanup helper did not become ready:\n%s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}

			if err := cmd.Process.Signal(testCase.signal); err != nil {
				t.Fatalf("send %s: %v", testCase.name, err)
			}
			waitDone := make(chan error, 1)
			go func() {
				waitDone <- cmd.Wait()
			}()
			var err error
			select {
			case err = <-waitDone:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-waitDone
				t.Fatalf("signal cleanup helper did not exit after %s:\n%s", testCase.name, output.String())
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("signal cleanup helper error = %v, want exit %d\n%s", err, testCase.exitCode, output.String())
			}
			if got := exitErr.ExitCode(); got != testCase.exitCode {
				t.Fatalf("signal cleanup helper exit code = %d, want %d\n%s", got, testCase.exitCode, output.String())
			}
			if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("generated registry credential remained after %s cleanup: %v", testCase.name, err)
			}
		})
	}
}

func TestSuiteSignalCleanupHelper(t *testing.T) {
	if os.Getenv("NVCF_BDD_SIGNAL_HELPER") != "1" {
		return
	}
	secretPath := os.Getenv("NVCF_BDD_SIGNAL_SECRET")
	readyPath := os.Getenv("NVCF_BDD_SIGNAL_READY")
	suite := &Suite{
		Ledger:    NewLedger(filepath.Join(filepath.Dir(secretPath), "originals")),
		EnvLedger: NewEnvLedger(),
	}
	if err := suite.Ledger.Snapshot(secretPath); err != nil {
		t.Fatalf("snapshot generated secret: %v", err)
	}
	stopSignalCleanup := suite.InstallSignalCleanup()
	defer stopSignalCleanup()

	stepContext, err := suite.BeginSignalSafeStep(context.Background())
	if err != nil {
		t.Fatalf("begin signal-safe step: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("registryCredential: generated\n"), 0o600); err != nil {
		t.Fatalf("write generated secret: %v", err)
	}
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("write helper readiness file: %v", err)
	}

	<-stepContext.Done()
	// Simulate a file-writing step completing after cancellation. Signal
	// cleanup must wait for this active step, then restore the absent snapshot.
	if err := os.WriteFile(secretPath, []byte("registryCredential: recreated-after-cancel\n"), 0o600); err != nil {
		t.Fatalf("rewrite generated secret after cancellation: %v", err)
	}
	suite.EndSignalSafeStep(stepContext)
	select {}
}
