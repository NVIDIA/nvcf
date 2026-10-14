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
	"errors"
	"strings"
	"testing"

	"nvcf-bdd/harness"
)

func TestHTTPTeardownSendsRegisteredRequestWithItsHeaders(t *testing.T) {
	t.Setenv("BDD_TEARDOWN_KEY_ID", "key-1")
	sc, fake := newHTTPScenario(t, harness.HTTPResponse{Status: 204})
	if err := sc.theHTTPRequestHeaderIs("Key-Owner-Id", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := sc.iSendHTTPRequestAtTeardown("DELETE", "http://api-keys/v1/keys/${BDD_TEARDOWN_KEY_ID}", 204); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 {
		t.Fatalf("request sent at registration: %d calls", fake.calls)
	}
	if len(sc.HTTPHeaders) != 0 {
		t.Fatalf("pending headers not consumed: %v", sc.HTTPHeaders)
	}

	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if fake.method != "DELETE" || fake.url != "http://api-keys/v1/keys/key-1" || fake.headers["Key-Owner-Id"] != "owner" {
		t.Fatalf("sent %s %s headers=%v", fake.method, fake.url, fake.headers)
	}
}

func TestHTTPTeardownReportsFailure(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 500, Body: "boom"})
	if err := sc.iSendHTTPRequestAtTeardown("DELETE", "http://x/", 204); err != nil {
		t.Fatal(err)
	}
	err := sc.runTeardown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 500, want 204") {
		t.Fatalf("expected status failure, got %v", err)
	}
}

func TestHTTPTeardownRequiresTheExactStatus(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 200})
	if err := sc.iSendHTTPRequestAtTeardown("DELETE", "http://x/", 204); err != nil {
		t.Fatal(err)
	}
	if err := sc.runTeardown(context.Background()); err == nil || !strings.Contains(err.Error(), "status 200, want 204") {
		t.Fatalf("a 200 must not satisfy an expected 204, got %v", err)
	}
}

func TestTeardownRunsInReverseOrderAndContinuesAfterFailure(t *testing.T) {
	sc, _ := newScenarioContext(t)
	var order []string
	for _, name := range []string{"first", "second", "third"} {
		sc.addTeardown(name, func(context.Context) error {
			order = append(order, name)
			if name == "second" {
				return errors.New("second failed")
			}
			return nil
		})
	}
	err := sc.runTeardown(context.Background())
	if strings.Join(order, ",") != "third,second,first" {
		t.Fatalf("order = %v", order)
	}
	if err == nil || !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("expected the second failure, got %v", err)
	}
	if len(sc.teardown) != 0 {
		t.Fatal("queue not cleared")
	}
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatalf("second run should be a no-op: %v", err)
	}
}

func TestUndeployTeardownRequiresSelectedConfig(t *testing.T) {
	sc, _ := newScenarioContext(t)
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err == nil {
		t.Fatal("expected an error without an NVCF CLI config")
	}
}

func TestUndeployTeardownRunsTheCLIOnce(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 0 {
		t.Fatal("undeploy ran at registration")
	}
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 1 || !strings.Contains(fake.runs[0].command, "function delete --deployment-only") ||
		!strings.Contains(fake.runs[0].command, "--config /cfg.yaml") {
		t.Fatalf("runs = %+v", fake.runs)
	}
}

func TestUndeployTeardownSkipsAfterTheScenarioUndeployed(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if err := sc.iSuccessfullyUndeploySelectedFunction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sc.FunctionUndeployed {
		t.Fatal("successful undeploy not remembered")
	}
	runsBefore := len(fake.runs)
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != runsBefore {
		t.Fatalf("teardown undeployed again: %+v", fake.runs[runsBefore:])
	}
}

func TestUndeployTeardownToleratesAFunctionWithNoDeployment(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	fake.result = harness.Result{ExitCode: 1, Stderr: `Error: API error 404: {"detail":"Deployment not found"}`}
	fake.err = errors.New("exit status 1")
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatalf("not-found should count as already undeployed: %v", err)
	}
}

func TestUndeployTeardownReportsOtherFailures(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	fake.result = harness.Result{ExitCode: 1, Stderr: "Error: connection refused"}
	fake.err = errors.New("exit status 1")
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if err := sc.runTeardown(context.Background()); err == nil {
		t.Fatal("expected the failure to be reported")
	}
}

func TestDeleteTeardownRequiresSelectedConfig(t *testing.T) {
	sc, _ := newScenarioContext(t)
	if err := sc.theSelectedFunctionIsDeletedAtTeardown(); err == nil {
		t.Fatal("expected an error without an NVCF CLI config")
	}
}

func TestDeleteTeardownDeletesTheFunctionAfterTheUndeploy(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	if err := sc.theSelectedFunctionIsDeletedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if err := sc.theSelectedFunctionIsUndeployedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 0 {
		t.Fatal("teardown ran at registration")
	}
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 2 ||
		!strings.HasSuffix(fake.runs[0].command, "function delete --deployment-only") ||
		!strings.HasSuffix(fake.runs[1].command, "function delete") {
		t.Fatalf("want undeploy then delete, got %+v", fake.runs)
	}
}

func TestDeleteTeardownStillDeletesAfterTheScenarioUndeployed(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	if err := sc.theSelectedFunctionIsDeletedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	sc.FunctionUndeployed = true
	if err := sc.runTeardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 1 || !strings.HasSuffix(fake.runs[0].command, "function delete") {
		t.Fatalf("runs = %+v", fake.runs)
	}
}

func TestDeleteTeardownReportsFailures(t *testing.T) {
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	sc, fake := newScenarioContext(t)
	sc.NVCFCLIConfig = "/cfg.yaml"
	fake.result = harness.Result{ExitCode: 1, Stderr: `Error: API error 404: {"detail":"Deployment not found"}`}
	fake.err = errors.New("exit status 1")
	if err := sc.theSelectedFunctionIsDeletedAtTeardown(); err != nil {
		t.Fatal(err)
	}
	if err := sc.runTeardown(context.Background()); err == nil {
		t.Fatal("the deployment-not-found tolerance must not apply to a function delete")
	}
}
