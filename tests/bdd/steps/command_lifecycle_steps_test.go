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

package steps

import (
	"context"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
)

func TestAfterScenarioCommandIsInterpolatedAndDeferred(t *testing.T) {
	sc, fake := newScenarioContext(t)
	t.Setenv("BDD_TARGET", "compute-1")
	doc := &godog.DocString{Content: "restore ${BDD_TARGET}"}
	if err := sc.afterScenarioISuccessfullyRunCommand(context.Background(), "1s", doc); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("deferred command ran during registration: %v", fake.runs)
	}
	if err := sc.Deferred.Run(context.Background(), fake, t.TempDir()); err != nil {
		t.Fatalf("run deferred: %v", err)
	}
	if len(fake.runs) != 1 || fake.runs[0].command != "restore compute-1" {
		t.Fatalf("runs=%v", fake.runs)
	}
}

func TestCommandShouldSucceedWithinRetriesAndRecordsSuccess(t *testing.T) {
	sc, fake := newScenarioContext(t)
	fake.runResults = []harness.Result{{ExitCode: 1}, {ExitCode: 0, Stdout: "ready"}}
	doc := &godog.DocString{Content: "check target"}
	if err := sc.commandShouldSucceedWithin(context.Background(), "1s", "1ms", doc); err != nil {
		t.Fatalf("eventual command: %v", err)
	}
	if len(fake.runs) != 2 {
		t.Fatalf("runs=%v want two attempts", fake.runs)
	}
	if sc.LastResult.Stdout != "ready" || !sc.Suite.Cache.Has("check target") {
		t.Fatalf("result=%+v command was not cached", sc.LastResult)
	}
}

func TestJSONCommandOutputShouldContainTypedSubset(t *testing.T) {
	sc, _ := newScenarioContext(t)
	sc.LastResult.Stdout = `{"configChanged":true,"extra":"value"}`
	doc := &godog.DocString{Content: `{"configChanged":true}`}
	if err := sc.jsonCommandOutputShouldContain(doc); err != nil {
		t.Fatalf("assert JSON subset: %v", err)
	}
}
