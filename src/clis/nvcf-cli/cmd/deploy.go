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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"
	"nvcf-cli/internal/state"

	"github.com/spf13/cobra"
)

// deployCmd represents the deploy command group
var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Manage function deployments",
	Long: `Manage function deployments including creating, updating, retrieving, and removing deployments.

Available subcommands:
- create: Create a new deployment
- update: Update an existing deployment
- get: Get deployment details
- remove: Remove a deployment

Examples:
  # Create a new deployment
  nvcf-cli function deploy create --input-file deploy.json

  # Update an existing deployment
  nvcf-cli function deploy update --function-id <id> --version-id <version> --min-instances 2

  # Get deployment details
  nvcf-cli function deploy get --function-id <id> --version-id <version>

  # Remove a deployment
  nvcf-cli function deploy remove --function-id <id> --version-id <version>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// deployCreateCmd represents the deploy create command
var deployCreateCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create a function deployment",
	SilenceUsage: true,
	Long: `Creates a deployment for a function with the specified configuration.

This command deploys a function that was previously created, configuring it with
GPU specifications, instance types, and scaling parameters. The deployment process
may take several minutes to complete.`,
	RunE: runDeployCreate,
}

// deployUpdateCmd represents the deploy update command
var deployUpdateCmd = &cobra.Command{
	Use:          "update",
	Short:        "Update an existing function deployment",
	SilenceUsage: true,
	Long: `Updates a single GPU specification of an existing function deployment
via PATCH /v2/nvcf/deployments/{deploymentId}/gpu-specifications/{gpuSpecId}.

Selectors (required — used to look up the matching GPU spec):
  - functionId, versionId, gpu, instanceType

Updatable body fields (at least one required):
  - minInstances (>= 0)
  - maxInstances (> 0, >= minInstances)
  - autoscalingConfiguration (custom scaleUp/scaleDown — use --input-file)
  - autoscalingConfigurationPolicy (CUSTOM_CONFIGURATION | PLATFORM_CONFIGURATION)

Note: gpu, instanceType, backend, clusters, availabilityZones, preferredOrder,
and maxRequestConcurrency are immutable on an existing GPU spec and cannot be
changed here.

Authentication: Requires NVCF_TOKEN (JWT) with admin:deploy_function scope.`,
	RunE: runDeployUpdate,
}

// deployRemoveCmd represents the deploy remove command
var deployRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a function deployment",
	Long: `Remove a function deployment from the NVCF platform.

This command removes the function deployment, stopping all running instances
and making the function unavailable for invocation. The function definition
itself remains and can be redeployed later.

Examples:
  # Remove deployment for a specific function
  nvcf-cli function deploy remove --function-id <id> --version-id <version>

  # Remove with debug output
  nvcf-cli --debug function deploy remove --function-id <id> --version-id <version>`,
	RunE: runDeployRemove,
}

// deployGetCmd represents the deploy get command
var deployGetCmd = &cobra.Command{
	Use:          "get",
	Short:        "Get deployment details for a function",
	SilenceUsage: true,
	Long: `Retrieves deployment details for the specified function version.

This command returns comprehensive deployment information including:
- Function and deployment IDs
- Current deployment status
- GPU specifications and instance configurations
- Scaling parameters (min/max instances)
- Health information (if available)
- Deployment timestamps

Examples:
  # Get deployment details for a specific function
  nvcf-cli function deploy get --function-id <id> --version-id <version>

  # Get deployment details with debug output
  nvcf-cli --debug function deploy get --function-id <id> --version-id <version>

  # Get deployment details in JSON format
  nvcf-cli function deploy get --function-id <id> --version-id <version> --json

Authentication: Requires NVCF_TOKEN (JWT) with deploy_function scope.`,
	RunE: runDeployGet,
}

// DeployConfig represents the JSON configuration for deploy command
type DeployConfig struct {
	// Required fields
	FunctionID   string `json:"functionId"`
	VersionID    string `json:"versionId"`
	InstanceType string `json:"instanceType"`
	GPU          string `json:"gpu"`
	MinInstances int    `json:"minInstances"`
	MaxInstances int    `json:"maxInstances"`

	// Optional deployment configuration
	Backend               string   `json:"backend,omitempty"`
	Clusters              []string `json:"clusters,omitempty"`
	Regions               []string `json:"regions,omitempty"`
	AvailabilityZones     []string `json:"availabilityZones,omitempty"`
	Attributes            []string `json:"attributes,omitempty"` // Deprecated for updates; kept for create
	MaxRequestConcurrency int      `json:"maxRequestConcurrency,omitempty"`
	PreferredOrder        int      `json:"preferredOrder,omitempty"`

	// Hardware specifications
	CPUArch       string `json:"cpuArch,omitempty"`
	OS            string `json:"os,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
	Storage       string `json:"storage,omitempty"`
	SystemMemory  string `json:"systemMemory,omitempty"`
	GPUMemory     string `json:"gpuMemory,omitempty"`

	// Helm configuration
	Configuration map[string]any `json:"configuration,omitempty"`

	// Deployment options
	Timeout int `json:"timeout,omitempty"`
}

// NestedDeployConfig represents the API-style nested JSON configuration
type NestedDeployConfig struct {
	FunctionID               string                    `json:"functionId"`
	VersionID                string                    `json:"versionId"`
	DeploymentSpecifications []DeploymentSpecification `json:"deploymentSpecifications"`
}

// DeploymentSpecification represents a single deployment specification
type DeploymentSpecification struct {
	// Required fields
	GPU          string `json:"gpu"`
	InstanceType string `json:"instanceType"`
	MinInstances int    `json:"minInstances"`
	MaxInstances int    `json:"maxInstances"`

	// Optional deployment configuration
	Backend               string   `json:"backend,omitempty"`
	Clusters              []string `json:"clusters,omitempty"`
	Regions               []string `json:"regions,omitempty"`
	AvailabilityZones     []string `json:"availabilityZones,omitempty"`
	Attributes            []string `json:"attributes,omitempty"` // Deprecated for updates; kept for create
	MaxRequestConcurrency int      `json:"maxRequestConcurrency,omitempty"`
	PreferredOrder        int      `json:"preferredOrder,omitempty"`

	// Hardware specifications
	CPUArch       string `json:"cpuArch,omitempty"`
	OS            string `json:"os,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
	Storage       string `json:"storage,omitempty"`
	SystemMemory  string `json:"systemMemory,omitempty"`
	GPUMemory     string `json:"gpuMemory,omitempty"`

	// Configuration
	Configuration map[string]any `json:"configuration,omitempty"`
}

// UpdateDeploymentConfig mirrors the new PATCH wire body plus the selectors
// needed to resolve which GPU specification to update. JSON layout matches
// UpdateGpuSpecificationRequest; functionId/versionId/gpu/instanceType are
// selectors used to look up deploymentId + gpuSpecificationId.
type UpdateDeploymentConfig struct {
	// Selectors (not sent in the PATCH body)
	FunctionID   string `json:"functionId,omitempty"`
	VersionID    string `json:"versionId,omitempty"`
	GPU          string `json:"gpu,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`

	// Body fields (optional; at least one required)
	MinInstances                   *int                                  `json:"minInstances,omitempty"`
	MaxInstances                   *int                                  `json:"maxInstances,omitempty"`
	AutoscalingConfiguration       *client.AutoscalingConfigurationDto   `json:"autoscalingConfiguration,omitempty"`
	AutoscalingConfigurationPolicy client.AutoscalingConfigurationPolicy `json:"autoscalingConfigurationPolicy,omitempty"`
}

var deployFlags struct {
	// Input file
	inputFile string

	// Required fields
	functionID   string
	versionID    string
	instanceType string
	gpu          string
	minInstances int
	maxInstances int

	// Optional deployment configuration
	backend               string
	clusters              []string
	regions               []string
	availabilityZones     []string
	attributes            []string // Deprecated for updates; kept for create
	maxRequestConcurrency int
	preferredOrder        int

	// Hardware specifications
	cpuArch       string
	os            string
	driverVersion string
	storage       string
	systemMemory  string
	gpuMemory     string

	// Helm configuration
	helmConfig map[string]string

	// Legacy cluster name (for backward compatibility)
	clusterName string

	// Deployment options
	timeout int
}

var deployGetFlags struct {
	functionID string
	versionID  string
}

var deployUpdateFlags struct {
	inputFile         string
	functionID        string
	versionID         string
	gpu               string
	instanceType      string
	minInstances      int
	maxInstances      int
	autoscalingPolicy string
}

var (
	deployRemoveFunctionID string
	deployRemoveVersionID  string
)

func init() {
	// Add subcommands to deploy
	deployCmd.AddCommand(deployCreateCmd)
	deployCmd.AddCommand(deployUpdateCmd)
	deployCmd.AddCommand(deployGetCmd)
	deployCmd.AddCommand(deployRemoveCmd)

	// Deploy Create flags
	// Input file option
	deployCreateCmd.Flags().StringVar(&deployFlags.inputFile, "input-file", "", "JSON file with deployment configuration (overrides individual flags)")

	// Required flags
	deployCreateCmd.Flags().StringVar(&deployFlags.functionID, "function-id", "", "Function ID (required)")
	deployCreateCmd.Flags().StringVar(&deployFlags.versionID, "version-id", "", "Version ID (required)")
	deployCreateCmd.Flags().StringVar(&deployFlags.instanceType, "instance-type", "NCP.GPU.H100_1x", "Instance type (required)")
	deployCreateCmd.Flags().StringVar(&deployFlags.gpu, "gpu", "H100", "GPU name (required)")
	deployCreateCmd.Flags().StringVar(&deployFlags.gpu, "gpu-name", "H100", "GPU name (alias for --gpu)")
	deployCreateCmd.Flags().MarkDeprecated("gpu-name", "Deprecated: use --gpu instead")
	deployCreateCmd.Flags().IntVar(&deployFlags.minInstances, "min-instances", 1, "Minimum number of instances")
	deployCreateCmd.Flags().IntVar(&deployFlags.maxInstances, "max-instances", 1, "Maximum number of instances")

	// Deployment configuration flags
	deployCreateCmd.Flags().StringVar(&deployFlags.backend, "backend", "", "Backend/CSP where the GPU powered instance will be launched")
	deployCreateCmd.Flags().StringSliceVar(&deployFlags.clusters, "clusters", []string{}, "Specific clusters within spot instance or worker node")
	deployCreateCmd.Flags().StringSliceVar(&deployFlags.regions, "regions", []string{}, "List of regions allowed to deploy")
	deployCreateCmd.Flags().StringSliceVar(&deployFlags.availabilityZones, "availability-zones", []string{}, "List of availability-zones in the cluster group")
	deployCreateCmd.Flags().StringSliceVar(&deployFlags.attributes, "attributes", []string{}, "Specific attributes capabilities to deploy functions")
	deployCreateCmd.Flags().IntVar(&deployFlags.maxRequestConcurrency, "max-request-concurrency", 0, "Max request concurrency between 1 and 1024")
	deployCreateCmd.Flags().IntVar(&deployFlags.preferredOrder, "preferred-order", 0, "Preferred order of deployment if there are several gpu specs")

	// Hardware specification flags
	deployCreateCmd.Flags().StringVar(&deployFlags.cpuArch, "cpu-arch", "", "Architecture details of the CPU")
	deployCreateCmd.Flags().StringVar(&deployFlags.os, "os", "", "Operating system details")
	deployCreateCmd.Flags().StringVar(&deployFlags.driverVersion, "driver-version", "", "GPU driver version")
	deployCreateCmd.Flags().StringVar(&deployFlags.storage, "storage", "", "The amount of available storage, e.g. 80G")
	deployCreateCmd.Flags().StringVar(&deployFlags.systemMemory, "system-memory", "", "The amount of RAM")
	deployCreateCmd.Flags().StringVar(&deployFlags.gpuMemory, "gpu-memory", "", "The amount of GPU memory")

	// Legacy compatibility flag
	deployCreateCmd.Flags().StringVar(&deployFlags.clusterName, "cluster-name", "GFN", "Cluster name (legacy, use --backend instead)")

	// Deployment management flags
	deployCreateCmd.Flags().IntVar(&deployFlags.timeout, "timeout", 900, "Deployment timeout in seconds")

	// Deploy Update flags — the new PATCH endpoint only accepts min/max instances
	// and autoscaling fields. --gpu and --instance-type are selectors used to
	// look up the matching gpuSpecificationId; they are not sent in the body.
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.inputFile, "input-file", "", "JSON file with deployment update configuration")
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.functionID, "function-id", "", "Function ID (required)")
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.versionID, "version-id", "", "Version ID (required)")
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.gpu, "gpu", "", "GPU type; selector for which GPU spec to update (required)")
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.instanceType, "instance-type", "", "Instance type; selector for which GPU spec to update (required)")
	deployUpdateCmd.Flags().IntVar(&deployUpdateFlags.minInstances, "min-instances", 0, "Minimum number of instances")
	deployUpdateCmd.Flags().IntVar(&deployUpdateFlags.maxInstances, "max-instances", 0, "Maximum number of instances")
	deployUpdateCmd.Flags().StringVar(&deployUpdateFlags.autoscalingPolicy, "autoscaling-policy", "", "Autoscaling policy (CUSTOM_CONFIGURATION or PLATFORM_CONFIGURATION)")

	// Deploy Get flags
	deployGetCmd.Flags().StringVar(&deployGetFlags.functionID, "function-id", "", "Function ID (required)")
	deployGetCmd.Flags().StringVar(&deployGetFlags.versionID, "version-id", "", "Version ID (required)")
	deployGetCmd.MarkFlagRequired("function-id")
	deployGetCmd.MarkFlagRequired("version-id")

	// Deploy Remove flags
	deployRemoveCmd.Flags().StringVar(&deployRemoveFunctionID, "function-id", "", "function ID to undeploy (overrides state)")
	deployRemoveCmd.Flags().StringVar(&deployRemoveVersionID, "version-id", "", "version ID to undeploy (overrides state)")
}

// loadDeployConfig loads and merges configuration from JSON file and CLI flags
func loadDeployConfig(cmd *cobra.Command) (*DeployConfig, error) {
	config := &DeployConfig{
		// Set defaults
		InstanceType: "NCP.GPU.H100_1x",
		GPU:          "H100",
		MinInstances: 1,
		MaxInstances: 1,
		Timeout:      900,
	}

	// Load from JSON file if provided
	if deployFlags.inputFile != "" {
		data, err := os.ReadFile(deployFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read input file '%s': %w", deployFlags.inputFile, err)
		}

		// Parse as nested format (API-style with deploymentSpecifications)
		// This is the ONLY format accepted as per OpenAPI spec
		var nestedConfig NestedDeployConfig
		if err := json.Unmarshal(data, &nestedConfig); err != nil {
			return nil, fmt.Errorf("failed to parse JSON file '%s': %w\n\nExpected format:\n{\n  \"functionId\": \"...\",\n  \"versionId\": \"...\",\n  \"deploymentSpecifications\": [{\n    \"gpu\": \"L40S\",\n    \"instanceType\": \"NCP.GPU.L40S_1x\",\n    \"minInstances\": 1,\n    \"maxInstances\": 1\n  }]\n}", deployFlags.inputFile, err)
		}

		// Validate deploymentSpecifications array
		if len(nestedConfig.DeploymentSpecifications) == 0 {
			return nil, fmt.Errorf("invalid JSON file '%s': deploymentSpecifications array is required and must contain at least one specification\n\nExpected format:\n{\n  \"functionId\": \"...\",\n  \"versionId\": \"...\",\n  \"deploymentSpecifications\": [{\n    \"gpu\": \"L40S\",\n    \"instanceType\": \"NCP.GPU.L40S_1x\",\n    \"minInstances\": 1,\n    \"maxInstances\": 1\n  }]\n}", deployFlags.inputFile)
		}

		// Extract configuration from nested format (use first deployment spec)
		spec := nestedConfig.DeploymentSpecifications[0]

		config.FunctionID = nestedConfig.FunctionID
		config.VersionID = nestedConfig.VersionID
		config.GPU = spec.GPU
		config.InstanceType = spec.InstanceType
		config.MinInstances = spec.MinInstances
		config.MaxInstances = spec.MaxInstances
		config.Backend = spec.Backend
		config.Clusters = spec.Clusters
		config.Regions = spec.Regions
		config.AvailabilityZones = spec.AvailabilityZones
		config.Attributes = spec.Attributes
		config.MaxRequestConcurrency = spec.MaxRequestConcurrency
		config.PreferredOrder = spec.PreferredOrder
		config.CPUArch = spec.CPUArch
		config.OS = spec.OS
		config.DriverVersion = spec.DriverVersion
		config.Storage = spec.Storage
		config.SystemMemory = spec.SystemMemory
		config.GPUMemory = spec.GPUMemory
		config.Configuration = spec.Configuration

		fmt.Printf("Loaded deployment configuration from %s\n", deployFlags.inputFile)
	}

	// Override with CLI flags (CLI flags take precedence)
	if cmd.Flags().Changed("function-id") {
		config.FunctionID = deployFlags.functionID
	}
	if cmd.Flags().Changed("version-id") {
		config.VersionID = deployFlags.versionID
	}
	if cmd.Flags().Changed("instance-type") {
		config.InstanceType = deployFlags.instanceType
	}
	if cmd.Flags().Changed("gpu") || cmd.Flags().Changed("gpu-name") {
		config.GPU = deployFlags.gpu
	}
	if cmd.Flags().Changed("min-instances") {
		config.MinInstances = deployFlags.minInstances
	}
	if cmd.Flags().Changed("max-instances") {
		config.MaxInstances = deployFlags.maxInstances
	}
	if cmd.Flags().Changed("backend") {
		config.Backend = deployFlags.backend
	}
	if cmd.Flags().Changed("clusters") {
		config.Clusters = deployFlags.clusters
	}
	if cmd.Flags().Changed("regions") {
		config.Regions = deployFlags.regions
	}
	if cmd.Flags().Changed("availability-zones") {
		config.AvailabilityZones = deployFlags.availabilityZones
	}
	if cmd.Flags().Changed("attributes") {
		config.Attributes = deployFlags.attributes
	}
	if cmd.Flags().Changed("max-request-concurrency") {
		config.MaxRequestConcurrency = deployFlags.maxRequestConcurrency
	}
	if cmd.Flags().Changed("preferred-order") {
		config.PreferredOrder = deployFlags.preferredOrder
	}
	if cmd.Flags().Changed("cpu-arch") {
		config.CPUArch = deployFlags.cpuArch
	}
	if cmd.Flags().Changed("os") {
		config.OS = deployFlags.os
	}
	if cmd.Flags().Changed("driver-version") {
		config.DriverVersion = deployFlags.driverVersion
	}
	if cmd.Flags().Changed("storage") {
		config.Storage = deployFlags.storage
	}
	if cmd.Flags().Changed("system-memory") {
		config.SystemMemory = deployFlags.systemMemory
	}
	if cmd.Flags().Changed("gpu-memory") {
		config.GPUMemory = deployFlags.gpuMemory
	}
	if cmd.Flags().Changed("timeout") {
		config.Timeout = deployFlags.timeout
	}

	// Handle legacy cluster-name flag
	if cmd.Flags().Changed("cluster-name") && config.Backend == "" {
		config.Backend = deployFlags.clusterName
	}

	return config, nil
}

func runDeployCreate(cmd *cobra.Command, args []string) error {
	// Load and merge configuration
	config, err := loadDeployConfig(cmd)
	if err != nil {
		return err
	}

	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load existing state: %v", err)
	}
	applySavedDeployContext(config, GetCurrentState())

	// Validate required fields
	if config.FunctionID == "" {
		return fmt.Errorf("function ID is required (use --function-id, specify in JSON file, or create a function first)")
	}
	if config.VersionID == "" {
		return fmt.Errorf("version ID is required (use --version-id, specify in JSON file, or create a function first)")
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	// Use backend from config
	backend := config.Backend

	// Prepare deployment request
	req := &client.FunctionDeploymentRequest{
		DeploymentSpecifications: []client.GPUSpecificationDto{
			{
				// Required fields
				GPU:          config.GPU,
				InstanceType: config.InstanceType,
				MinInstances: config.MinInstances,
				MaxInstances: config.MaxInstances,

				// Optional deployment configuration
				Backend:               backend,
				AvailabilityZones:     config.AvailabilityZones,
				MaxRequestConcurrency: config.MaxRequestConcurrency,
				Clusters:              config.Clusters,
				Regions:               config.Regions,
				Attributes:            config.Attributes,
				PreferredOrder:        config.PreferredOrder,

				// Hardware specifications
				CPUArch:       config.CPUArch,
				OS:            config.OS,
				DriverVersion: config.DriverVersion,
				Storage:       config.Storage,
				SystemMemory:  config.SystemMemory,
				GPUMemory:     config.GPUMemory,

				// Configuration
				Configuration: config.Configuration,
			},
		},
	}

	ctx := context.Background()

	fmt.Printf("Deploying function %s (version %s)...\n", config.FunctionID, config.VersionID)

	// Deploy function
	if err := nvcfClient.DeployFunction(ctx, config.FunctionID, config.VersionID, req); err != nil {
		return fmt.Errorf("failed to deploy function: %w", err)
	}

	fmt.Printf("⏳ Waiting for deployment to complete (timeout: %d seconds)...\n", config.Timeout)

	// Wait for deployment to complete
	if err := nvcfClient.WaitForFunctionDeployment(ctx, config.FunctionID, config.VersionID, config.Timeout); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	fmt.Printf("Function %s deployed successfully!\n", config.FunctionID)

	return nil
}

func applySavedDeployContext(config *DeployConfig, currentState *state.State) {
	if config.FunctionID == "" && currentState.FunctionID != "" {
		config.FunctionID = currentState.FunctionID
		logging.Info("Using saved function ID: %s", config.FunctionID)
	}
	if config.VersionID == "" && currentState.VersionID != "" {
		config.VersionID = currentState.VersionID
		logging.Info("Using saved version ID: %s", config.VersionID)
	}
}

// loadDeployUpdateConfig merges --input-file and CLI flags. Flags override file.
// Input-file schema matches UpdateGpuSpecificationRequest (the new PATCH wire
// body) plus top-level selectors (functionId, versionId, gpu, instanceType).
func loadDeployUpdateConfig(cmd *cobra.Command) (*UpdateDeploymentConfig, error) {
	config := &UpdateDeploymentConfig{}

	if deployUpdateFlags.inputFile != "" {
		data, err := os.ReadFile(deployUpdateFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read input file '%s': %w", deployUpdateFlags.inputFile, err)
		}
		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("failed to parse JSON file '%s': %w\n\nExpected format:\n{\n  \"functionId\": \"...\",\n  \"versionId\": \"...\",\n  \"gpu\": \"H100\",\n  \"instanceType\": \"NCP.GPU.H100_1x\",\n  \"minInstances\": 0,\n  \"maxInstances\": 2\n}", deployUpdateFlags.inputFile, err)
		}
		fmt.Printf("Loaded deployment update configuration from %s\n", deployUpdateFlags.inputFile)
	}

	// CLI flags override file.
	if cmd.Flags().Changed("function-id") {
		config.FunctionID = deployUpdateFlags.functionID
	}
	if cmd.Flags().Changed("version-id") {
		config.VersionID = deployUpdateFlags.versionID
	}
	if cmd.Flags().Changed("gpu") {
		config.GPU = deployUpdateFlags.gpu
	}
	if cmd.Flags().Changed("instance-type") {
		config.InstanceType = deployUpdateFlags.instanceType
	}
	if cmd.Flags().Changed("min-instances") {
		v := deployUpdateFlags.minInstances
		config.MinInstances = &v
	}
	if cmd.Flags().Changed("max-instances") {
		v := deployUpdateFlags.maxInstances
		config.MaxInstances = &v
	}
	if cmd.Flags().Changed("autoscaling-policy") {
		config.AutoscalingConfigurationPolicy = client.AutoscalingConfigurationPolicy(deployUpdateFlags.autoscalingPolicy)
	}

	return config, nil
}

func validateDeployUpdateConfig(config *UpdateDeploymentConfig) error {
	if config.FunctionID == "" {
		return fmt.Errorf("function ID is required (use --function-id or specify in JSON file)")
	}
	if config.VersionID == "" {
		return fmt.Errorf("version ID is required (use --version-id or specify in JSON file)")
	}
	if config.GPU == "" {
		return fmt.Errorf("gpu is required (use --gpu or specify in JSON file)")
	}
	if config.InstanceType == "" {
		return fmt.Errorf("instanceType is required (use --instance-type or specify in JSON file)")
	}
	if config.MinInstances == nil && config.MaxInstances == nil &&
		config.AutoscalingConfiguration == nil && config.AutoscalingConfigurationPolicy == "" {
		return fmt.Errorf("at least one of --min-instances, --max-instances, --autoscaling-policy, or autoscalingConfiguration (via --input-file) must be provided")
	}
	if config.MinInstances != nil && *config.MinInstances < 0 {
		return fmt.Errorf("minInstances must be >= 0")
	}
	if config.MaxInstances != nil && *config.MaxInstances <= 0 {
		return fmt.Errorf("maxInstances must be > 0")
	}
	if config.MinInstances != nil && config.MaxInstances != nil && *config.MinInstances > *config.MaxInstances {
		return fmt.Errorf("minInstances (%d) cannot be greater than maxInstances (%d)", *config.MinInstances, *config.MaxInstances)
	}
	if config.AutoscalingConfigurationPolicy != "" &&
		config.AutoscalingConfigurationPolicy != client.AutoscalingPolicyCustom &&
		config.AutoscalingConfigurationPolicy != client.AutoscalingPolicyPlatform {
		return fmt.Errorf("autoscaling-policy must be %s or %s", client.AutoscalingPolicyCustom, client.AutoscalingPolicyPlatform)
	}

	return nil
}

func findDeployUpdateGPUSpecID(dep *client.DeploymentResponse, config *UpdateDeploymentConfig) (string, error) {
	deploymentID := dep.Deployment.DeploymentID
	if deploymentID == "" {
		return "", fmt.Errorf("deployment response did not include a deploymentId (is the function deployed?)")
	}

	var gpuSpecID string
	var matches int
	for _, spec := range dep.Deployment.DeploymentSpecifications {
		if spec.GPU == config.GPU && spec.InstanceType == config.InstanceType {
			gpuSpecID = spec.GpuSpecificationID
			matches++
		}
	}
	if matches == 0 {
		return "", fmt.Errorf("no GPU spec matches --gpu=%s --instance-type=%s in deployment %s", config.GPU, config.InstanceType, deploymentID)
	}
	if matches > 1 {
		return "", fmt.Errorf("multiple GPU specs match --gpu=%s --instance-type=%s in deployment %s; cannot disambiguate", config.GPU, config.InstanceType, deploymentID)
	}
	if gpuSpecID == "" {
		return "", fmt.Errorf("matched GPU spec has no gpuSpecificationId; server response may be stale")
	}

	return gpuSpecID, nil
}

func newUpdateGPUSpecificationRequest(config *UpdateDeploymentConfig) *client.UpdateGpuSpecificationRequest {
	return &client.UpdateGpuSpecificationRequest{
		MinInstances:                   config.MinInstances,
		MaxInstances:                   config.MaxInstances,
		AutoscalingConfiguration:       config.AutoscalingConfiguration,
		AutoscalingConfigurationPolicy: config.AutoscalingConfigurationPolicy,
	}
}

func runDeployUpdate(cmd *cobra.Command, args []string) error {
	config, err := loadDeployUpdateConfig(cmd)
	if err != nil {
		return err
	}

	// Validation mirrors DeploymentValidationService: at least one settable field.
	if err := validateDeployUpdateConfig(config); err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	// Look up deploymentId + gpuSpecificationId by (functionId, versionId, gpu, instanceType).
	fmt.Printf("Looking up deployment for function %s (version %s)...\n", config.FunctionID, config.VersionID)
	dep, err := nvcfClient.GetDeployment(ctx, config.FunctionID, config.VersionID)
	if err != nil {
		return fmt.Errorf("failed to look up existing deployment: %w", err)
	}
	deploymentID := dep.Deployment.DeploymentID
	gpuSpecID, err := findDeployUpdateGPUSpecID(dep, config)
	if err != nil {
		return err
	}

	fmt.Printf("Updating GPU specification %s on deployment %s...\n", gpuSpecID, deploymentID)
	resp, err := nvcfClient.UpdateGpuSpecification(ctx, deploymentID, gpuSpecID, newUpdateGPUSpecificationRequest(config))
	if err != nil {
		return fmt.Errorf("failed to update GPU specification: %w", err)
	}

	updated := resp.GpuSpecification
	fmt.Printf("GPU specification updated successfully!\n")
	fmt.Printf("GPU: %s\n", updated.GPU)
	fmt.Printf("Instance Type: %s\n", updated.InstanceType)
	fmt.Printf("Min Instances: %d\n", updated.MinInstances)
	fmt.Printf("Max Instances: %d\n", updated.MaxInstances)
	return nil
}

func runDeployGet(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Validate required flags
	if deployGetFlags.functionID == "" || deployGetFlags.versionID == "" {
		return fmt.Errorf("function ID and version ID are required")
	}

	// Create client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer nvcfClient.Close()

	// Get deployment details
	ctx := context.Background()
	response, err := nvcfClient.GetDeployment(ctx, deployGetFlags.functionID, deployGetFlags.versionID)
	if err != nil {
		return fmt.Errorf("failed to get deployment details: %w", err)
	}

	// If JSON output requested, print raw JSON
	if IsJSONOutput() {
		return OutputJSON(response)
	}

	// Otherwise, print formatted output
	deployment := response.Deployment
	fmt.Printf("\n=== Deployment Details ===\n")
	fmt.Printf("Function ID:         %s\n", deployment.FunctionID)
	fmt.Printf("Version ID:          %s\n", deployment.FunctionVersionID)
	fmt.Printf("Deployment ID:       %s\n", deployment.DeploymentID)
	fmt.Printf("Function Name:       %s\n", deployment.FunctionName)
	fmt.Printf("NCA ID:              %s\n", deployment.NcaID)
	fmt.Printf("Status:              %s\n", deployment.FunctionStatus)
	fmt.Printf("Created At:          %s\n", deployment.CreatedAt)
	fmt.Printf("Last Updated At:     %s\n", deployment.LastUpdatedAt)

	// Print health info if available
	if len(deployment.HealthInfo) > 0 {
		fmt.Printf("\n--- Health Information ---\n")
		for i, health := range deployment.HealthInfo {
			fmt.Printf("  [%d] Status: %s\n", i+1, health.Status)
			if health.Message != "" {
				fmt.Printf("      Message: %s\n", health.Message)
			}
			if health.Reason != "" {
				fmt.Printf("      Reason: %s\n", health.Reason)
			}
		}
	}

	// Print deployment specifications
	if len(deployment.DeploymentSpecifications) > 0 {
		fmt.Printf("\n--- Deployment Specifications ---\n")
		for i, spec := range deployment.DeploymentSpecifications {
			fmt.Printf("  [%d] GPU:                   %s\n", i+1, spec.GPU)
			fmt.Printf("      Instance Type:         %s\n", spec.InstanceType)
			fmt.Printf("      Min Instances:         %d\n", spec.MinInstances)
			fmt.Printf("      Max Instances:         %d\n", spec.MaxInstances)
			if spec.Backend != "" {
				fmt.Printf("      Backend:               %s\n", spec.Backend)
			}
			if spec.MaxRequestConcurrency > 0 {
				fmt.Printf("      Max Concurrency:       %d\n", spec.MaxRequestConcurrency)
			}
			if spec.PreferredOrder > 0 {
				fmt.Printf("      Preferred Order:       %d\n", spec.PreferredOrder)
			}
			if len(spec.Regions) > 0 {
				fmt.Printf("      Regions:               %s\n", strings.Join(spec.Regions, ", "))
			}
			if len(spec.AvailabilityZones) > 0 {
				fmt.Printf("      Availability Zones:    %s\n", strings.Join(spec.AvailabilityZones, ", "))
			}
			if len(spec.Clusters) > 0 {
				fmt.Printf("      Clusters:              %s\n", strings.Join(spec.Clusters, ", "))
			}
			if len(spec.Attributes) > 0 {
				fmt.Printf("      Attributes:            %s\n", strings.Join(spec.Attributes, ", "))
			}
			if i < len(deployment.DeploymentSpecifications)-1 {
				fmt.Println()
			}
		}
	}

	fmt.Println()
	return nil
}

func runDeployRemove(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Determine function ID and version ID
	functionID := deployRemoveFunctionID
	versionID := deployRemoveVersionID

	// Validate that we have both IDs
	if functionID == "" || versionID == "" {
		return fmt.Errorf("function ID and version ID required (use --function-id and --version-id flags)")
	}

	fmt.Printf("Removing deployment for function %s version %s...\n", functionID, versionID)

	// Create client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer nvcfClient.Close()

	// Call the undeploy API (DELETE deployment)
	ctx := context.Background()
	err = nvcfClient.DeleteDeployment(ctx, functionID, versionID, false) // graceful=false for immediate undeploy
	if err != nil {
		return fmt.Errorf("undeploy API call failed: %w", err)
	}

	fmt.Printf("Deployment removal request sent successfully!\n")
	fmt.Printf("Function %s version %s is being undeployed\n", functionID, versionID)

	return nil
}
