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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/selfhosted/teardown"
)

var (
	downClusterName                 string
	downNCAID                       string
	downAll                         bool
	downDrainActive                 bool
	downRemovePersistent            bool
	downForceWithRegisteredClusters bool
	downPlanOnly                    bool
	downConfirm                     bool
	downKeepNamespaces              bool
	downAllConcurrency              int
)

const (
	downPlaneCompute = "compute-plane"
	downPlaneControl = "control-plane"
)

var selfHostedDownCmd = &cobra.Command{
	Use:          "down",
	Short:        "Orchestrator: drain → uninstall compute plane → unregister cluster (mirrors up)",
	SilenceUsage: true,
	RunE:         runSelfHostedDown,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedDownCmd)
	selfHostedDownCmd.Flags().StringVar(&downClusterName, "cluster-name", "",
		"Cluster to tear down")
	selfHostedDownCmd.Flags().StringVar(&downNCAID, "nca-id", "nvcf-default",
		"NCA ID (account) the cluster was registered under (mirrors `up --nca-id`)")
	selfHostedDownCmd.Flags().BoolVar(&downAll, "all", false,
		"Tear down ALL registered compute planes + control plane")
	selfHostedDownCmd.Flags().BoolVar(&downDrainActive, "drain-active", false,
		"Remove ACTIVE function deployments before uninstall (interactive prompt unless --confirm)")
	selfHostedDownCmd.Flags().BoolVar(&downRemovePersistent, "remove-persistent", false,
		"Also delete PVCs (default: preserve data)")
	selfHostedDownCmd.Flags().BoolVar(&downForceWithRegisteredClusters, "force-with-registered-clusters", false,
		"Allow control-plane teardown while compute planes are still registered")
	selfHostedDownCmd.Flags().BoolVar(&downPlanOnly, "plan-only", false,
		"Dry-run: emit planned phase sequence + helm uninstall commands without changing state")
	selfHostedDownCmd.Flags().BoolVar(&downConfirm, "confirm", false,
		"Required when --remove-persistent is set or for non-TTY runs")
	selfHostedDownCmd.Flags().BoolVar(&downKeepNamespaces, "keep-namespaces", false,
		"Don't delete the helm-managed namespaces")
	selfHostedDownCmd.Flags().IntVar(&downAllConcurrency, "all-concurrency", 4,
		"Max parallel compute-plane teardowns when --all")
}

func runSelfHostedDown(c *cobra.Command, _ []string) error {
	if downClusterName == "" && !downAll {
		return fmt.Errorf("either --cluster-name or --all is required")
	}
	if downClusterName != "" && downAll {
		return fmt.Errorf("--cluster-name and --all are mutually exclusive")
	}
	if downRemovePersistent && !downConfirm {
		return fmt.Errorf("--remove-persistent requires --confirm")
	}

	return runDown(c)
}

func runDown(c *cobra.Command) error {
	ctx := c.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now().UTC()

	sink, _, err := progress.SelectRenderer(c.ErrOrStderr(), progress.RenderOpts{
		JSON:       selfHostedJSON,
		Plain:      selfHostedPlain,
		Accessible: selfHostedAccessible,
		Cluster:    downClusterName,
		Stack:      selfHostedStack,
	})
	if err != nil {
		return err
	}
	defer func() { _ = sink.Close() }()

	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}

	// --plan-only short-circuit: emit Planned + Final{PlanOnly:true}, no cluster
	// mutations.
	if downPlanOnly {
		return runDownPlanOnly(ctx, sink, started)
	}

	return runDownPhases(c, ctx, sink, started)
}

// runDownPlanOnly emits the phase plan and returns without touching any
// cluster or SIS resources.
func runDownPlanOnly(ctx context.Context, sink progress.EventSink, started time.Time) error {
	plane := downPlaneCompute
	if downAll {
		plane = downPlaneControl
	}

	releases := defaultDownReleases(plane)
	plan, err := teardown.Plan(teardown.PlanOpts{
		Plane:       plane,
		ClusterName: downClusterName,
		KubeContext: selfHostedComputePlaneContext,
		Releases:    releases,
	})
	if err != nil {
		return err
	}

	phases := make([]progress.PlannedPhase, 0, len(plan.Phases))
	for _, p := range plan.Phases {
		phases = append(phases, progress.PlannedPhase{Num: p.Num, Name: p.Name, ETASec: p.EstSec})
	}
	_ = sink.Emit(ctx, progress.Planned{
		Cluster:       downClusterName,
		Stack:         selfHostedStack,
		Phases:        phases,
		TotalETASec:   plan.TotalEstSec,
		WillUninstall: plan.WillUninstall,
	})
	_ = sink.Emit(ctx, progress.Final{
		PlanOnly: true,
		Success:  true,
		Duration: time.Since(started),
	})
	return nil
}

// runDownPhases executes the real teardown: helmfile destroy + cluster
// unregister. Drain is wired with DrainModeSkip in this batch because
// FunctionDeploymentLister is not yet exposed on the SIS client
// (deferred to M+11.G follow-up).
//
// TODO(M+11.G): wire teardown.Drain with DrainModeForce/Prompt when the
// SIS function-deployment API surface is available.
// TODO(M+11.H/--remove-persistent): call teardown.RemovePersistent after
// helmfile destroy when downRemovePersistent is true.
// TODO(M+11.H/--keep-namespaces): skip namespace deletion when downKeepNamespaces
// is true.
func runDownPhases(c *cobra.Command, ctx context.Context, sink progress.EventSink, started time.Time) error {
	helmRuntimeMode, err := resolveSelfHostedHelmRuntimeMode(ctx)
	if err != nil {
		emitDownFinal(ctx, sink, started, false)
		return fmt.Errorf("resolve Helm runtime mode: %w", err)
	}

	var phaseErr error
	if downClusterName != "" {
		phaseErr = runDownNamedClusterPhases(c, ctx, sink, helmRuntimeMode)
	} else if downAll {
		phaseErr = runDownAllClustersPhases(c, ctx, sink, helmRuntimeMode)
	}
	if phaseErr != nil {
		emitDownFinal(ctx, sink, started, false)
		return phaseErr
	}

	emitDownFinal(ctx, sink, started, true)
	return nil
}

func runDownNamedClusterPhases(c *cobra.Command, ctx context.Context, sink progress.EventSink, helmRuntimeMode selfhosted.HelmRuntimeMode) error {
	skipClusterRows, err := skipClusterRowsForLocalAbsentControlPlane(ctx)
	if err != nil {
		return err
	}
	if err := runDownComputePlaneForCluster(c, ctx, sink, downClusterName, helmRuntimeMode, skipClusterRows); err != nil {
		return err
	}
	if skipClusterRows {
		return nil
	}

	remaining, err := listRegisteredClusters(ctx, resolveICMSURL(selfHostedICMSURL), downNCAID)
	if err != nil {
		return fmt.Errorf("list registered clusters after unregister: %w", err)
	}
	if len(remaining) > 0 {
		return nil
	}
	return runDownControlPlane(c, ctx, sink, helmRuntimeMode)
}

func runDownAllClustersPhases(c *cobra.Command, ctx context.Context, sink progress.EventSink, helmRuntimeMode selfhosted.HelmRuntimeMode) error {
	skipClusterRows, err := skipClusterRowsForLocalAbsentControlPlane(ctx)
	if err != nil {
		return err
	}
	if skipClusterRows {
		clusters, err := localDownFallbackClusterNames(ctx)
		if err != nil {
			return err
		}
		for _, clusterName := range clusters {
			if err := runDownComputePlaneForCluster(c, ctx, sink, clusterName, helmRuntimeMode, true); err != nil {
				return err
			}
		}
		return nil
	}

	clusters, err := listRegisteredClusters(ctx, resolveICMSURL(selfHostedICMSURL), downNCAID)
	if err != nil {
		return fmt.Errorf("list registered clusters: %w", err)
	}
	for _, cl := range clusters {
		if err := runDownComputePlaneForCluster(c, ctx, sink, registeredClusterName(cl), helmRuntimeMode, false); err != nil {
			return err
		}
	}
	return runDownControlPlane(c, ctx, sink, helmRuntimeMode)
}

func localDownFallbackClusterNames(ctx context.Context) ([]string, error) {
	resolved, err := selfhosted.ResolveStack(ctx, selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		return nil, fmt.Errorf("resolve stack: %w", err)
	}
	return clusterNamesFromStackOut(resolved.Path)
}

func clusterNamesFromStackOut(stackPath string) ([]string, error) {
	outDir := filepath.Join(stackPath, "out")
	suffixes := []string{
		"-nvca-values.yaml",
		"-nvca-values.yml",
		"-register-values.yaml",
		"-register-values.yml",
	}
	names := make(map[string]bool)
	for _, suffix := range suffixes {
		matches, err := filepath.Glob(filepath.Join(outDir, "*"+suffix))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			base := filepath.Base(match)
			name := strings.TrimSuffix(base, suffix)
			if name != "" && name != base {
				names[name] = true
			}
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func registeredClusterName(cl client.SISCluster) string {
	if cl.ClusterName != "" {
		return cl.ClusterName
	}
	return cl.ClusterID
}

func emitDownFinal(ctx context.Context, sink progress.EventSink, started time.Time, success bool) {
	_ = sink.Emit(ctx, progress.Final{
		Success:  success,
		Duration: time.Since(started),
	})
}

func runDownComputePlaneForCluster(c *cobra.Command, ctx context.Context, sink progress.EventSink, clusterName string, helmRuntimeMode selfhosted.HelmRuntimeMode, skipClusterRow bool) error {
	// Phase 2: uninstall compute plane.
	p2 := time.Now().UTC()
	_ = sink.Emit(ctx, progress.PhaseStarted{Num: 2, Name: "uninstall-compute-plane", StartedAt: p2})

	resolved, err := selfhosted.ResolveStack(ctx, selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		return fmt.Errorf("resolve stack: %w", err)
	}

	helmfileFile, selector := computePlaneTarget(resolved.Path)

	// The compute-plane helmfile (helmfile-nvca-operator.yaml.gotmpl) reads
	// CLUSTER_ID/CLUSTER_GROUP_ID/IDENTITY_SOURCE/CLUSTER_REGION from env
	// during template render. Without them, the helmfile filters out the
	// nvca-operator release entirely, and `helmfile destroy` reports "no
	// releases found". `up` passes them after register; for `down` we
	// recover the values from the register-values.yaml that `up` wrote
	// at <stack>/out/<cluster>-register-values.yaml.
	extra := []string{
		"CLUSTER_NAME=" + clusterName,
		"NCA_ID=" + downNCAID,
	}
	if rv, err := readRegisterValuesYAML(resolved.Path, clusterName); err == nil {
		extra = append(extra,
			"CLUSTER_ID="+rv.ClusterID,
			"CLUSTER_GROUP_ID="+rv.ClusterGroupID,
			"IDENTITY_SOURCE="+rv.IdentitySource,
			"CLUSTER_REGION="+rv.Region,
		)
	}

	if err := teardown.Destroy(teardown.DestroyOpts{
		Plane:           downPlaneCompute,
		ClusterName:     clusterName,
		KubeContext:     selfHostedComputePlaneContext,
		StackPath:       resolved.Path,
		HelmfileFile:    helmfileFile,
		Selector:        selector,
		Env:             selfHostedEnv,
		HelmRuntimeMode: helmRuntimeMode,
		Stdout:          c.OutOrStdout(),
		Stderr:          c.ErrOrStderr(),
		Ctx:             ctx,
		ExtraEnv:        extra,
	}, sink); err != nil {
		return fmt.Errorf("helmfile destroy compute-plane: %w", err)
	}
	_ = sink.Emit(ctx, progress.PhaseCompleted{
		Num:      2,
		Name:     "uninstall-compute-plane",
		Duration: time.Since(p2),
	})

	// Phase 3: unregister cluster from ICMS.
	p3 := time.Now().UTC()
	_ = sink.Emit(ctx, progress.PhaseStarted{Num: 3, Name: "remove-cluster-row", StartedAt: p3})
	if skipClusterRow {
		_ = sink.Emit(ctx, progress.PhaseCompleted{
			Num:      3,
			Name:     "remove-cluster-row",
			Duration: time.Since(p3),
		})
		return nil
	}

	icmsURL := resolveICMSURL(selfHostedICMSURL)
	deleter, closeDeleter, err := newClusterDeleterForDown(icmsURL)
	if err != nil {
		return fmt.Errorf("constructing cluster deleter: %w", err)
	}
	defer closeDeleter()

	if err := teardown.Unregister(ctx, deleter, icmsURL, clusterName); err != nil {
		return fmt.Errorf("unregister cluster: %w", err)
	}
	_ = sink.Emit(ctx, progress.PhaseCompleted{
		Num:      3,
		Name:     "remove-cluster-row",
		Duration: time.Since(p3),
	})

	return nil
}

func skipClusterRowsForLocalAbsentControlPlane(ctx context.Context) (bool, error) {
	if !strings.EqualFold(selfHostedEnv, "local") {
		return false, nil
	}
	installed, err := downControlPlaneInstalled(ctx, selfHostedControlPlaneContext)
	if err != nil {
		return false, fmt.Errorf("check local control-plane releases: %w", err)
	}
	return !installed, nil
}

var downControlPlaneInstalled = func(ctx context.Context, kubeContext string) (bool, error) {
	args := []string{}
	if kubeContext != "" {
		args = append(args, "--kube-context", kubeContext)
	}
	args = append(args, "list", "-A", "-q")
	cmd := exec.CommandContext(ctx, "helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("helm list: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return downOutputHasControlPlaneRelease(string(out)), nil
}

func downOutputHasControlPlaneRelease(output string) bool {
	names := downControlPlaneReleaseNames()
	for _, line := range strings.Split(output, "\n") {
		if names[strings.TrimSpace(line)] {
			return true
		}
	}
	return false
}

func downControlPlaneReleaseNames() map[string]bool {
	names := make(map[string]bool)
	for _, rel := range defaultDownReleases(downPlaneControl) {
		if rel.Name == "eg" {
			continue
		}
		names[rel.Name] = true
	}
	return names
}

func runDownControlPlane(c *cobra.Command, ctx context.Context, sink progress.EventSink, helmRuntimeMode selfhosted.HelmRuntimeMode) error {
	p4 := time.Now().UTC()
	_ = sink.Emit(ctx, progress.PhaseStarted{Num: 4, Name: "uninstall-control-plane", StartedAt: p4})

	resolved, err := selfhosted.ResolveStack(ctx, selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		return fmt.Errorf("resolve stack: %w", err)
	}

	if err := teardown.Destroy(teardown.DestroyOpts{
		Plane:           downPlaneControl,
		KubeContext:     selfHostedControlPlaneContext,
		StackPath:       resolved.Path,
		Env:             selfHostedEnv,
		HelmRuntimeMode: helmRuntimeMode,
		Stdout:          c.OutOrStdout(),
		Stderr:          c.ErrOrStderr(),
		Ctx:             ctx,
	}, sink); err != nil {
		return fmt.Errorf("helmfile destroy control-plane: %w", err)
	}

	_ = sink.Emit(ctx, progress.PhaseCompleted{
		Num:      4,
		Name:     "uninstall-control-plane",
		Duration: time.Since(p4),
	})
	return nil
}

type registeredClusterLister interface {
	ListClusters(ctx context.Context, sisURL, ncaID string) ([]client.SISCluster, error)
}

func listRegisteredClusters(ctx context.Context, icmsURL, ncaID string) ([]client.SISCluster, error) {
	deleter, closeDeleter, err := newClusterDeleterForDown(icmsURL)
	if err != nil {
		return nil, fmt.Errorf("constructing cluster lister: %w", err)
	}
	defer closeDeleter()

	lister, ok := deleter.(registeredClusterLister)
	if !ok {
		return nil, fmt.Errorf("cluster client cannot list registered clusters")
	}
	return lister.ListClusters(ctx, icmsURL, ncaID)
}

// newClusterDeleterForDown is a package-level seam so tests can inject a fake
// ClusterDeleter without hitting a real ICMS endpoint. Production callers use
// the default factory which wraps *client.Client.
var newClusterDeleterForDown = func(icmsURL string) (teardown.ClusterDeleter, func(), error) {
	cfg, err := client.LoadConfig()
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to load client config: %w", err)
	}
	c, err := client.NewClient(cfg)
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to create client: %w", err)
	}
	return c, func() { _ = c.Close() }, nil
}

// defaultDownReleases returns the hardcoded release list for --plan-only mode.
// TODO(M+11.J): enumerate releases by walking helmfile.d/ in the resolved stack.
//
// Names + namespaces match the actual nvcf-self-managed-stack releases verified
// on mcamp-dev-vm (iter #4 post-up): release names from `helm ls -A` and
// namespaces from `kubectl get all -A | grep helm-`. The reverse-DAG order
// for control-plane intentionally tears down dependents before deps.
func defaultDownReleases(plane string) []teardown.ReleaseRef {
	if plane == downPlaneControl {
		return []teardown.ReleaseRef{
			// Routes / gateway-managed services (depend on api / reval)
			{Name: "ingress", Namespace: "envoy-gateway-system"},
			// Compute-plane consumers of nvcf-api
			{Name: "invocation-service", Namespace: "nvcf"},
			{Name: "grpc-proxy", Namespace: "nvcf"},
			// nvcf-api stack
			{Name: "api", Namespace: "nvcf"},
			{Name: "notary-service", Namespace: "nvcf"},
			{Name: "reval", Namespace: "nvcf"},
			{Name: "ess-api", Namespace: "ess"},
			// Auth + secrets (depend on nats + openbao)
			{Name: "admin-issuer-proxy", Namespace: "api-keys"},
			{Name: "api-keys", Namespace: "api-keys"},
			{Name: "nats-auth-callout-service", Namespace: "nats-system"},
			// Data plane (depend on no nvcf release; tear down last)
			{Name: "openbao-server", Namespace: "vault-system"},
			{Name: "cassandra", Namespace: "cassandra-system"},
			{Name: "nats", Namespace: "nats-system"},
			{Name: "sis", Namespace: "sis"},
			// Gateway controller (uninstalled by ncp-local-cluster make destroy)
			{Name: "eg", Namespace: "envoy-gateway-system"},
		}
	}
	// Compute plane: only nvca-operator. Workers are operator-managed pods,
	// not a separate helm release (verified iter #2 + #4 on mcamp-dev-vm).
	return []teardown.ReleaseRef{
		{Name: "nvca-operator", Namespace: "nvca-operator"},
	}
}

// registerValuesYAML mirrors the subset of the YAML schema written by
// writeRegisterValuesYAML in cmd/self_hosted_up.go. Down reads it back to
// recover the cluster IDs that the compute-plane helmfile templates need as env
// vars at destroy time; endpoint values are intentionally ignored.
type registerValuesYAML struct {
	ClusterID      string
	ClusterGroupID string
	IdentitySource string
	NCAID          string
	Region         string
}

// readRegisterValuesYAML loads <stack>/out/<cluster>-register-values.yaml that
// `up` wrote at register time. Tiny hand-rolled parser — the file is a small
// helm values handoff emitted by writeRegisterValuesYAML and we don't want to
// drag a YAML parser in for one file. Returns ErrNotExist if the file is missing
// (expected when down is run against a never-up'd cluster).
//
// Accepts both the post-`ff9aaf7` shape (top-level capital-D `clusterID:` etc.)
// and the pre-`ff9aaf7` legacy shape (nested `selfManaged.clusterId` lowercase-d
// under indentation). Down was shipped on the legacy shape and may encounter
// register-values files written by older `up` runs against an existing cluster
// that hasn't been re-registered yet, so honoring both keeps the read forgiving.
func readRegisterValuesYAML(stackPath, clusterName string) (*registerValuesYAML, error) {
	var body []byte
	var err error
	for _, path := range []string{nvcaValuesPath(stackPath, clusterName), legacyRegisterValuesPath(stackPath, clusterName)} {
		body, err = os.ReadFile(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	out := &registerValuesYAML{}
	for _, ln := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(ln)
		switch {
		// Post-ff9aaf7 shape (top-level, capital-D).
		case strings.HasPrefix(line, "clusterID:"):
			out.ClusterID = strings.TrimSpace(strings.TrimPrefix(line, "clusterID:"))
		case strings.HasPrefix(line, "clusterGroupID:"):
			out.ClusterGroupID = strings.TrimSpace(strings.TrimPrefix(line, "clusterGroupID:"))
		case strings.HasPrefix(line, "ncaID:"):
			out.NCAID = strings.TrimSpace(strings.TrimPrefix(line, "ncaID:"))
		// Legacy shape (nested under selfManaged:, lowercase-d). Kept for
		// forward compatibility while older register-values files exist.
		case strings.HasPrefix(line, "clusterId:"):
			out.ClusterID = strings.TrimSpace(strings.TrimPrefix(line, "clusterId:"))
		case strings.HasPrefix(line, "clusterGroupId:"):
			out.ClusterGroupID = strings.TrimSpace(strings.TrimPrefix(line, "clusterGroupId:"))
		case strings.HasPrefix(line, "ncaId:"):
			out.NCAID = strings.TrimSpace(strings.TrimPrefix(line, "ncaId:"))
		// Both shapes share these keys.
		case strings.HasPrefix(line, "identitySource:"):
			out.IdentitySource = strings.TrimSpace(strings.TrimPrefix(line, "identitySource:"))
		case strings.HasPrefix(line, "region:"):
			out.Region = strings.TrimSpace(strings.TrimPrefix(line, "region:"))
		}
	}
	return out, nil
}
