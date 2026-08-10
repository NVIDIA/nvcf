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

package agent

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestStartRootfsCapture_DisabledIsNoop(t *testing.T) {
	a := &Agent{config: Config{}, log: logrus.New()}
	if _, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{Enabled: false}); err != nil {
		t.Fatalf("disabled should be no-op; got %v", err)
	}
}

func TestStartRootfsCapture_EnabledFailsWithoutKubeConfig(t *testing.T) {
	// In a unit-test environment with no in-cluster SA + no KUBECONFIG,
	// kube client construction fails. We verify that error is surfaced
	// (so a misconfigured agent doesn't silently skip capture).
	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	t.Setenv("HOME", t.TempDir()) // hide any ~/.kube/config the test runner has
	a := &Agent{config: Config{}, log: logrus.New()}
	_, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected kube client construction error when no config available")
	}
}
