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

package k3d

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner records the k3d invocations and returns canned output per
// subcommand so tests never shell out to a real k3d binary.
type fakeRunner struct {
	calls    [][]string
	listOut  string
	failList bool
}

func (f *fakeRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "cluster" && args[1] == "list" {
		if f.failList {
			return []byte("boom"), context.Canceled
		}
		return []byte(f.listOut), nil
	}
	return nil, nil
}

func withRunner(f *fakeRunner) func() {
	prev := Runner
	Runner = f.run
	return func() { Runner = prev }
}

func called(calls [][]string, want ...string) bool {
	for _, c := range calls {
		if len(c) >= len(want) {
			match := true
			for i := range want {
				if c[i] != want[i] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func TestKubeContext(t *testing.T) {
	if got := KubeContext("byoo-perf"); got != "k3d-byoo-perf" {
		t.Errorf("KubeContext = %q, want k3d-byoo-perf", got)
	}
}

func TestCreateArgs(t *testing.T) {
	args := createArgs(Options{Name: "byoo-perf", Servers: 1, Agents: 0})
	joined := strings.Join(args, " ")
	for _, want := range []string{"cluster create byoo-perf", "--servers 1", "--agents 0", "--wait"} {
		if !strings.Contains(joined, want) {
			t.Errorf("createArgs missing %q, got %q", want, joined)
		}
	}
}

func TestCreateProvisionsWhenAbsent(t *testing.T) {
	f := &fakeRunner{listOut: "other 1/1\n"}
	defer withRunner(f)()

	cluster, err := Create(context.Background(), DefaultOptions("byoo-perf"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cluster.Context != "k3d-byoo-perf" {
		t.Errorf("context = %q", cluster.Context)
	}
	if !called(f.calls, "cluster", "create", "byoo-perf") {
		t.Errorf("expected a cluster create call, got %v", f.calls)
	}
}

func TestCreateReusesWhenPresent(t *testing.T) {
	f := &fakeRunner{listOut: "byoo-perf 1/1\n"}
	defer withRunner(f)()

	if _, err := Create(context.Background(), DefaultOptions("byoo-perf")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if called(f.calls, "cluster", "create", "byoo-perf") {
		t.Errorf("existing cluster should be reused, not recreated: %v", f.calls)
	}
}

func TestDeleteNoOpWhenAbsent(t *testing.T) {
	f := &fakeRunner{listOut: "other 1/1\n"}
	defer withRunner(f)()

	if err := Delete(context.Background(), "byoo-perf"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if called(f.calls, "cluster", "delete", "byoo-perf") {
		t.Errorf("delete of absent cluster should be a no-op: %v", f.calls)
	}
}

func TestDeleteRemovesWhenPresent(t *testing.T) {
	f := &fakeRunner{listOut: "byoo-perf 1/1\n"}
	defer withRunner(f)()

	if err := Delete(context.Background(), "byoo-perf"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !called(f.calls, "cluster", "delete", "byoo-perf") {
		t.Errorf("expected a cluster delete call, got %v", f.calls)
	}
}

func TestImportImages(t *testing.T) {
	f := &fakeRunner{}
	defer withRunner(f)()

	if err := ImportImages(context.Background(), "byoo-perf"); err != nil {
		t.Fatalf("ImportImages (empty): %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("empty import should be a no-op, got %v", f.calls)
	}

	if err := ImportImages(context.Background(), "byoo-perf", "img:a", "img:b"); err != nil {
		t.Fatalf("ImportImages: %v", err)
	}
	if !called(f.calls, "image", "import", "--cluster", "byoo-perf", "img:a", "img:b") {
		t.Errorf("unexpected import args: %v", f.calls)
	}
}

func TestExistsPropagatesListError(t *testing.T) {
	f := &fakeRunner{failList: true}
	defer withRunner(f)()

	if _, err := Exists(context.Background(), "byoo-perf"); err == nil {
		t.Fatal("expected error when cluster list fails")
	}
}
