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

package dsl

import "testing"

func TestServiceMonitorExistenceCommandBuildsSingleExplicitGet(t *testing.T) {
	names := []string{
		"nvcf-default-monitors-state-metrics",
		"nvcf-default-monitors-grpc-proxy",
	}

	got, err := ServiceMonitorExistenceCommand("monitoring", "k3d-ncp-local", names)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "kubectl get servicemonitor/nvcf-default-monitors-state-metrics servicemonitor/nvcf-default-monitors-grpc-proxy --namespace monitoring --context k3d-ncp-local"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestServiceMonitorExistenceCommandRejectsEmptyNames(t *testing.T) {
	if _, err := ServiceMonitorExistenceCommand("monitoring", "k3d-ncp-local", nil); err == nil {
		t.Fatal("expected empty names error")
	}
}
