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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/selfhosted/managementtls"
	"nvcf-cli/internal/selfhosted/nvca"
	"nvcf-cli/internal/selfhosted/reachability"
)

var (
	computePlaneInstallValues       string
	computePlaneInstallKubeContext  string
	computePlaneInstallClusterName  string
	computePlaneInstallNCAID        string
	computePlaneRegisterDryRun      bool
	computePlaneRegisterProfile     string
	computePlaneRegisterClusterName string
	computePlaneRegisterKubeContext string
	computePlaneRegisterRegion      string
	computePlaneRegisterOutput      string
)

var selfHostedComputePlaneCmd = &cobra.Command{
	Use:          "compute-plane",
	Short:        "Manage self-hosted compute-plane clusters",
	SilenceUsage: true,
}

var selfHostedComputePlaneInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install the compute-plane NVCA operator from a values file",
	RunE:         runSelfHostedComputePlaneInstall,
	SilenceUsage: true,
}

var selfHostedComputePlaneRegisterCmd = &cobra.Command{
	Use:          "register",
	Short:        "Register a compute-plane cluster with a self-hosted control plane",
	RunE:         runSelfHostedComputePlaneRegister,
	SilenceUsage: true,
}

var computePlaneRegisterReachabilityCheck = func(ctx context.Context, req reachability.CheckRequest) error {
	return reachability.Check(ctx, req)
}

func init() {
	selfHostedCmd.AddCommand(selfHostedComputePlaneCmd)
	selfHostedComputePlaneCmd.AddCommand(selfHostedComputePlaneInstallCmd)
	selfHostedComputePlaneCmd.AddCommand(selfHostedComputePlaneRegisterCmd)
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallValues, "values", "", "nvca-operator values file to install")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallKubeContext, "kube-context", "", "kubeconfig context for the compute-plane cluster")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallClusterName, "cluster-name", "", "Cluster name override when the values file does not set clusterName")
	selfHostedComputePlaneInstallCmd.Flags().StringVar(&computePlaneInstallNCAID, "nca-id", "nvcf-default", "NCA ID override when the values file does not set ncaID")

	selfHostedComputePlaneRegisterCmd.Flags().BoolVar(&computePlaneRegisterDryRun, "dry-run", false, "Validate registration inputs without mutating SIS or writing values")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterProfile, "control-plane-profile", "", "Control-plane profile file")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterClusterName, "cluster-name", "", "Compute-plane cluster name")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterKubeContext, "kube-context", "", "kubeconfig context for the compute-plane cluster")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterRegion, "region", "us-west-1", "Compute-plane cluster region")
	selfHostedComputePlaneRegisterCmd.Flags().StringVar(&computePlaneRegisterOutput, "output", "", "Output path for generated nvca-operator values")
}

func runSelfHostedComputePlaneInstall(c *cobra.Command, _ []string) error {
	if computePlaneInstallValues == "" {
		return fmt.Errorf("--values is required")
	}

	valuesPath, err := filepath.Abs(computePlaneInstallValues)
	if err != nil {
		return fmt.Errorf("resolving values path: %w", err)
	}
	metadata, err := readNVCAValuesMetadata(valuesPath)
	if err != nil {
		return err
	}
	clusterName := firstNonEmpty(computePlaneInstallClusterName, metadata.ClusterName, inferClusterNameFromValuesPath(valuesPath))
	if clusterName == "" {
		return fmt.Errorf("cluster name is required: set clusterName in the values file or pass --cluster-name")
	}
	ncaID := firstNonEmpty(metadata.NCAID, computePlaneInstallNCAID)

	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        selfHostedComputePlaneStack,
		BuiltInOCIRef: builtInComputePlaneStackOCI(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.ErrOrStderr(), ">>> Resolving stack: %s\n", stackDescriptor(resolved))

	helmRuntimeMode, err := resolveSelfHostedHelmRuntimeMode(c.Context())
	if err != nil {
		return fmt.Errorf("resolving helm runtime: %w", err)
	}

	return selfhosted.Render(selfhosted.RenderOptions{
		StackPath:       resolved.Path,
		Env:             selfHostedEnv,
		Apply:           !selfHostedNoApply,
		KubeContext:     computePlaneInstallKubeContext,
		HelmRuntimeMode: helmRuntimeMode,
		Stdout:          c.OutOrStdout(),
		Stderr:          c.ErrOrStderr(),
		Ctx:             c.Context(),
		ExtraEnv:        computePlaneInstallEnv(clusterName, ncaID),
	})
}

type nvcaValuesMetadata struct {
	ClusterName string
	NCAID       string
}

func readNVCAValuesMetadata(path string) (nvcaValuesMetadata, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nvcaValuesMetadata{}, fmt.Errorf("reading values file: %w", err)
	}
	// Strict-decode against the canonical nvca.Values schema (with a lowercase
	// ncaId alias preserved for backward compatibility) so a typo in a known
	// field (for example clusterName mistyped as cluterName) surfaces as an
	// error rather than silently producing an empty cluster identity. The CLI
	// writes this same schema in writeComputePlaneNVCAValues, so any extra
	// keys indicate either a typo or a chart override that should live in a
	// separate helmfile values overlay rather than this file.
	var values nvcaValuesMetadataDoc
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&values); err != nil {
		return nvcaValuesMetadata{}, fmt.Errorf("parsing values file: %w", err)
	}
	return nvcaValuesMetadata{
		ClusterName: values.ClusterName,
		NCAID:       firstNonEmpty(values.NCAID, values.NCAIDLower),
	}, nil
}

// nvcaValuesMetadataDoc is the strict-decode shape for the nvca-operator
// values file. It mirrors nvca.Values but also accepts the lowercase ncaId
// alias the CLI has historically tolerated. Unknown keys fail decode so
// operator typos do not silently zero out cluster identity.
type nvcaValuesMetadataDoc struct {
	ClusterName    string                  `yaml:"clusterName,omitempty"`
	ClusterID      string                  `yaml:"clusterID,omitempty"`
	ClusterGroupID string                  `yaml:"clusterGroupID,omitempty"`
	NCAID          string                  `yaml:"ncaID,omitempty"`
	NCAIDLower     string                  `yaml:"ncaId,omitempty"`
	Region         string                  `yaml:"region,omitempty"`
	SelfManaged    nvca.SelfManagedValues  `yaml:"selfManaged,omitempty"`
	Agent          *nvca.AgentValues       `yaml:"agent,omitempty"`
	AgentConfig    *nvca.AgentConfigValues `yaml:"agentConfig,omitempty"`
}

func stringMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	v, ok := values[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func inferClusterNameFromValuesPath(path string) string {
	base := filepath.Base(path)
	for _, suffix := range []string{"-nvca-values.yaml", "-nvca-values.yml", "-register-values.yaml", "-register-values.yml", ".yaml", ".yml"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return ""
}

func computePlaneInstallEnv(clusterName, ncaID string) []string {
	return []string{
		"CLUSTER_NAME=" + clusterName,
		"NCA_ID=" + ncaID,
	}
}

func runSelfHostedComputePlaneRegister(c *cobra.Command, _ []string) error {
	if err := validateComputePlaneRegisterFlags(); err != nil {
		return err
	}

	validation, selected, err := loadControlPlaneProfileForComputeRegister(computePlaneRegisterProfile, computePlaneRegisterClusterName)
	if err != nil {
		return err
	}
	registrationICMSURL := computePlaneRegisterICMSURL(validation.Profile, selected)
	if err := computePlaneRegisterReachabilityCheck(c.Context(), reachability.CheckRequest{
		TargetClusterName: computePlaneRegisterClusterName,
		ICMSURL:           registrationICMSURL,
		ReValURL:          selected.Endpoints.ReValURL,
		NATSURL:           selected.Endpoints.NATSURL,
		SISHost:           validation.Profile.ControlPlane.Hosts.SIS,
		ReValHost:         validation.Profile.ControlPlane.Hosts.ReVal,
		ProbeHTTP:         shouldProbeComputeRegisterHTTP(selected.Name),
	}); err != nil {
		return err
	}

	identity, err := discoverComputePlaneRegisterIdentity(c.Context(), computePlaneRegisterKubeContext)
	if err != nil {
		return err
	}

	cp := validation.Profile.ControlPlane
	registration, handoff, err := registerComputePlaneCluster(
		c,
		registrationICMSURL,
		cp,
		selected.Endpoints,
		identity,
		validation.Profile.ManagementTLS,
		validation.Profile.TransportTLS,
		cp.Addons.LLM.RequestRouterAddress,
	)
	if err != nil {
		return err
	}

	writeComputePlaneRegisterSummary(c, computePlaneRegisterSummary{
		ControlPlane:        cp,
		Selected:            selected,
		RegistrationICMSURL: registrationICMSURL,
		Identity:            identity,
		Registration:        registration,
		Handoff:             handoff,
		ComputePlaneKubeCtx: computePlaneRegisterKubeContext,
		ComputePlaneRegion:  computePlaneRegisterRegion,
		ComputePlaneCluster: computePlaneRegisterClusterName,
		ComputePlaneDryRun:  computePlaneRegisterDryRun,
	})
	return nil
}

func validateComputePlaneRegisterFlags() error {
	if computePlaneRegisterProfile == "" {
		return fmt.Errorf("--control-plane-profile is required")
	}
	if computePlaneRegisterClusterName == "" {
		return fmt.Errorf("--cluster-name is required")
	}
	return nil
}

type computePlaneClusterIdentity struct {
	OIDCIssuer     string
	JWKS           string
	IdentitySource string
}

func discoverComputePlaneRegisterIdentity(ctx context.Context, kubeContext string) (computePlaneClusterIdentity, error) {
	oidcIssuer, jwks, identitySource, err := fetchClusterIdentity(ctx, kubeContext)
	if err != nil {
		return computePlaneClusterIdentity{}, fmt.Errorf("discovering target cluster identity: %w", err)
	}
	if strings.TrimSpace(oidcIssuer) == "" {
		return computePlaneClusterIdentity{}, fmt.Errorf("discovering target cluster identity: OIDC issuer is empty")
	}
	if strings.TrimSpace(jwks) == "" {
		return computePlaneClusterIdentity{}, fmt.Errorf("discovering target cluster identity: JWKS is empty")
	}
	if identitySource == "" {
		identitySource = "psat"
	}
	return computePlaneClusterIdentity{
		OIDCIssuer:     oidcIssuer,
		JWKS:           jwks,
		IdentitySource: identitySource,
	}, nil
}

func registerComputePlaneCluster(
	c *cobra.Command,
	registrationICMSURL string,
	cp controlplaneprofile.ControlPlane,
	endpoints controlplaneprofile.EndpointScope,
	identity computePlaneClusterIdentity,
	managementTLS controlplaneprofile.ManagementTLS,
	transport controlplaneprofile.TransportTLS,
	requestRouterAddress string,
) (*selfhosted.RegisterResponse, *computePlaneRegisterHandoff, error) {
	if computePlaneRegisterDryRun {
		return nil, nil, nil
	}
	handoff, err := prepareComputePlaneRegisterHandoff(c, computePlaneRegisterClusterName)
	if err != nil {
		return nil, nil, err
	}
	mtls, err := managementtls.TLSConfig(managementTLS)
	if err != nil {
		return nil, nil, fmt.Errorf("building management-API trust: %w", err)
	}
	cc, err := newClusterClientForSelfHostedWithTrust(registrationICMSURL, mtls)
	if err != nil {
		return nil, nil, fmt.Errorf("constructing cluster client: %w", err)
	}
	defer cc.Close()
	registration, err := cc.RegisterCluster(c.Context(), selfhosted.RegisterRequest{
		ClusterName:    computePlaneRegisterClusterName,
		NCAID:          cp.NCAID,
		Region:         computePlaneRegisterRegion,
		JWKS:           identity.JWKS,
		OIDCIssuer:     identity.OIDCIssuer,
		IdentitySource: identity.IdentitySource,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("cluster register: %w", err)
	}
	if err := writeComputePlaneNVCAValues(computePlaneNVCAValuesRequest{
		Path:                 handoff.ValuesPath,
		ClusterName:          computePlaneRegisterClusterName,
		NCAID:                cp.NCAID,
		Region:               computePlaneRegisterRegion,
		IdentitySource:       identity.IdentitySource,
		Registration:         registration,
		Endpoints:            endpoints,
		Hosts:                cp.Hosts,
		TransportTLS:         transport,
		RequestRouterAddress: requestRouterAddress,
	}); err != nil {
		return nil, nil, err
	}
	return registration, handoff, nil
}

type computePlaneRegisterSummary struct {
	ControlPlane        controlplaneprofile.ControlPlane
	Selected            selfhosted.ControlPlaneProfileEndpointScopeSelection
	RegistrationICMSURL string
	Identity            computePlaneClusterIdentity
	Registration        *selfhosted.RegisterResponse
	Handoff             *computePlaneRegisterHandoff
	ComputePlaneKubeCtx string
	ComputePlaneRegion  string
	ComputePlaneCluster string
	ComputePlaneDryRun  bool
}

func writeComputePlaneRegisterSummary(c *cobra.Command, summary computePlaneRegisterSummary) {
	out := c.OutOrStdout()
	fmt.Fprintf(out, "dryRun: %t\n", summary.ComputePlaneDryRun)
	fmt.Fprintf(out, "clusterName: %s\n", summary.ComputePlaneCluster)
	fmt.Fprintf(out, "controlPlaneClusterName: %s\n", summary.ControlPlane.ClusterName)
	fmt.Fprintf(out, "ncaID: %s\n", summary.ControlPlane.NCAID)
	fmt.Fprintf(out, "region: %s\n", summary.ComputePlaneRegion)
	fmt.Fprintf(out, "kubeContext: %s\n", summary.ComputePlaneKubeCtx)
	fmt.Fprintf(out, "endpointScope: %s\n", summary.Selected.Name)
	fmt.Fprintf(out, "icmsURL: %s\n", summary.RegistrationICMSURL)
	fmt.Fprintf(out, "revalURL: %s\n", summary.Selected.Endpoints.ReValURL)
	fmt.Fprintf(out, "natsURL: %s\n", summary.Selected.Endpoints.NATSURL)
	fmt.Fprintf(out, "oidcIssuer: %s\n", summary.Identity.OIDCIssuer)
	fmt.Fprintf(out, "identitySource: %s\n", summary.Identity.IdentitySource)
	if summary.Registration != nil && summary.Handoff != nil {
		fmt.Fprintf(out, "clusterID: %s\n", summary.Registration.ClusterID)
		fmt.Fprintf(out, "clusterGroupID: %s\n", summary.Registration.ClusterGroupID)
		fmt.Fprintln(out, "sisMutation: completed")
		fmt.Fprintf(out, "valuesPath: %s\n", summary.Handoff.ValuesPath)
		fmt.Fprintln(out, "valuesWrite: completed")
		fmt.Fprintln(out, "helmCommand:")
		fmt.Fprintf(out, "  %s\n", shellCommand("helm", "upgrade", "--install", "nvca-operator", summary.Handoff.Chart, "--version", summary.Handoff.Version, "--namespace", "nvca-operator", "--create-namespace", "--values", summary.Handoff.ValuesPath))
		fmt.Fprintln(out, "computePlaneInstallCommand:")
		installArgs := []string{"nvcf", "self-hosted", "compute-plane", "install"}
		if summary.Handoff.StackArg != "" {
			installArgs = append(installArgs, "--compute-plane-stack", summary.Handoff.StackArg)
		}
		installArgs = append(installArgs, "--values", summary.Handoff.ValuesPath)
		if summary.ComputePlaneKubeCtx != "" {
			installArgs = append(installArgs, "--kube-context", summary.ComputePlaneKubeCtx)
		}
		fmt.Fprintf(out, "  %s\n", shellCommand(installArgs...))
	} else {
		fmt.Fprintln(out, "sisMutation: skipped")
		fmt.Fprintln(out, "valuesWrite: skipped")
	}
}

// computePlaneRegisterICMSURL picks the ICMS endpoint the local CLI will use
// to call SIS during cluster register, in priority order:
//
//  1. --icms-url flag (explicit operator override).
//  2. NVCF_ICMS_URL env var.
//  3. NVCF_SIS_URL env var (legacy quickstart name).
//  4. icms_url from the CLI config file (Viper-resolved).
//  5. Compute-reachable scope ICMS URL from the control-plane profile, even when
//     SelectControlPlaneProfileEndpointScope returned in-cluster (because the
//     operator host that runs `nvcf-cli` cannot dial in-cluster service DNS).
//  6. The selected scope's ICMS URL as-is (last-resort fallback; multi-cluster
//     already lands on compute-reachable via SelectControlPlaneProfileEndpointScope).
func computePlaneRegisterICMSURL(profile controlplaneprofile.ControlPlaneProfile, selected selfhosted.ControlPlaneProfileEndpointScopeSelection) string {
	if strings.TrimSpace(selfHostedICMSURL) != "" {
		return selfHostedICMSURL
	}
	if v := os.Getenv("NVCF_ICMS_URL"); strings.TrimSpace(v) != "" {
		return v
	}
	if v := os.Getenv("NVCF_SIS_URL"); strings.TrimSpace(v) != "" {
		return v
	}
	cfg, err := client.LoadConfigWithoutAuth()
	if err == nil && strings.TrimSpace(cfg.ICMSURL) != "" {
		return cfg.ICMSURL
	}
	// When SelectControlPlaneProfileEndpointScope returned in-cluster (single
	// cluster where target cluster name == control-plane cluster name), the
	// resulting URL is a Kubernetes service DNS name (e.g.
	// http://api.sis.svc.cluster.local:8080) that the operator host cannot
	// reach. Prefer the compute-reachable scope if it is populated, so the
	// CLI dials the externally-routable URL instead.
	if selected.Name == selfhosted.EndpointScopeInCluster {
		if external := strings.TrimSpace(profile.ControlPlane.Endpoints.ComputeReachable.ICMSURL); external != "" {
			return external
		}
	}
	return selected.Endpoints.ICMSURL
}

func shouldProbeComputeRegisterHTTP(endpointScope selfhosted.ControlPlaneProfileEndpointScopeName) bool {
	if endpointScope != selfhosted.EndpointScopeComputeReachable {
		return false
	}
	return !strings.EqualFold(selfHostedEnv, "local")
}

func loadControlPlaneProfileForComputeRegister(path, clusterName string) (*controlplaneprofile.ValidationResult, selfhosted.ControlPlaneProfileEndpointScopeSelection, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, fmt.Errorf("reading control-plane profile: %w", err)
	}
	validation, err := controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{Require: controlplaneprofile.RequireAny})
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	selected, err := selfhosted.SelectControlPlaneProfileEndpointScope(validation.Profile, clusterName)
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	validation, err = controlplaneprofile.ParseAndValidate(body, controlplaneprofile.ValidateOptions{
		Require: selfhosted.ControlPlaneProfileRequireModeForEndpointScope(selected.Name),
	})
	if err != nil {
		return nil, selfhosted.ControlPlaneProfileEndpointScopeSelection{}, err
	}
	return validation, selected, nil
}

type computePlaneRegisterHandoff struct {
	StackArg   string
	StackPath  string
	ValuesPath string
	Chart      string
	Version    string
}

func prepareComputePlaneRegisterHandoff(c *cobra.Command, clusterName string) (*computePlaneRegisterHandoff, error) {
	resolved, err := selfhosted.ResolveStack(c.Context(), selfhosted.StackOptions{
		Source:        selfHostedComputePlaneStack,
		BuiltInOCIRef: builtInComputePlaneStackOCI(),
	})
	if err != nil {
		return nil, err
	}
	valuesPath := computePlaneRegisterOutput
	if valuesPath == "" {
		// TODO: resolve to using one path in both CLI and helmfiles.
		valuesPath = filepath.Join(resolved.Path, "out", clusterName+"-register-values.yaml")
	} else {
		valuesPath, err = filepath.Abs(valuesPath)
		if err != nil {
			return nil, fmt.Errorf("resolving output path: %w", err)
		}
	}
	chart, version, err := computePlaneChartFromStack(resolved.Path)
	if err != nil {
		return nil, err
	}
	return &computePlaneRegisterHandoff{
		StackArg:   selfHostedComputePlaneStack,
		StackPath:  resolved.Path,
		ValuesPath: valuesPath,
		Chart:      chart,
		Version:    version,
	}, nil
}

type computePlaneNVCAValuesRequest struct {
	Path                 string
	ClusterName          string
	NCAID                string
	Region               string
	IdentitySource       string
	Registration         *selfhosted.RegisterResponse
	Endpoints            controlplaneprofile.EndpointScope
	Hosts                controlplaneprofile.Hosts
	TransportTLS         controlplaneprofile.TransportTLS
	RequestRouterAddress string
}

func writeComputePlaneNVCAValues(req computePlaneNVCAValuesRequest) error {
	agentConfig, err := transportTLSAgentConfigValues(req.TransportTLS)
	if err != nil {
		return err
	}
	return nvca.WriteFile(req.Path, nvca.Values{
		ClusterName:    req.ClusterName,
		ClusterID:      req.Registration.ClusterID,
		ClusterGroupID: req.Registration.ClusterGroupID,
		NCAID:          req.NCAID,
		Region:         req.Region,
		SelfManaged: nvca.SelfManagedValues{
			IdentitySource:                 req.IdentitySource,
			ICMSServiceURL:                 req.Endpoints.ICMSURL,
			ICMSServiceHostHeaderOverride:  req.Hosts.SIS,
			ReValServiceURL:                req.Endpoints.ReValURL,
			ReValServiceHostHeaderOverride: req.Hosts.ReVal,
			NATSURL:                        req.Endpoints.NATSURL,
			NATSHostOverride:               req.Hosts.NATS,
		},
		Agent:       agentValues(req.RequestRouterAddress),
		AgentConfig: agentConfig,
	})
}

// agentValues renders the control-plane profile's LLM request router address
// (controlPlane.addons.llm.requestRouterAddress) into agent.llm.requestRouterAddress
// so LLM workloads that do not set STARGATE_ADDRESS bootstrap against it. The
// section is omitted when the profile advertises no address.
func agentValues(requestRouterAddress string) *nvca.AgentValues {
	addr := strings.TrimSpace(requestRouterAddress)
	if addr == "" {
		return nil
	}
	return &nvca.AgentValues{LLM: &nvca.AgentLLMValues{RequestRouterAddress: addr}}
}

type transportTLSAgentConfig struct {
	Workload transportTLSWorkloadConfig `yaml:"workload,omitempty"`
}

type transportTLSWorkloadConfig struct {
	TransportTLS *transportTLSConfig `yaml:"transportTLS,omitempty"`
}

type transportTLSConfig struct {
	TrustMode              string `yaml:"trustMode"`
	TrustBundleFingerprint string `yaml:"trustBundleFingerprint,omitempty"`
	TrustBundlePEM         string `yaml:"trustBundlePem,omitempty"`
}

// transportTLSAgentConfigValues renders the control-plane profile's
// transportTls into the NVCA agent config merge fragment (contract C-2). The
// profile transportTls has already been validated by
// controlplaneprofile.ParseAndValidate. The installer image is intentionally not
// rendered by the CLI; the operator defaults it to the resolved NVCA image.
func transportTLSAgentConfigValues(t controlplaneprofile.TransportTLS) (*nvca.AgentConfigValues, error) {
	if strings.TrimSpace(t.TrustMode) == "" {
		return nil, nil
	}
	transportTLS := &transportTLSConfig{TrustMode: t.TrustMode}
	if t.TrustMode == controlplaneprofile.TrustModeBundle {
		transportTLS.TrustBundleFingerprint = t.TrustBundleFingerprint
		transportTLS.TrustBundlePEM = t.TrustBundlePEM
	}
	body, err := yaml.Marshal(transportTLSAgentConfig{
		Workload: transportTLSWorkloadConfig{TransportTLS: transportTLS},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal transport TLS agent config: %w", err)
	}
	return &nvca.AgentConfigValues{MergeConfig: string(body)}, nil
}

func computePlaneChartFromStack(stackPath string) (chart, version string, err error) {
	helmfileDir := filepath.Join(stackPath, "helmfile.d")
	entries, err := os.ReadDir(helmfileDir)
	if err != nil {
		return "", "", fmt.Errorf("reading compute-plane helmfile directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && isHelmfileTemplate(entry.Name()) {
			body, err := os.ReadFile(filepath.Join(helmfileDir, entry.Name()))
			if err != nil {
				return "", "", fmt.Errorf("reading compute-plane nvca helmfile: %w", err)
			}
			if chart, version = parseComputePlaneChart(string(body)); chart != "" && version != "" {
				return chart, version, nil
			}
		}
	}
	return "", "", fmt.Errorf("compute-plane chart reference not found in stack")
}

func isHelmfileTemplate(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yaml.gotmpl")
}

func parseComputePlaneChart(body string) (string, string) {
	var chart, version string
	inNVCARelease := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "- name:"):
			inNVCARelease = strings.TrimSpace(strings.TrimPrefix(line, "- name:")) == "nvca-operator"
			chart = ""
			version = ""
		case !inNVCARelease:
			continue
		case strings.HasPrefix(line, "chart:") && chart == "":
			chart = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "chart:")), "\"'")
		case strings.HasPrefix(line, "version:") && version == "":
			version = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "version:")), "\"'")
		}
		if inNVCARelease && chart != "" && version != "" {
			return chart, version
		}
	}
	return chart, version
}

func shellCommand(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'))
	}) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}
