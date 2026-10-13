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

package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"nvcf-cli/internal/logging"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/selfhosted/kubectx"
)

const (
	eventLedgerReleaseName = "event-ledger"
	eventLedgerNamespace   = "nvcf"
	eventLedgerHostService = "events"
)

// eventLedgerDeployed reports whether the control plane has the event-ledger
// release deployed. The stack gates that release on addons.eventLedger.enabled.
var eventLedgerDeployed = func(ctx context.Context, kubeContext string) (bool, error) {
	args := []string{}
	if kubeContext != "" {
		args = append(args, "--kube-context", kubeContext)
	}
	args = append(args, "list", "--namespace", eventLedgerNamespace, "--deployed", "--short")
	out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("helm list: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == eventLedgerReleaseName {
			return true, nil
		}
	}
	return false, nil
}

// resolveEventLedgerAddon decides whether the compute plane should run the NVCA
// OTel collector. A failed lookup leaves the collector disabled so a broken
// control-plane query never points the collector at a missing backend.
func resolveEventLedgerAddon(ctx context.Context, env, controlCtx, computeCtx, icmsURL string) controlplaneprofile.EventLedgerAddon {
	if ctx == nil {
		ctx = context.Background()
	}
	deployed, err := eventLedgerDeployed(ctx, controlCtx)
	if err != nil {
		logging.Warning("Could not check for the event-ledger release; leaving the NVCA OTel collector disabled: %v", err)
		return controlplaneprofile.EventLedgerAddon{}
	}
	if !deployed {
		return controlplaneprofile.EventLedgerAddon{}
	}
	addon := controlplaneprofile.EventLedgerAddon{Enabled: true}
	// A compute plane in the control-plane cluster uses the chart's in-cluster default.
	if kubectx.SelectMode(controlCtx, computeCtx) == kubectx.ModeSingle {
		return addon
	}
	if strings.EqualFold(env, "local") {
		addon.ServiceURL = localSplitHTTPServiceURL(icmsURL, eventLedgerHostService)
	} else {
		addon.ServiceURL = deriveSiblingHTTPServiceURL(icmsURL, eventLedgerHostService)
	}
	return addon
}
