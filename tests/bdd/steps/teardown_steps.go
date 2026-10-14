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
	"fmt"
	"os"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

// functionDeploymentNotFound is how the NVCF API reports a function with no
// deployment, which is the expected state when a scenario already undeployed it.
const functionDeploymentNotFound = "Deployment not found"

// registerTeardownSteps hooks the steps that register cleanup to run after the
// scenario ends, whether it passed or failed. The live runner stops at the
// first failed step, so cleanup written as a later step would never run.
func registerTeardownSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^I send an? "([A-Z]+)" request to "([^"]*)" at scenario teardown expecting status (\d+)$`, sc.iSendHTTPRequestAtTeardown)
	ctx.Step(`^the function selected by NVCF CLI is undeployed at scenario teardown$`, sc.theSelectedFunctionIsUndeployedAtTeardown)
	ctx.Step(`^the function selected by NVCF CLI is deleted at scenario teardown$`, sc.theSelectedFunctionIsDeletedAtTeardown)
}

// addTeardown queues cleanup for the end of the current scenario.
func (sc *ScenarioContext) addTeardown(description string, action func(context.Context) error) {
	sc.teardown = append(sc.teardown, teardownAction{description: description, run: action})
}

// iSendHTTPRequestAtTeardown queues a request for the end of the scenario. It
// takes the headers queued so far, like the send step does, so a credential is
// declared next to the request that needs it. The URL is resolved now, so any
// variable it uses must already be exported. Any status other than the
// expected one fails the scenario.
func (sc *ScenarioContext) iSendHTTPRequestAtTeardown(method, url string, wantStatus int) error {
	headers := sc.HTTPHeaders
	sc.HTTPHeaders = nil
	resolved := dsl.Interpolate(url)
	sc.addTeardown(method+" "+resolved, func(ctx context.Context) error {
		resp, err := sc.httpClient().Do(ctx, method, resolved, headers, "")
		if err != nil {
			return err
		}
		if resp.Status != wantStatus {
			return fmt.Errorf("status %d, want %d: %s", resp.Status, wantStatus, truncateBody(resp.Body))
		}
		return nil
	})
	return nil
}

// theSelectedFunctionIsUndeployedAtTeardown queues an undeploy of the function
// the NVCF CLI has selected. It does nothing when the scenario already
// undeployed the function, and treats a function with no deployment as done, so
// it can be registered before the deploy step.
func (sc *ScenarioContext) theSelectedFunctionIsUndeployedAtTeardown() error {
	return sc.addSelectedFunctionTeardown(
		"undeploy the selected function",
		[]string{"function", "delete", "--deployment-only"},
		functionDeploymentNotFound,
		func() bool { return sc.FunctionUndeployed },
	)
}

// theSelectedFunctionIsDeletedAtTeardown queues a delete of the function the
// NVCF CLI has selected. Register it before the undeploy step so it runs after
// the undeploy.
func (sc *ScenarioContext) theSelectedFunctionIsDeletedAtTeardown() error {
	return sc.addSelectedFunctionTeardown(
		"delete the selected function",
		[]string{"function", "delete"},
		"",
		func() bool { return false },
	)
}

// addSelectedFunctionTeardown queues one NVCF CLI command against the selected
// function. A failure counts as done when its output contains tolerated.
func (sc *ScenarioContext) addSelectedFunctionTeardown(description string, args []string, tolerated string, skip func() bool) error {
	if sc.NVCFCLIConfig == "" {
		return fmt.Errorf("no NVCF CLI config selected; use 'I use NVCF CLI config' first")
	}
	command := dsl.BuildCommand(sc.nvcfCLICommandArgs(args...)...)
	sc.addTeardown(description, func(ctx context.Context) error {
		if skip() {
			return nil
		}
		result, err := sc.Suite.Runner.Run(ctx, command)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		if tolerated != "" && strings.Contains(combinedOutput(result), tolerated) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("exit code %d: %w", result.ExitCode, err)
		}
		return fmt.Errorf("exit code %d", result.ExitCode)
	})
	return nil
}

// runTeardown runs the queued cleanup in reverse order, so a resource is
// released before the one it depends on. A failing action does not stop the
// rest. The queue is cleared and every failure is reported together.
func (sc *ScenarioContext) runTeardown(ctx context.Context) error {
	actions := sc.teardown
	sc.teardown = nil
	var failures []error
	for i := len(actions) - 1; i >= 0; i-- {
		action := actions[i]
		fmt.Fprintf(os.Stderr, ">>> teardown: %s\n", action.description)
		if err := action.run(ctx); err != nil {
			failures = append(failures, fmt.Errorf("teardown %q: %w", action.description, err))
		}
	}
	return errors.Join(failures...)
}
