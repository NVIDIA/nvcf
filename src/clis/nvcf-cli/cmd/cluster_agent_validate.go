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
	"io"
	"net/http"
	"strings"
	"time"

	"nvcf-cli/internal/clusteragent"
	"nvcf-cli/internal/k8s"

	"github.com/spf13/cobra"
)

const (
	flagCheck    = "check"
	flagFailFast = "fail-fast"
	flagNVCAURL  = "nvca-url"

	// validationHTTPTimeout bounds the whole NVCA HTTP client; per-probe timeouts
	// are applied in the validator.
	validationHTTPTimeout = 15 * time.Second
)

var clusterAgentValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Run pre-defined health checks against a cluster",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Run a set of pre-defined health checks against a compute-plane cluster and
print a pass/fail table. The checks cover NVCA reachability, GPU count vs the
capacity NVCA registered, NATS/queue health, image pull credential validity, and
TLS certificate expiry.

By default every check runs. Use --check to run a subset (repeatable) and
--fail-fast to stop at the first failure. Pass --nvca-url to enable live NVCA
HTTP probes (/version and /livez); without it those checks fall back to the
NVCFBackend custom resource.

Select the cluster with --compute-plane-context, as with the other agent commands.
The command exits non-zero if any check fails.`,
	RunE: runClusterAgentValidate,
}

var clusterAgentValidateDeploymentCmd = &cobra.Command{
	Use:          "validate-deployment <function-id> [version-id]",
	Short:        "Check that a specific function deployment is healthy",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	Long: `Check the health of one function deployment scheduled on the cluster: pod
readiness, request/queue phase, and cluster GPU utilization. With no version-id,
the first scheduled request for the function is used.

The command exits non-zero if any check fails.`,
	RunE: runClusterAgentValidateDeployment,
}

// clusterAgentValidateFlags holds the bound --check slice. Binding to a variable
// (rather than reading via GetStringSlice) gives tests a clean reset point, since
// pflag's StringSlice appends on repeated Set calls within a single process.
var clusterAgentValidateFlags struct {
	checks []string
}

// newAgentValidator builds the AgentValidator for a command. It is a package var
// so tests can swap in a fake (mirroring newAgentMaintainer).
var newAgentValidator = loadAgentValidator

func loadAgentValidator(cmd *cobra.Command) (clusteragent.AgentValidator, error) {
	kubeconfig, _ := cmd.Flags().GetString(flagKubeconfig)
	ctxOverride, _ := cmd.Flags().GetString(flagComputePlaneContext)

	kc, err := k8s.NewClient(&k8s.ClientConfig{
		KubeconfigPath:  kubeconfig,
		ContextOverride: ctxOverride,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to compute-plane cluster: %w", err)
	}

	// The HTTP client is only created when an NVCA URL is configured; a nil client
	// tells the validator to fall back to CR-derived state for the HTTP checks.
	var httpCli *http.Client
	if nvcaURL, _ := cmd.Flags().GetString(flagNVCAURL); nvcaURL != "" {
		httpCli = &http.Client{Timeout: validationHTTPTimeout}
	}
	return clusteragent.NewK8sValidator(kc.Clientset(), kc.Dynamic(), httpCli), nil
}

func initClusterAgentValidateCmds() {
	cmds := []*cobra.Command{clusterAgentValidateCmd, clusterAgentValidateDeploymentCmd}
	for _, c := range cmds {
		clusterAgentCmd.AddCommand(c)
		c.Flags().String(flagComputePlaneContext, "", "Kube context for the target compute-plane cluster")
		c.Flags().String(flagKubeconfig, "", "Path to kubeconfig for the target cluster")
		c.Flags().String(flagBackendNamespace, defaultBackendNamespace, "Namespace of the NVCFBackend resource")
	}

	clusterAgentValidateCmd.Flags().StringSliceVar(&clusterAgentValidateFlags.checks, flagCheck, nil, "Run only the named check(s); repeatable. One of: "+strings.Join(clusteragent.AllClusterChecks, ", "))
	clusterAgentValidateCmd.Flags().Bool(flagFailFast, false, "Stop after the first failing check")
	clusterAgentValidateCmd.Flags().String(flagNVCAURL, "", "Base URL for NVCA HTTP checks (/version, /livez); HTTP checks are skipped when omitted")
}

func runClusterAgentValidate(cmd *cobra.Command, _ []string) error {
	checks := clusterAgentValidateFlags.checks
	if err := validateCheckNames(checks); err != nil {
		return err
	}
	failFast, _ := cmd.Flags().GetBool(flagFailFast)
	nvcaURL, _ := cmd.Flags().GetString(flagNVCAURL)
	backendNS, _ := cmd.Flags().GetString(flagBackendNamespace)

	v, err := newAgentValidator(cmd)
	if err != nil {
		return err
	}

	res, err := v.Validate(context.Background(), clusteragent.ValidateOptions{
		BackendNS:  backendNS,
		CheckNames: checks,
		FailFast:   failFast,
		NVCAURL:    nvcaURL,
	})
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		if err := OutputJSON(res); err != nil {
			return err
		}
	} else {
		printValidationReport(cmd, res)
	}
	if res.HasFailure() {
		return &ExitCodeError{Code: 2, Msg: "cluster validation failed"}
	}
	return nil
}

func runClusterAgentValidateDeployment(cmd *cobra.Command, args []string) error {
	functionID := args[0]
	versionID := ""
	if len(args) == 2 {
		versionID = args[1]
	}
	backendNS, _ := cmd.Flags().GetString(flagBackendNamespace)

	v, err := newAgentValidator(cmd)
	if err != nil {
		return err
	}

	out, err := v.ValidateDeployment(context.Background(), functionID, versionID, clusteragent.ValidateOptions{
		BackendNS: backendNS,
	})
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		if err := OutputJSON(out); err != nil {
			return err
		}
	} else {
		printDeploymentValidation(cmd, out)
	}
	if out.HasFailure() {
		return &ExitCodeError{Code: 2, Msg: "deployment validation failed"}
	}
	return nil
}

// validateCheckNames rejects any --check value that is not a known cluster check.
func validateCheckNames(names []string) error {
	valid := make(map[string]bool, len(clusteragent.AllClusterChecks))
	for _, n := range clusteragent.AllClusterChecks {
		valid[n] = true
	}
	for _, n := range names {
		if !valid[n] {
			return fmt.Errorf("invalid --check %q: must be one of %s", n, strings.Join(clusteragent.AllClusterChecks, ", "))
		}
	}
	return nil
}

func printValidationReport(cmd *cobra.Command, res *clusteragent.ValidationResult) {
	w := cmd.OutOrStdout()
	header := "Cluster Validation"
	if id := joinNameID(res.ClusterName, res.ClusterID); id != "" {
		header += ": " + id
	}
	fmt.Fprintf(w, "%s\n\n", header)
	printCheckTable(w, res.Checks)
	printCheckSummary(w, res.Checks)
}

func printDeploymentValidation(cmd *cobra.Command, out *clusteragent.DeploymentValidation) {
	w := cmd.OutOrStdout()
	header := "Deployment Validation: function " + out.FunctionID
	if out.FunctionVersionID != "" {
		header += " version " + out.FunctionVersionID
	}
	fmt.Fprintf(w, "%s\n\n", header)
	printCheckTable(w, out.Checks)
	printCheckSummary(w, out.Checks)
}

func printCheckTable(w io.Writer, checks []clusteragent.CheckResult) {
	fmt.Fprintf(w, "  %-18s %-7s %s\n", "CHECK", "STATUS", "MESSAGE")
	fmt.Fprintf(w, "  %-18s %-7s %s\n", "-----", "------", "-------")
	for _, c := range checks {
		fmt.Fprintf(w, "  %-18s %-7s %s\n", c.Name, c.Status, c.Message)
	}
}

func printCheckSummary(w io.Writer, checks []clusteragent.CheckResult) {
	var pass, warn, fail, skip int
	for _, c := range checks {
		switch c.Status {
		case clusteragent.CheckPassed:
			pass++
		case clusteragent.CheckWarning:
			warn++
		case clusteragent.CheckFailed:
			fail++
		case clusteragent.CheckSkipped:
			skip++
		}
	}
	fmt.Fprintf(w, "\nResult: %d passed, %d warning, %d failed", pass, warn, fail)
	if skip > 0 {
		fmt.Fprintf(w, ", %d skipped", skip)
	}
	fmt.Fprintln(w)
}
