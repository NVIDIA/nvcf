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

package main

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/sirupsen/logrus"
)

// silentLogger returns a logrus logger that discards output, so test
// runs don't dump capture-plan parse output.
func silentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// TestRunCaptureWrite_EmptyPlanFails covers the env-var-missing case
// — the Job's container can't proceed without a plan.
func TestRunCaptureWrite_EmptyPlanFails(t *testing.T) {
	prev := os.Getenv("NVSNAP_CAPTURE_PLAN")
	_ = os.Setenv("NVSNAP_CAPTURE_PLAN", "")
	defer func() { _ = os.Setenv("NVSNAP_CAPTURE_PLAN", prev) }()
	if err := runCaptureWrite(context.Background(), silentLogger()); err == nil {
		t.Fatal("expected error when NVSNAP_CAPTURE_PLAN is empty")
	}
}
