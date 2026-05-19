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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var clusterRegisterCmd = &cobra.Command{
	Use:          "register",
	Short:        "Register a compute plane cluster with NVCF",
	SilenceUsage: true,
	Long: `Register a new compute plane cluster by fetching its OIDC JWKS
and sending it to ICMS. Returns helm values for nvca-operator install.

The command detects the cluster's OIDC issuer (K8s API server or SPIRE)
and fetches the public JWKS for JWT verification.`,
	RunE: runClusterRegister,
}

var clusterRotateCmd = &cobra.Command{
	Use:          "rotate",
	Short:        "Rotate cluster JWKS in ICMS",
	SilenceUsage: true,
	Long:         `Re-fetch the cluster's JWKS and update ICMS. Used after cert rotation.`,
	RunE:         runClusterRotate,
}

var clusterDeleteCmd = &cobra.Command{
	Use:          "delete",
	Short:        "Delete a cluster registration from ICMS",
	SilenceUsage: true,
	RunE:         runClusterDelete,
}

const (
	clusterFlagNcaID       = "nca-id"
	clusterFlagICMSURL     = "icms-url"
	clusterFlagICMSURLHelp = "ICMS endpoint URL (default: derived from config; env: NVCF_ICMS_URL)"
	clusterFlagNATSURL     = "nats-url"
	clusterFlagNATSURLHelp = "NATS endpoint URL for the compute plane agent (default: derived from ICMS/API URL; env: NVCF_NATS_URL)"
	clusterFlagClusterID   = "cluster-id"

	errFailedToLoadConfig   = "failed to load config: %w"
	errFailedToCreateClient = "failed to create client: %w"
)

var directOIDCHTTPClient = &http.Client{Timeout: 10 * time.Second}

type oidcDiscoveryDocument struct {
	Issuer  string `json:"issuer"`
	JwksURI string `json:"jwks_uri"`
}

func initClusterRegistrationCmds() {
	clusterCmd.AddCommand(clusterRegisterCmd)
	clusterCmd.AddCommand(clusterRotateCmd)
	clusterCmd.AddCommand(clusterDeleteCmd)

	// Register flags
	clusterRegisterCmd.Flags().String("name", "", "Cluster name (required)")
	clusterRegisterCmd.Flags().String(clusterFlagNcaID, "", "NCA/tenant ID (required)")
	clusterRegisterCmd.Flags().String("region", "us-west-1", "Cluster region")
	clusterRegisterCmd.Flags().String("kubeconfig", "", "Path to kubeconfig for target cluster")
	addClusterICMSURLFlags(clusterRegisterCmd)
	clusterRegisterCmd.Flags().String(clusterFlagNATSURL, "", clusterFlagNATSURLHelp)
	_ = clusterRegisterCmd.MarkFlagRequired("name")
	_ = clusterRegisterCmd.MarkFlagRequired(clusterFlagNcaID)

	clusterRegisterCmd.Flags().String("oidc-issuer-url", "", "OIDC issuer URL (overrides auto-detection; skips SPIRE and K8s discovery)")
	clusterRegisterCmd.Flags().Bool("ignore-existing", false, "If cluster already exists, return existing IDs instead of failing")

	clusterRotateCmd.Flags().String(clusterFlagClusterID, "", "Cluster UUID (required)")
	clusterRotateCmd.Flags().String("kubeconfig", "", "Path to kubeconfig for target cluster")
	addClusterICMSURLFlags(clusterRotateCmd)
	clusterRotateCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	_ = clusterRotateCmd.MarkFlagRequired(clusterFlagClusterID)

	clusterDeleteCmd.Flags().String(clusterFlagClusterID, "", "Cluster UUID (required)")
	clusterDeleteCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	clusterDeleteCmd.Flags().Bool("ignore-missing", false, "Exit 0 when the cluster row is already gone (useful for `down` re-runs)")
	addClusterICMSURLFlags(clusterDeleteCmd)
	_ = clusterDeleteCmd.MarkFlagRequired(clusterFlagClusterID)
}

func addClusterICMSURLFlags(cmd *cobra.Command) {
	cmd.Flags().String(clusterFlagICMSURL, "", clusterFlagICMSURLHelp)
}

// discoverSpireOIDC attempts to find a SPIRE OIDC discovery service in the cluster.
// Returns the ClusterIP:Port (e.g., "10.43.x.x:8080") or empty string if not found.
func discoverSpireOIDC(config *client.Config) string {
	// Try the standard Helm chart label first
	cmd := buildKubectlCommand(config, []string{
		"get", "svc", "-A",
		"-l", "app.kubernetes.io/name=spiffe-oidc-discovery-provider",
		"-o", "jsonpath={.items[0].spec.clusterIP}:{.items[0].spec.ports[0].port}",
	})
	output, err := executeCommand(cmd)
	if err == nil && output != "" && output != ":" {
		return output
	}

	// Fallback: scan all services for common SPIRE OIDC names
	cmd = buildKubectlCommand(config, []string{
		"get", "svc", "-A",
		"-o", "jsonpath={range .items[*]}{.metadata.name} {.spec.clusterIP}:{.spec.ports[0].port}\n{end}",
	})
	output, err = executeCommand(cmd)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && (strings.Contains(parts[0], "oidc-discovery") || strings.Contains(parts[0], "spire-oidc")) {
			return parts[1]
		}
	}
	return ""
}

// fetchJWKSFromURL fetches OIDC configuration and JWKS from an arbitrary OIDC
// issuer reachable from inside the cluster. It launches an ephemeral curl pod
// because the OIDC service typically has a ClusterIP only.
func fetchJWKSFromURL(config *client.Config, baseURL string) (issuer string, jwks string, err error) {
	oidcURL := strings.TrimRight(baseURL, "/") + "/.well-known/openid-configuration"

	// Fetch OIDC discovery document via ephemeral pod
	logging.Info("Fetching OIDC discovery from %s ...", oidcURL)
	cmd := buildKubectlCommand(config, []string{
		"run", "nvcf-oidc-probe", "--image=curlimages/curl:latest",
		"--rm", "-i", "--restart=Never", "--quiet",
		"--", "-sf", oidcURL,
	})
	oidcRaw, err := executeCommand(cmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch OIDC config from %s: %w", oidcURL, err)
	}

	var oidcDoc oidcDiscoveryDocument
	if err := json.Unmarshal([]byte(oidcRaw), &oidcDoc); err != nil {
		return "", "", fmt.Errorf("failed to parse OIDC config from %s: %w", oidcURL, err)
	}
	if oidcDoc.Issuer == "" || oidcDoc.JwksURI == "" {
		return "", "", fmt.Errorf("OIDC config at %s missing issuer or jwks_uri", oidcURL)
	}

	// Fetch JWKS
	logging.Info("Fetching JWKS from %s ...", oidcDoc.JwksURI)
	cmd = buildKubectlCommand(config, []string{
		"run", "nvcf-jwks-probe", "--image=curlimages/curl:latest",
		"--rm", "-i", "--restart=Never", "--quiet",
		"--", "-sf", oidcDoc.JwksURI,
	})
	jwksData, err := executeCommand(cmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch JWKS from %s: %w", oidcDoc.JwksURI, err)
	}

	// Validate JSON
	var check json.RawMessage
	if err := json.Unmarshal([]byte(jwksData), &check); err != nil {
		return "", "", fmt.Errorf("JWKS from %s is not valid JSON: %w", oidcDoc.JwksURI, err)
	}

	return oidcDoc.Issuer, jwksData, nil
}

func supportsDirectOIDCDiscovery(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	return host != "kubernetes.default.svc.cluster.local" &&
		host != "kubernetes.default.svc" &&
		host != "kubernetes.default" &&
		!strings.HasSuffix(host, ".svc") &&
		!strings.HasSuffix(host, ".svc.cluster.local")
}

func fetchDirectOIDCJWKS(ctx context.Context, issuerURL string, httpClient *http.Client) (issuer string, jwks string, err error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	logging.Info("Fetching OIDC discovery from issuer %s ...", discoveryURL)

	oidcRaw, err := getHTTPString(ctx, httpClient, discoveryURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch OIDC config from %s: %w", discoveryURL, err)
	}

	var oidcDoc oidcDiscoveryDocument
	if err := json.Unmarshal([]byte(oidcRaw), &oidcDoc); err != nil {
		return "", "", fmt.Errorf("failed to parse OIDC config from %s: %w", discoveryURL, err)
	}
	if oidcDoc.Issuer == "" || oidcDoc.JwksURI == "" {
		return "", "", fmt.Errorf("OIDC config at %s missing issuer or jwks_uri", discoveryURL)
	}
	if oidcDoc.Issuer != issuerURL {
		return "", "", fmt.Errorf("OIDC config at %s issuer %q does not match requested issuer %q", discoveryURL, oidcDoc.Issuer, issuerURL)
	}

	jwksURL, err := resolveOIDCJWKSURL(discoveryURL, oidcDoc.JwksURI)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve JWKS URI %q: %w", oidcDoc.JwksURI, err)
	}

	logging.Info("Fetching JWKS from issuer %s ...", jwksURL)
	jwksData, err := getHTTPString(ctx, httpClient, jwksURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch JWKS from %s: %w", jwksURL, err)
	}

	var check json.RawMessage
	if err := json.Unmarshal([]byte(jwksData), &check); err != nil {
		return "", "", fmt.Errorf("JWKS from %s is not valid JSON: %w", jwksURL, err)
	}

	return oidcDoc.Issuer, jwksData, nil
}

func getHTTPString(ctx context.Context, httpClient *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("%d from %s: %s", resp.StatusCode, rawURL, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func resolveOIDCJWKSURL(discoveryURL, jwksURI string) (string, error) {
	base, err := url.Parse(discoveryURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(jwksURI)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// fetchClusterJWKS fetches JWKS using the following precedence:
//  1. Manual --oidc-issuer-url override
//  2. Auto-detected SPIRE OIDC discovery service
//  3. Kubernetes API server OIDC (default)
//
// Returns issuer URL, raw JWKS JSON, identity source label, and error.
func fetchClusterJWKS(config *client.Config, manualIssuerURL string) (issuer string, jwks string, identitySource string, err error) {
	// 1. Manual override
	if manualIssuerURL != "" {
		logging.Info("Using manual OIDC issuer: %s", manualIssuerURL)
		issuer, jwks, err = fetchJWKSFromURL(config, manualIssuerURL)
		if err != nil {
			return "", "", "", fmt.Errorf("manual OIDC issuer fetch failed: %w", err)
		}
		// Determine identity source from URL
		identitySource = "custom"
		if strings.Contains(manualIssuerURL, "spire") || strings.Contains(manualIssuerURL, "spiffe") {
			identitySource = "spire"
		}
		logging.Success("Using OIDC issuer: %s (source: %s)", issuer, identitySource)
		return issuer, jwks, identitySource, nil
	}

	// 2. Try SPIRE OIDC auto-detection
	spireAddr := discoverSpireOIDC(config)
	if spireAddr != "" {
		spireURL := "http://" + spireAddr
		logging.Info("SPIRE OIDC discovery service found at %s", spireURL)
		issuer, jwks, err = fetchJWKSFromURL(config, spireURL)
		if err == nil {
			logging.Success("Using SPIRE OIDC issuer: %s", issuer)
			return issuer, jwks, "spire", nil
		}
		logging.Warning("SPIRE OIDC fetch failed (%v), falling back to K8s API server OIDC", err)
	}

	// 3. Kubernetes API server OIDC (default)
	issuer, jwks, err = fetchK8sOIDCJWKS(config)
	if err != nil {
		return "", "", "", err
	}
	return issuer, jwks, "psat", nil
}

// fetchK8sOIDCJWKS fetches the OIDC configuration and JWKS from the Kubernetes API server
func fetchK8sOIDCJWKS(config *client.Config) (issuer string, jwks string, err error) {
	// Fetch OIDC configuration from K8s API server
	logging.Info("Fetching OIDC configuration from cluster...")
	cmd := buildKubectlCommand(config, []string{"get", "--raw", "/.well-known/openid-configuration"})
	oidcConfigRaw, err := executeCommand(cmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch OIDC configuration: %w", err)
	}

	logging.Debug("OIDC configuration: %s", oidcConfigRaw)

	// Parse issuer from OIDC config
	var oidcResponse oidcDiscoveryDocument
	if err := json.Unmarshal([]byte(oidcConfigRaw), &oidcResponse); err != nil {
		return "", "", fmt.Errorf("failed to parse OIDC configuration: %w", err)
	}

	if oidcResponse.Issuer == "" {
		return "", "", fmt.Errorf("OIDC configuration does not contain an issuer")
	}

	logging.Info("Detected OIDC issuer: %s", oidcResponse.Issuer)

	if supportsDirectOIDCDiscovery(oidcResponse.Issuer) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var directIssuer, directJWKS string
		directIssuer, directJWKS, err = fetchDirectOIDCJWKS(ctx, oidcResponse.Issuer, directOIDCHTTPClient)
		if err == nil {
			logging.Success("Successfully fetched issuer JWKS (%d bytes)", len(directJWKS))
			return directIssuer, directJWKS, nil
		}
		logging.Warning("Direct OIDC issuer discovery failed (%v), falling back to Kubernetes API server JWKS", err)
	}

	// Fetch JWKS from K8s API server
	logging.Info("Fetching JWKS from cluster...")
	jwksCmd := buildKubectlCommand(config, []string{"get", "--raw", "/openid/v1/jwks"})
	jwksData, err := executeCommand(jwksCmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch JWKS: %w", err)
	}

	// Validate that JWKS is valid JSON
	var jwksCheck json.RawMessage
	if err := json.Unmarshal([]byte(jwksData), &jwksCheck); err != nil {
		return "", "", fmt.Errorf("fetched JWKS is not valid JSON: %w", err)
	}

	logging.Success("Successfully fetched JWKS (%d bytes)", len(jwksData))

	return oidcResponse.Issuer, jwksData, nil
}

// getICMSURL resolves the ICMS URL from flag, environment, config, or default.
func getICMSURL(cmd *cobra.Command, config *client.Config) string {
	icmsURL, _ := cmd.Flags().GetString(clusterFlagICMSURL)
	if icmsURL != "" {
		return icmsURL
	}
	if v := os.Getenv("NVCF_ICMS_URL"); v != "" {
		return v
	}
	if config.ICMSURL != "" {
		return config.ICMSURL
	}
	if derived, ok := deriveICMSFromAPI(config.BaseHTTPURL); ok {
		return derived
	}
	// Fall back to base HTTP URL from config (single-host gateway deployments)
	return config.BaseHTTPURL
}

func getNATSURL(cmd *cobra.Command, baseServiceURL string) string {
	natsURL, _ := cmd.Flags().GetString(clusterFlagNATSURL)
	return resolveNATSURL(natsURL, baseServiceURL)
}

func runClusterRegister(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	ncaID, _ := cmd.Flags().GetString(clusterFlagNcaID)
	region, _ := cmd.Flags().GetString("region")
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errFailedToLoadConfig, err)
	}

	// Apply kubeconfig override if provided
	if kubeconfig != "" {
		config.KubeconfigPath = kubeconfig
	}

	icmsURL := getICMSURL(cmd, config)
	natsURL := getNATSURL(cmd, icmsURL)

	logging.Info("Registering cluster '%s' for NCA '%s' in region '%s'", name, ncaID, region)

	// Step 1: Validate cluster access
	logging.Info("Validating cluster access...")
	if err := validateClusterAccess(config); err != nil {
		return fmt.Errorf("cluster access validation failed: %w", err)
	}
	logging.Success("Cluster is accessible")

	// Step 2: Fetch OIDC configuration and JWKS
	oidcIssuerURL, _ := cmd.Flags().GetString("oidc-issuer-url")
	issuer, jwks, identitySource, err := fetchClusterJWKS(config, oidcIssuerURL)
	if err != nil {
		return fmt.Errorf("failed to fetch cluster JWKS: %w", err)
	}

	const defaultK8sIssuer = "https://kubernetes.default.svc.cluster.local"
	if issuer == defaultK8sIssuer {
		fmt.Println("\n⚠  Warning: Detected default Kubernetes OIDC issuer URL.")
		fmt.Println("   This issuer is shared by all vanilla K8s clusters.")
		fmt.Println("   Multiple clusters with this issuer are supported, but unique issuers")
		fmt.Println("   are recommended for better auth performance.")
		fmt.Println("   To configure a unique issuer: set --service-account-issuer on kube-apiserver")
		fmt.Println("   Or use: --oidc-issuer-url <custom-url>")
		fmt.Println()
	}

	// Step 3: Create API client and register with ICMS
	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf(errFailedToCreateClient, err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	logging.Info("Registering cluster with ICMS at %s...", icmsURL)

	registerReq := &client.RegisterClusterRequest{
		ClusterName:      name,
		ClusterGroupName: name,
		NcaID:            ncaID,
		CloudProvider:    "ON-PREM",
		Region:           region,
		NvcaVersion:      "0.0.0",
		Capabilities:     []string{"DynamicGPUDiscovery"},
		JWKS:             &jwks,
		OIDCIssuer:       &issuer,
	}

	ignoreExisting, _ := cmd.Flags().GetBool("ignore-existing")

	resp, err := c.RegisterCluster(ctx, icmsURL, ncaID, registerReq)
	if err != nil {
		// Check if this is an "already exists" error and --ignore-existing was passed
		if ignoreExisting && strings.Contains(err.Error(), "already exists") {
			logging.Info("Cluster already registered, looking up existing IDs...")
			clusterGroupID, clusterID, lookupErr := lookupExistingCluster(ctx, c, icmsURL, ncaID, name)
			if lookupErr != nil {
				return fmt.Errorf("cluster already exists but failed to look up IDs: %w", lookupErr)
			}
			printRegistrationOutput(name, clusterGroupID, clusterID, ncaID, region, issuer, identitySource, icmsURL, natsURL)
			return nil
		}
		return fmt.Errorf("failed to register cluster: %w", err)
	}

	clusterGroupID, clusterID := registeredClusterIDs(resp)

	logging.Success("Cluster registered successfully!")
	printRegistrationOutput(name, clusterGroupID, clusterID, ncaID, region, issuer, identitySource, icmsURL, natsURL)

	return nil
}

func registeredClusterIDs(resp *client.RegisterClusterResponse) (clusterGroupID, clusterID string) {
	clusterGroupID = resp.ClusterGroup.ID
	if clusterGroupID == "" {
		clusterGroupID = resp.ClusterGroupID
	}
	if len(resp.ClusterGroup.Clusters) > 0 {
		clusterID = resp.ClusterGroup.Clusters[0].ID
	}
	if clusterID == "" {
		clusterID = resp.ClusterID
	}
	return clusterGroupID, clusterID
}

// helmValues represents the YAML output for nvca-operator helm values.
//
// Schema matches the nvca-operator chart's expected keys. clusterID,
// clusterGroupID, and ncaID live at the top level with the mixed-case "ID"
// suffix to match the `## @param` annotations in
// `deployments/nvca-operator/values.yaml` (e.g. `## @param clusterID`).
// `selfManaged.identitySource` stays nested because it scopes a self-managed-
// only knob (PSAT vs SPIRE) and the chart's `selfManaged:` block already
// houses other self-managed-specific fields like `nvcaVersion`.
type helmValues struct {
	ClusterID      string            `yaml:"clusterID"`
	ClusterGroupID string            `yaml:"clusterGroupID"`
	NcaID          string            `yaml:"ncaID"`
	Region         string            `yaml:"region"`
	SelfManaged    selfManagedValues `yaml:"selfManaged"`
}

type selfManagedValues struct {
	IdentitySource  string `yaml:"identitySource"`
	ICMSServiceURL  string `yaml:"icmsServiceURL,omitempty"`
	ReValServiceURL string `yaml:"revalServiceURL,omitempty"`
	NATSURL         string `yaml:"natsURL,omitempty"`
}

// printRegistrationOutput prints the registration result and helm values YAML.
func printRegistrationOutput(name, clusterGroupID, clusterID, ncaID, region, issuer, identitySource, icmsURL, natsURL string) {
	fmt.Println()
	fmt.Printf("Cluster Group ID: %s\n", clusterGroupID)
	fmt.Printf("Cluster Group Name: %s\n", name)
	if clusterID != "" {
		fmt.Printf("Cluster ID: %s\n", clusterID)
	}
	fmt.Printf("OIDC Issuer: %s\n", issuer)
	fmt.Printf("Region: %s\n", region)

	// Output helm values YAML
	fmt.Println()
	fmt.Println("--- Helm values for nvca-operator ---")
	fmt.Println("Add the following to your nvca-operator Helm values:")
	fmt.Println()

	vals := helmValues{
		ClusterID:      clusterID,
		ClusterGroupID: clusterGroupID,
		NcaID:          ncaID,
		Region:         region,
		SelfManaged:    newSelfManagedValues(identitySource, icmsURL, natsURL),
	}

	out, err := yaml.Marshal(vals)
	if err != nil {
		logging.Warning("Failed to marshal helm values: %v", err)
		return
	}
	fmt.Print(string(out))
}

func newSelfManagedValues(identitySource, icmsServiceURL, natsURL string) selfManagedValues {
	return newSelfManagedValuesFromEndpoints(identitySource, registerEndpointValues{
		ICMSServiceURL:  icmsServiceURL,
		ReValServiceURL: deriveSiblingHTTPServiceURL(icmsServiceURL, "reval"),
		NATSURL:         natsURL,
	})
}

func newSelfManagedValuesFromEndpoints(identitySource string, endpoints registerEndpointValues) selfManagedValues {
	vals := selfManagedValues{
		IdentitySource: identitySource,
		ICMSServiceURL: endpoints.ICMSServiceURL,
		NATSURL:        endpoints.NATSURL,
	}
	if endpoints.ReValServiceURL != "" {
		vals.ReValServiceURL = endpoints.ReValServiceURL
	}
	return vals
}

// lookupExistingCluster finds an existing cluster by name via the ICMS
// list-clusters endpoint and returns the cluster group ID and cluster ID.
// This uses the same cluster-management auth as registration, avoiding the
// NVCF API's ListClusterGroups which returns 403 for admin tokens.
func lookupExistingCluster(ctx context.Context, c *client.Client, icmsURL, ncaID, clusterName string) (clusterGroupID, clusterID string, err error) {
	clusters, err := c.ListClusters(ctx, icmsURL, ncaID)
	if err != nil {
		return "", "", fmt.Errorf("failed to list clusters from ICMS: %w", err)
	}

	for _, cl := range clusters {
		if cl.ClusterName == clusterName {
			clusterGroupID = cl.ClusterGroupID
			clusterID = cl.ClusterID
			logging.Success("Found existing cluster '%s' (clusterGroupID: %s, clusterID: %s)", clusterName, clusterGroupID, clusterID)
			return clusterGroupID, clusterID, nil
		}
	}

	return "", "", fmt.Errorf("cluster '%s' not found in ICMS listing despite 'already exists' error", clusterName)
}

func runClusterRotate(cmd *cobra.Command, args []string) error {
	clusterID, _ := cmd.Flags().GetString(clusterFlagClusterID)
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	force, _ := cmd.Flags().GetBool("force")

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errFailedToLoadConfig, err)
	}

	// Apply kubeconfig override if provided
	if kubeconfig != "" {
		config.KubeconfigPath = kubeconfig
	}

	icmsURL := getICMSURL(cmd, config)

	logging.Info("Rotating JWKS for cluster '%s'", clusterID)

	// Step 1: Validate cluster access
	if err := validateClusterAccess(config); err != nil {
		return fmt.Errorf("cluster access validation failed: %w", err)
	}

	// Step 2: Fetch new JWKS from the target cluster
	issuer, jwks, identitySource, err := fetchClusterJWKS(config, "")
	if err != nil {
		return fmt.Errorf("failed to fetch cluster JWKS: %w", err)
	}

	// Step 3: Confirmation prompt (unless --force)
	if !force {
		fmt.Println()
		fmt.Printf("About to overwrite JWKS for cluster: %s\n", clusterID)
		fmt.Printf("  New OIDC issuer:    %s\n", issuer)
		fmt.Printf("  Identity source:    %s\n", identitySource)
		fmt.Printf("  ICMS endpoint:      %s\n", icmsURL)
		fmt.Println()
		fmt.Print("This replaces the cluster's trust root. Proceed? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			logging.Info("Rotation cancelled")
			return nil
		}
	}

	// Step 4: Update JWKS in ICMS
	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf(errFailedToCreateClient, err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	logging.Info("Updating JWKS in ICMS at %s...", icmsURL)

	updateReq := &client.UpdateClusterJWKSRequest{
		JWKS:       jwks,
		OIDCIssuer: &issuer,
	}

	if err := c.UpdateClusterJWKS(ctx, icmsURL, clusterID, updateReq); err != nil {
		return fmt.Errorf("failed to update cluster JWKS: %w", err)
	}

	logging.Success("JWKS rotated successfully for cluster '%s'", clusterID)
	fmt.Printf("OIDC Issuer: %s\n", issuer)

	return nil
}

func runClusterDelete(cmd *cobra.Command, args []string) error {
	clusterID, _ := cmd.Flags().GetString(clusterFlagClusterID)
	force, _ := cmd.Flags().GetBool("force")
	ignoreMissing, _ := cmd.Flags().GetBool("ignore-missing")

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errFailedToLoadConfig, err)
	}

	icmsURL := getICMSURL(cmd, config)

	// Confirmation prompt unless --force
	if !force {
		fmt.Printf("Are you sure you want to delete cluster '%s'? This cannot be undone. [y/N]: ", clusterID)
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			logging.Info("Deletion cancelled")
			return nil
		}
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf(errFailedToCreateClient, err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	logging.Info("Deleting cluster '%s' from ICMS at %s...", clusterID, icmsURL)

	if err := c.DeleteCluster(ctx, icmsURL, clusterID); err != nil {
		// --ignore-missing: 404 → silent 0-exit; other errors still bubble up.
		// Heuristic: ICMS DeleteCluster returns errors that wrap "not found"
		// or "404" in their message when the row is already gone.
		if ignoreMissing && (strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404")) {
			fmt.Fprintf(os.Stderr, "cluster %s not found; nothing to do\n", clusterID)
			return nil
		}
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	logging.Success("Cluster '%s' deleted successfully", clusterID)

	return nil
}
