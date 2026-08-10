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
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"nvcf-cli/internal/clusteragent"
	"nvcf-cli/internal/k8s"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

const (
	flagBackendNamespace = "backend-namespace"
	flagDryRun           = "dry-run"
	flagYes              = "yes"
	flagForce            = "force"
	flagReason           = "reason"
	flagExpectClusterID  = "expect-cluster-id"
	flagConfirm          = "confirm"
	flagTimeout          = "timeout"

	defaultRolloutTimeout = 5 * time.Minute
)

var clusterAgentCordonDrainCmd = &cobra.Command{
	Use:          "cordon-and-drain",
	Aliases:      []string{"drain"},
	Short:        "Drain a cluster for maintenance (cordon, then drain to zero)",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Put the cluster into CordonAndDrain maintenance: stop accepting new
deployments, let in-flight requests complete, and scale all function instances
to zero.

This sets the CordonAndDrainMaintenance feature flag and maintenanceMode on the
NVCA agent-config ConfigMap and restarts the NVCA deployment. The command returns
once NVCA has been told to drain (and, by default, once the restart rolls out);
use "cluster agent list-functions --phase DRAINING" to watch instances wind down.

Select the cluster with --compute-plane-context, as with the inspection commands.`,
	RunE: runClusterAgentCordonDrain,
}

var clusterAgentUncordonCmd = &cobra.Command{
	Use:          "uncordon",
	Aliases:      []string{"undrain"},
	Short:        "Reverse a drain and re-enable the cluster",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Reverse a cordon-and-drain: remove the CordonAndDrainMaintenance feature
flag and maintenanceMode from the NVCA agent-config ConfigMap and restart NVCA so
the cluster accepts new deployments again.`,
	RunE: runClusterAgentUncordon,
}

var clusterAgentKillFunctionCmd = &cobra.Command{
	Use:          "kill-function <function-id> [version-id]",
	Short:        "Force-terminate all instances of one function version",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	Long: `Force-terminate every instance of one function version immediately by
deleting its ICMSRequest custom resource(s). The NVCA reconciler detects the
deletion and evicts the workload. With no version-id, all versions of the
function are terminated.

By default this is a graceful delete that lets NVCA evict the workload. Use
--force to also strip finalizers when a request is stuck Terminating because NVCA
is not running.`,
	RunE: runClusterAgentKillFunction,
}

var clusterAgentKillAllCmd = &cobra.Command{
	Use:          "kill-all",
	Short:        "Force-terminate every function running on the cluster",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Force-terminate every function running on the cluster by deleting all
ICMSRequest custom resources. The affected function ids are printed before
anything is deleted.

This is a high-impact operation. It requires typing the cluster name to confirm;
there is no plain --yes bypass. For automation, pass both --yes and
--confirm <cluster-name> matching the connected cluster.`,
	RunE: runClusterAgentKillAll,
}

// newAgentMaintainer builds the AgentMaintainer for a command. It is a package
// var so tests can swap in a fake (mirroring newClusterDeleterForDown).
var newAgentMaintainer = loadAgentMaintainer

func loadAgentMaintainer(cmd *cobra.Command) (clusteragent.AgentMaintainer, error) {
	kubeconfig, _ := cmd.Flags().GetString(flagKubeconfig)
	ctxOverride, _ := cmd.Flags().GetString(flagComputePlaneContext)

	kc, err := k8s.NewClient(&k8s.ClientConfig{
		KubeconfigPath:  kubeconfig,
		ContextOverride: ctxOverride,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to compute-plane cluster: %w", err)
	}
	return clusteragent.NewK8sMaintainer(kc.Dynamic(), kc.Clientset()), nil
}

func initClusterAgentMaintenanceCmds() {
	maintenanceCmds := []*cobra.Command{
		clusterAgentCordonDrainCmd,
		clusterAgentUncordonCmd,
		clusterAgentKillFunctionCmd,
		clusterAgentKillAllCmd,
	}
	for _, c := range maintenanceCmds {
		clusterAgentCmd.AddCommand(c)
		c.Flags().String(flagComputePlaneContext, "", "Kube context for the target compute-plane cluster")
		c.Flags().String(flagKubeconfig, "", "Path to kubeconfig for the target cluster")
		c.Flags().String(flagBackendNamespace, defaultBackendNamespace, "Namespace of the NVCFBackend resource")
		c.Flags().Bool(flagDryRun, false, "Print intended actions without mutating the cluster")
		c.Flags().Bool(flagYes, false, "Skip the confirmation prompt")
		c.Flags().String(flagExpectClusterID, "", "Refuse to act unless the connected cluster's id or name matches this value")
	}

	for _, c := range []*cobra.Command{clusterAgentCordonDrainCmd, clusterAgentUncordonCmd} {
		c.Flags().Duration(flagTimeout, defaultRolloutTimeout, "How long to wait for the NVCA rollout to complete")
		c.Flags().Bool(flagForce, false, "Skip waiting for the NVCA rollout to complete")
	}

	for _, c := range []*cobra.Command{clusterAgentKillFunctionCmd, clusterAgentKillAllCmd} {
		c.Flags().String(flagReason, "", "Optional reason recorded in logs for audit")
		c.Flags().Bool(flagForce, false, "Strip finalizers so requests stuck Terminating are removed")
	}
	clusterAgentKillAllCmd.Flags().String(flagConfirm, "", "Cluster name confirming kill-all (required with --yes)")
}

func runClusterAgentCordonDrain(cmd *cobra.Command, _ []string) error {
	return runDrainCommon(cmd, true)
}

func runClusterAgentUncordon(cmd *cobra.Command, _ []string) error {
	return runDrainCommon(cmd, false)
}

func runDrainCommon(cmd *cobra.Command, drain bool) error {
	m, err := newAgentMaintainer(cmd)
	if err != nil {
		return err
	}

	backendNS, _ := cmd.Flags().GetString(flagBackendNamespace)
	dryRun, _ := cmd.Flags().GetBool(flagDryRun)
	yes, _ := cmd.Flags().GetBool(flagYes)
	force, _ := cmd.Flags().GetBool(flagForce)
	timeout, _ := cmd.Flags().GetDuration(flagTimeout)
	expect, _ := cmd.Flags().GetString(flagExpectClusterID)

	ctx := context.Background()
	verb := "drain"
	if !drain {
		verb = "undrain"
	}

	if !dryRun {
		target, err := m.ResolveCluster(ctx, backendNS)
		if err != nil {
			return err
		}
		if err := checkExpectedCluster(target, expect); err != nil {
			return err
		}
		ok, err := confirmSimple(cmd, fmt.Sprintf("This will %s cluster %s.", verb, joinNameID(target.ClusterName, target.ClusterID)), yes)
		if err != nil {
			return err
		}
		if !ok {
			logging.Info("%s cancelled", strings.ToUpper(verb[:1])+verb[1:])
			return nil
		}
	}

	opts := clusteragent.DrainOptions{
		BackendNS:       backendNS,
		ExpectClusterID: expect,
		DryRun:          dryRun,
		Force:           force,
		Timeout:         timeout,
	}

	var res *clusteragent.DrainResult
	if drain {
		res, err = m.Drain(ctx, opts)
	} else {
		res, err = m.Undrain(ctx, opts)
	}
	return finishDrain(cmd, res, drain, err)
}

// finishDrain renders a drain/undrain result and returns the (possibly non-nil)
// error. A partial result (e.g. ConfigChanged but the rollout trigger failed) is
// still emitted so --json callers can see the partial state, mirroring finishKill.
func finishDrain(cmd *cobra.Command, res *clusteragent.DrainResult, drain bool, err error) error {
	if res == nil {
		return err
	}
	if IsJSONOutput() {
		if jsonErr := OutputJSON(res); jsonErr != nil {
			return jsonErr
		}
		return err
	}
	printDrainResult(cmd, res, drain)
	return err
}

func runClusterAgentKillFunction(cmd *cobra.Command, args []string) error {
	functionID := args[0]
	versionID := ""
	if len(args) == 2 {
		versionID = args[1]
	}

	m, err := newAgentMaintainer(cmd)
	if err != nil {
		return err
	}

	backendNS, _ := cmd.Flags().GetString(flagBackendNamespace)
	dryRun, _ := cmd.Flags().GetBool(flagDryRun)
	yes, _ := cmd.Flags().GetBool(flagYes)
	force, _ := cmd.Flags().GetBool(flagForce)
	reason, _ := cmd.Flags().GetString(flagReason)
	expect, _ := cmd.Flags().GetString(flagExpectClusterID)

	ctx := context.Background()

	if !dryRun {
		target, err := m.ResolveCluster(ctx, backendNS)
		if err != nil {
			return err
		}
		if err := checkExpectedCluster(target, expect); err != nil {
			return err
		}
		ok, err := confirmSimple(cmd, fmt.Sprintf("This will force-terminate %s on cluster %s.", functionLabel(functionID, versionID), joinNameID(target.ClusterName, target.ClusterID)), yes)
		if err != nil {
			return err
		}
		if !ok {
			logging.Info("kill-function cancelled")
			return nil
		}
	}

	res, err := m.KillFunction(ctx, functionID, versionID, clusteragent.KillOptions{
		BackendNS:       backendNS,
		ExpectClusterID: expect,
		Reason:          reason,
		DryRun:          dryRun,
		Force:           force,
	})
	return finishKill(cmd, res, err)
}

func runClusterAgentKillAll(cmd *cobra.Command, _ []string) error {
	m, err := newAgentMaintainer(cmd)
	if err != nil {
		return err
	}

	backendNS, _ := cmd.Flags().GetString(flagBackendNamespace)
	dryRun, _ := cmd.Flags().GetBool(flagDryRun)
	yes, _ := cmd.Flags().GetBool(flagYes)
	force, _ := cmd.Flags().GetBool(flagForce)
	reason, _ := cmd.Flags().GetString(flagReason)
	expect, _ := cmd.Flags().GetString(flagExpectClusterID)
	confirm, _ := cmd.Flags().GetString(flagConfirm)

	ctx := context.Background()

	target, err := m.ResolveCluster(ctx, backendNS)
	if err != nil {
		return err
	}
	if err := checkExpectedCluster(target, expect); err != nil {
		return err
	}

	if !dryRun {
		preview, err := m.KillAll(ctx, clusteragent.KillOptions{BackendNS: backendNS, ExpectClusterID: expect, DryRun: true})
		if err != nil {
			return err
		}
		printKillAllPreview(cmd, target, preview)
		if len(preview.Affected) == 0 {
			logging.Info("No functions running on the cluster; nothing to do.")
			return nil
		}

		// Confirm against the cluster name when available: it is far easier to type
		// correctly than the GUID cluster id. Fall back to the id only when the
		// NVCFBackend has no name.
		confirmValue, confirmLabel := target.ClusterName, "cluster name"
		if confirmValue == "" {
			confirmValue, confirmLabel = target.ClusterID, "cluster id"
		}
		if confirmValue == "" {
			return fmt.Errorf("cannot determine a cluster name or id to confirm against; the NVCFBackend has neither a clusterName nor clusterID")
		}
		ok, err := confirmTypeIn(cmd, confirmValue, confirmLabel, confirm, yes)
		if err != nil {
			return err
		}
		if !ok {
			logging.Info("kill-all cancelled")
			return nil
		}
	}

	// Re-lists ICMSRequests from scratch; requests that arrived or were deleted
	// between the preview and this call are silently included or absent respectively.
	// This is an inherent TOCTOU in the preview-then-confirm design.
	res, err := m.KillAll(ctx, clusteragent.KillOptions{
		BackendNS:       backendNS,
		ExpectClusterID: expect,
		Reason:          reason,
		DryRun:          dryRun,
		Force:           force,
	})
	return finishKill(cmd, res, err)
}

// finishKill renders a kill result and returns the (possibly aggregate) error.
// On a partial failure the populated result is still printed first.
func finishKill(cmd *cobra.Command, res *clusteragent.KillResult, err error) error {
	if res == nil {
		return err
	}
	if IsJSONOutput() {
		if jsonErr := OutputJSON(res); jsonErr != nil {
			return jsonErr
		}
		return err
	}
	printKillResult(cmd, res)
	return err
}

// checkExpectedCluster enforces the optional --expect-cluster-id guard in the
// cmd layer so a mismatch aborts before prompting. The maintainer verifies again
// for defense in depth.
func checkExpectedCluster(target *clusteragent.ClusterTarget, expect string) error {
	if expect == "" {
		return nil
	}
	if expect == target.ClusterID || expect == target.ClusterName {
		return nil
	}
	return fmt.Errorf("refusing to proceed: --expect-cluster-id %q does not match the connected cluster %s; check --compute-plane-context", expect, joinNameID(target.ClusterName, target.ClusterID))
}

// confirmSimple prompts for a y/N confirmation, returning true when the operator
// agrees or --yes was passed. It reads from cmd.InOrStdin so it is testable.
func confirmSimple(cmd *cobra.Command, message string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s Proceed? [y/N]: ", message)
	resp, err := readLine(cmd)
	if err != nil {
		return false, err
	}
	resp = strings.ToLower(strings.TrimSpace(resp))
	return resp == "y" || resp == "yes", nil
}

// confirmTypeIn requires the operator to type the expected cluster identifier
// (named by expectedLabel, e.g. "cluster name"). With --yes it instead requires
// --confirm to equal that value; there is no plain bypass.
func confirmTypeIn(cmd *cobra.Command, expected, expectedLabel, confirmFlag string, yes bool) (bool, error) {
	if yes {
		if confirmFlag == "" {
			return false, fmt.Errorf("kill-all with --yes also requires --confirm <%s> (%q)", expectedLabel, expected)
		}
		if confirmFlag != expected {
			return false, fmt.Errorf("--confirm %q does not match the %s %q", confirmFlag, expectedLabel, expected)
		}
		return true, nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Type the %s (%s) to confirm: ", expectedLabel, expected)
	resp, err := readLine(cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(resp) == expected, nil
}

func readLine(cmd *cobra.Command) (string, error) {
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read confirmation: %w", err)
	}
	return line, nil
}

func functionLabel(functionID, versionID string) string {
	if versionID == "" {
		return fmt.Sprintf("all versions of function %s", functionID)
	}
	return fmt.Sprintf("function %s version %s", functionID, versionID)
}

func printDrainResult(cmd *cobra.Command, res *clusteragent.DrainResult, drain bool) {
	w := cmd.OutOrStdout()
	verb := "Drain"
	if !drain {
		verb = "Undrain"
	}
	prefix := ""
	if res.DryRun {
		prefix = "[dry-run] "
	}
	fmt.Fprintf(w, "%s%s cluster %s\n", prefix, verb, joinNameID(res.ClusterName, res.ClusterID))
	if !res.ConfigChanged {
		fmt.Fprintln(w, "  already in the requested state; no change")
		return
	}
	if res.DryRun {
		fmt.Fprintln(w, "  would update agent-config and restart NVCA")
		return
	}
	if drain {
		fmt.Fprintf(w, "  agent-config updated (maintenanceMode=%s); NVCA restart triggered\n", orDash(res.Mode))
	} else {
		fmt.Fprintln(w, "  agent-config updated (maintenance cleared); NVCA restart triggered")
	}
	switch {
	case res.RolloutComplete:
		fmt.Fprintln(w, "  rollout complete")
	case res.Message != "":
		fmt.Fprintf(w, "  %s\n", res.Message)
	}
}

func printKillAllPreview(cmd *cobra.Command, target *clusteragent.ClusterTarget, preview *clusteragent.KillResult) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Cluster: %s\n", joinNameID(target.ClusterName, target.ClusterID))
	fmt.Fprintf(w, "About to force-terminate %d request(s) in namespace %s:\n", len(preview.Affected), preview.RequestsNamespace)
	for _, r := range preview.Affected {
		fmt.Fprintf(w, "  function=%s version=%s (%s/%s)\n", orDash(r.FunctionID), orDash(r.FunctionVersionID), r.Namespace, r.Name)
	}
}

func printKillResult(cmd *cobra.Command, res *clusteragent.KillResult) {
	w := cmd.OutOrStdout()
	prefix := ""
	verbed := "terminated"
	if res.DryRun {
		prefix = "[dry-run] "
		verbed = "would terminate"
	}
	fmt.Fprintf(w, "%s%s %d request(s) in namespace %s\n", prefix, verbed, len(res.Affected)-res.FailedCount, res.RequestsNamespace)
	if res.Reason != "" {
		fmt.Fprintf(w, "  reason: %s\n", res.Reason)
	}
	for _, r := range res.Affected {
		status := "deleted"
		if res.DryRun {
			status = "would delete"
		}
		if r.Error != "" {
			status = "FAILED: " + r.Error
		}
		fmt.Fprintf(w, "  %s/%s  function=%s version=%s  [%s]\n", r.Namespace, r.Name, orDash(r.FunctionID), orDash(r.FunctionVersionID), status)
	}
	if res.FailedCount > 0 {
		fmt.Fprintf(w, "%d of %d request(s) failed\n", res.FailedCount, len(res.Affected))
	}
}
