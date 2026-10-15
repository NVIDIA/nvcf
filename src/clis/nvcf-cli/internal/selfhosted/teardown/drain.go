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

package teardown

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nvcf-cli/internal/selfhosted/progress"
)

// DrainMode controls how Drain handles ACTIVE function deployments.
type DrainMode int

const (
	// DrainModePrompt asks the user interactively whether to force-drain.
	// The orchestrator pre-translates Prompt → Force (TTY) or Skip (non-TTY)
	// before calling Drain; passing Prompt directly returns an error.
	DrainModePrompt DrainMode = iota
	// DrainModeForce removes all ACTIVE deployments and polls until STOPPED.
	DrainModeForce
	// DrainModeSkip emits a Waiting event noting the active count and returns
	// nil. The helm uninstall that follows will terminate them forcibly.
	DrainModeSkip
)

// FunctionDeploymentLister is the SIS API subset that Drain needs. The
// interface lives in the teardown package; the orchestrator wires whichever
// production SIS client implements it. Tests use a fake.
//
// NOTE: ListActiveDeployments, RemoveDeployment, and DeploymentStatus are not
// yet exposed on client.ICMSCluster — those are deferred SIS API surfaces
// tracked as part of M+11.G.
type FunctionDeploymentLister interface {
	ListActiveDeployments(ctx context.Context, sisURL, ncaID, clusterID string) ([]ActiveDeployment, error)
	RemoveDeployment(ctx context.Context, sisURL, ncaID, clusterID, deploymentID string) error
	DeploymentStatus(ctx context.Context, sisURL, ncaID, clusterID, deploymentID string) (string, error)
}

// ActiveDeployment is a minimal descriptor for a deployed function version.
type ActiveDeployment struct {
	ID      string
	Name    string
	Version string
}

// ErrDrainTimeout is returned when ACTIVE deployments do not reach STOPPED
// within drainTimeout.
var ErrDrainTimeout = errors.New("drain timed out waiting for deployments to STOP")

// drainTimeout is the package-level seam overridden in tests to avoid
// real wall-clock waits.
var drainTimeout = 5 * time.Minute

// Drain removes ACTIVE function deployments from the cluster. Returns nil
// when no ACTIVE deployments exist (no-op). Uses DrainModeForce to remove and
// poll; DrainModeSkip to leave them for helm uninstall. DrainModePrompt
// requires pre-translation by the orchestrator before calling Drain.
func Drain(
	ctx context.Context,
	sis FunctionDeploymentLister,
	sisURL, ncaID, clusterID string,
	mode DrainMode,
	sink progress.EventSink,
) error {
	deps, err := sis.ListActiveDeployments(ctx, sisURL, ncaID, clusterID)
	if err != nil {
		return fmt.Errorf("list active deployments: %w", err)
	}
	if len(deps) == 0 {
		return nil
	}

	switch mode {
	case DrainModeSkip:
		_ = sink.Emit(ctx, progress.Waiting{
			Num: 1,
			Reason: fmt.Sprintf(
				"skipping drain: %d active deployment(s) will be terminated by helm uninstall",
				len(deps),
			),
		})
		return nil

	case DrainModePrompt:
		// The orchestrator MUST pre-translate Prompt to Force (TTY) or Skip
		// (non-TTY) before calling Drain.
		return errors.New("DrainModePrompt requires TTY pre-translation by the orchestrator")
	}

	// Force mode: issue remove requests for all deployments first, then poll.
	for _, d := range deps {
		_ = sink.Emit(ctx, progress.DrainProgress{
			Num: 1, Deployment: d.Name, State: "REMOVING",
		})
		if err := sis.RemoveDeployment(ctx, sisURL, ncaID, clusterID, d.ID); err != nil {
			return fmt.Errorf("remove deployment %s: %w", d.Name, err)
		}
	}

	deadline := time.Now().Add(drainTimeout)
	pending := make(map[string]string, len(deps)) // ID → Name
	for _, d := range deps {
		pending[d.ID] = d.Name
	}

	for len(pending) > 0 {
		if time.Now().After(deadline) {
			return ErrDrainTimeout
		}
		for id, name := range pending {
			state, err := sis.DeploymentStatus(ctx, sisURL, ncaID, clusterID, id)
			if err != nil {
				// transient; keep polling
				continue
			}
			if state == "STOPPED" {
				_ = sink.Emit(ctx, progress.DrainProgress{
					Num: 1, Deployment: name, State: "STOPPED",
				})
				delete(pending, id)
			}
		}
		if len(pending) > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}
