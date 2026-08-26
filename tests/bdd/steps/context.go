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

// Package steps registers the strict-DSL Gherkin step handlers with
// Godog. Every handler in this package is a thin wrapper around
// harness.CommandRunner or one of the dsl helpers; no domain-level
// validation lives here. The exhaustive step catalog is in
// tests/bdd/PLAN.md.
package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
	"nvcf-bdd/harness"
)

// ScenarioContext holds the per-scenario state Godog hands to each
// handler. The Suite pointer is shared across scenarios in the same
// suite (so the Ledger and CommandCache persist); LastResult,
// LastErr, LastCommand, and Deferred are reset per scenario inside the Before
// hook installed by RegisterAll. LastCommand tracks the resolved text
// of the most recent command executed in this scenario. A successful-run step
// or an explicit "the command exit code should be 0" assertion uses it to seed
// the suite-level CommandCache so a subsequent "Given command has succeeded:"
// for the same resolved text hits the cache instead of rerunning a destructive
// command.
type ScenarioContext struct {
	Suite         *harness.Suite
	Deferred      *harness.DeferredCommands
	LastResult    harness.Result
	LastErr       error
	LastCommand   string
	NVCFCLIConfig string
}

// StepRegistrar is the narrow registration surface shared by Godog and the
// syntax-only feature catalog test.
type StepRegistrar interface {
	Step(expr, stepFunc interface{})
}

// NewScenarioContext wraps suite in a fresh per-scenario state. The
// caller (typically a Godog scenario initializer) is responsible for
// creating one ScenarioContext per scenario.
func NewScenarioContext(suite *harness.Suite) *ScenarioContext {
	return &ScenarioContext{
		Suite: suite,
		Deferred: harness.NewDeferredCommands(
			filepath.Join(suite.Config.OutDir, "pending-compensations.sh"),
		),
	}
}

// RegisterAll wires every step from every category to ctx so a single
// call from a scenario initializer hooks up the whole catalog.
func RegisterAll(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		sc.LastResult = harness.Result{}
		sc.LastErr = nil
		sc.LastCommand = ""
		sc.NVCFCLIConfig = ""
		sc.Deferred.Reset()
		return c, nil
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		return c, sc.Deferred.Run(c, sc.Suite.Runner, sc.Suite.Config.CommandLogDir)
	})
	// Godog's default pretty formatter buffers the scenario block until
	// every step finishes, which makes hangs invisible during a live
	// run. Echo each step's text to stderr the moment it starts so the
	// operator can see which command is currently executing.
	ctx.StepContext().Before(func(c context.Context, st *godog.Step) (context.Context, error) {
		fmt.Fprintf(os.Stderr, ">>> %s\n", st.Text)
		return c, nil
	})
	RegisterSteps(ctx, sc)
}

// RegisterSteps registers the strict vocabulary without installing scenario
// hooks. Godog uses it through RegisterAll; syntax tests use it to detect
// undefined or ambiguous feature steps without faking product behavior.
func RegisterSteps(ctx StepRegistrar, sc *ScenarioContext) {
	registerFileSteps(ctx, sc)
	registerCommandSteps(ctx, sc)
	registerNVCFCLISteps(ctx, sc)
	registerAssertionSteps(ctx, sc)
	registerInfraSteps(ctx, sc)
}

// cachedRun is the shared implementation behind "Given command has
// succeeded:" and the bootstrap Givens. If the suite has already seen
// the resolved command text complete successfully, the call no-ops;
// otherwise the command runs and on success is recorded in the cache.
// LastResult is set in both branches so an assertion step running
// immediately after a cached Given inspects a synthetic success result
// rather than the stale LastResult from whatever ran earlier in the
// scenario.
func (sc *ScenarioContext) cachedRun(ctx context.Context, command string) error {
	resolved := strings.TrimSpace(dsl.Interpolate(command))
	if sc.Suite.Cache.Has(resolved) {
		sc.LastResult = harness.Result{ExitCode: 0}
		sc.LastErr = nil
		sc.LastCommand = resolved
		return nil
	}
	result, err := sc.Suite.Runner.Run(ctx, resolved)
	sc.LastResult = result
	sc.LastErr = err
	sc.LastCommand = resolved
	if err != nil {
		return err
	}
	sc.Suite.Cache.Record(resolved)
	return nil
}
