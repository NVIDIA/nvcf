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
	"path/filepath"
	"strings"
	"time"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"
	"nvcf-cli/internal/state"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	errCreateNVCFClientFmt = "failed to create NVCF client: %w"
	errLoadConfigFmt       = "failed to load configuration: %w"
	errParseInputFileFmt   = "failed to parse JSON file '%s': %w"
	errReadInputFileFmt    = "failed to read input file '%s': %w"
)

// ============================================================================
// Command Definitions
// ============================================================================

// functionCmd represents the function command group
var functionCmd = &cobra.Command{
	Use:   "function",
	Short: "Manage NVIDIA Cloud Functions",
	Long: `Manage NVIDIA Cloud Functions including creation, deployment, invocation, and lifecycle operations.

Available subcommands:
- create: Create a new function
- delete: Delete a function or deployment
- deploy: Manage function deployments
- invoke: Invoke a function
- get: Get function details
- list: List all functions
- list-ids: List function IDs only
- list-versions: List versions of a specific function
- update: Update function metadata
- queue: Manage function queues

Examples:
  # Create a new function
  nvcf-cli function create --input-file function.json

  # Deploy a function
  nvcf-cli function deploy --input-file deploy.json

  # Invoke a function
  nvcf-cli function invoke --request-body '{"input": "test"}'

  # List all functions
  nvcf-cli function list

  # Get function details
  nvcf-cli function get --function-id <id> --version-id <version>

  # Update function tags
  nvcf-cli function update --function-id <id> --version-id <version> --tags tag1,tag2,tag3

  # Delete a function
  nvcf-cli function delete`,
}

// createCmd represents the create command
var createCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create a new function",
	SilenceUsage: true,
	Long: `Creates a new function within the authenticated NVIDIA Cloud Account.

This command registers a new function with the specified parameters including 
health endpoints, protocol configuration, container settings, and more. The function
will be created with a unique function ID and version ID that can be used for
deployment and invocation.

Input Options:
  1. Use command line flags for individual parameters
  2. Use --input-file with a JSON configuration file
  3. Combine both (command line flags override JSON file values)

Health Configuration:
  Use --health-uri for a simple health endpoint, or combine --health-protocol 
  and --health-port for detailed health configuration with protocol support 
  (HTTP or gRPC).

Container Environment:
  Use --container-env multiple times to set environment variables in key=value format.

Secrets:
  Use --secrets to pass sensitive configuration like API keys or passwords.
  Format: --secrets KEY1=value1,KEY2=value2
  Values are encrypted at rest and masked in logs.

Example JSON file structure:
  {
    "name": "my-function",
    "containerImage": "my-registry/my-image:latest",
    "inferenceUrl": "http://0.0.0.0:8000/predict",
    "inferencePort": 8000,
    "healthProtocol": "HTTP",
    "healthPort": 8080,
    "containerEnvironment": [
      {"key": "MODEL_PATH", "value": "/models"},
      {"key": "BATCH_SIZE", "value": "32"}
    ],
    "secrets": [
      {"name": "API_KEY", "value": "sk-12345"},
      {"name": "DB_PASSWORD", "value": "secret"}
    ]
  }`,
	RunE: runCreate,
}

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:          "delete [function-id] [version-id]",
	Short:        "Delete a function",
	SilenceUsage: true,
	Long: `Deletes the specified function version or its deployment.

This command can either:
1. Delete the function version entirely (default) - removes the function permanently
2. Delete only the deployment (with --deployment-only) - keeps the function but stops all instances

Function/Version ID Resolution (in order of priority):
1. Explicit arguments: delete <function-id> <version-id>
2. CLI flags: --function-id and --version-id  
3. JSON file: --input-file with functionId and versionId
4. Current state: Uses function from 'nvcf-cli create' (automatic)

For deployment deletion, use --graceful to allow current tasks to complete before termination.
Function deletion is permanent and cannot be undone.

Examples:
  # Delete current function from state (easiest)
  nvcf-cli function delete

  # Delete specific function by arguments
  nvcf-cli function delete func-123 ver-456

  # Delete specific function by flags  
  nvcf-cli function delete --function-id func-123 --version-id ver-456

  # Delete only deployment (keep function)
  nvcf-cli function delete --deployment-only

  # Graceful deployment deletion
  nvcf-cli function delete --deployment-only --graceful

Authentication: This command uses NVCF_TOKEN only for authentication.`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runDelete,
}

// getFunctionCmd represents the get command
var getFunctionCmd = &cobra.Command{
	Use:          "get",
	Short:        "Get function details",
	SilenceUsage: true,
	Long:         `Get detailed information about a specific function version.`,
	RunE:         runGetFunction,
}

// invokeCmd represents the invoke command
var invokeCmd = &cobra.Command{
	Use:          "invoke",
	Short:        "Invoke a function",
	SilenceUsage: true,
	Long: `Invokes a function with a JSON request body.

This command sends a request to the specified function and waits for the response.
The request body must be a valid JSON string. Use --poll-duration to ask the
service to hold the invocation connection open for that many seconds before it
returns pending request metadata.

Invocation Methods:
  --grpc     Use gRPC invocation (native Go client with JSON encoding)
  (default)  Use direct REST invocation through <function-id>.invocation.<domain>

Examples:
  # Direct REST invocation (default)
  nvcf-cli function invoke --function-id func-123 --version-id ver-456 --request-body '{"input": "test"}'

  # gRPC invocation (experimental)
  nvcf-cli function invoke --grpc --function-id func-123 --version-id ver-456 --request-body '{"input": "test"}'

  # Using saved function context (from create/deploy)
  nvcf-cli function invoke --request-body '{"input": "test"}'

  # Using JSON configuration file
  nvcf-cli function invoke --input-file invoke-config.json`,
	RunE: runInvoke,
}

// listFunctionsCmd represents the list command
var listFunctionsCmd = &cobra.Command{
	Use:          "list",
	Short:        "List all functions",
	SilenceUsage: true,
	Long:         `List all functions in the authenticated NVIDIA Cloud Account.`,
	RunE:         runListFunctions,
}

// listFunctionIDsCmd represents the list-ids command
var listFunctionIDsCmd = &cobra.Command{
	Use:          "list-ids",
	Short:        "List function IDs",
	SilenceUsage: true,
	Long:         `List only the function IDs in the authenticated NVIDIA Cloud Account.`,
	RunE:         runListFunctionIDs,
}

// listVersionsCmd represents the list-versions command
var listVersionsCmd = &cobra.Command{
	Use:          "list-versions [function-id]",
	Short:        "List function versions",
	SilenceUsage: true,
	Long:         `List all versions of a specific function.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runListVersions,
}

// updateCmd represents the update command
var updateCmd = &cobra.Command{
	Use:          "update",
	Short:        "Update function tags",
	SilenceUsage: true,
	Long: `Updates function tags.

This allows you to modify the tags of an existing function version
without affecting the function's code or deployment configuration.

For updating deployments, use: nvcf-cli function deploy update

Authentication: Requires NVCF_TOKEN with admin:update_function scope.`,
	RunE: runUpdate,
}

// queueCmd represents the queue command
var queueCmd = &cobra.Command{
	Use:          "queue",
	Short:        "Manage function queues",
	SilenceUsage: true,
	Long: `Monitor and manage NVIDIA Cloud Function execution queues.

Available subcommands:
- status: Get queue details for a function
- position: Get position in queue for a request
- details: Get detailed queue information`,
}

// queueStatusCmd represents the queue status command
var queueStatusCmd = &cobra.Command{
	Use:          "status [function-id] [version-id]",
	Short:        "Get queue status",
	SilenceUsage: true,
	Long:         `Get queue status for a function or function version.`,
	Args:         cobra.RangeArgs(1, 2),
	RunE:         runQueueStatus,
}

// queuePositionCmd represents the queue position command
var queuePositionCmd = &cobra.Command{
	Use:          "position [request-id]",
	Short:        "Get position in queue",
	SilenceUsage: true,
	Long:         `Get the position of a specific request in the execution queue.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runQueuePosition,
}

// ============================================================================
// Configuration Structs
// ============================================================================

// CreateConfig represents the JSON configuration for create command
type CreateConfig struct {
	// Required fields
	Name           string `json:"name"`
	ContainerImage string `json:"containerImage"`
	InferenceURL   string `json:"inferenceUrl"`
	InferencePort  int    `json:"inferencePort"`

	// Optional metadata
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// Health configuration (flat format for backward compatibility)
	HealthURI            string `json:"healthUri,omitempty"`
	HealthProtocol       string `json:"healthProtocol,omitempty"`
	HealthPort           int    `json:"healthPort,omitempty"`
	HealthTimeout        string `json:"healthTimeout,omitempty"`
	HealthExpectedStatus int    `json:"healthExpectedStatus,omitempty"`

	// Health configuration (nested format - preferred)
	Health *HealthConfigInput `json:"health,omitempty"`

	// Function configuration
	FunctionType         string                      `json:"functionType,omitempty"`
	APIBodyFormat        string                      `json:"apiBodyFormat,omitempty"`
	ContainerArgs        string                      `json:"containerArgs,omitempty"`
	ContainerEnvironment []ContainerEnvironmentEntry `json:"containerEnvironment,omitempty"`

	// Helm configuration
	HelmChart            string `json:"helmChart,omitempty"`
	HelmChartServiceName string `json:"helmChartServiceName,omitempty"`

	// Secrets configuration - can be either strings or full secret objects
	Secrets interface{} `json:"secrets,omitempty"` // Can be []string or []SecretConfig

	// Models and resources (ArtifactDto arrays)
	Models    []ArtifactConfig `json:"models,omitempty"`
	Resources []ArtifactConfig `json:"resources,omitempty"`

	// Rate limiting configuration
	RateLimit         string   `json:"rateLimit,omitempty"`
	RateLimitExempted []string `json:"rateLimitExempted,omitempty"`
	RateLimitSync     bool     `json:"rateLimitSync,omitempty"`

	// Telemetry configuration
	LogsTelemetryId    string `json:"logsTelemetryId,omitempty"`
	MetricsTelemetryId string `json:"metricsTelemetryId,omitempty"`
	TracesTelemetryId  string `json:"tracesTelemetryId,omitempty"`
}

// ArtifactConfig represents a model or resource artifact in CLI configuration
type ArtifactConfig struct {
	Name      string          `json:"name"`
	Version   string          `json:"version,omitempty"`
	URI       string          `json:"uri,omitempty"`
	LLMConfig *LLMConfigInput `json:"llmConfig,omitempty"`
}

// LLMConfigInput represents LLM routing metadata in CLI configuration
type LLMConfigInput struct {
	URIs           []string `json:"uris,omitempty"`
	TokenRateLimit *string  `json:"tokenRateLimit,omitempty"`
	RoutingMethod  *string  `json:"routingMethod,omitempty"`
}

// ContainerEnvironmentEntry represents an environment variable in CLI configuration
type ContainerEnvironmentEntry struct {
	Key   string `json:"key"`   // Environment variable key
	Value string `json:"value"` // Environment variable value
}

// HealthConfigInput represents health configuration in JSON format
type HealthConfigInput struct {
	Protocol           string `json:"protocol"`
	URI                string `json:"uri"`
	Port               int    `json:"port"`
	Timeout            string `json:"timeout,omitempty"`
	ExpectedStatusCode int    `json:"expectedStatusCode,omitempty"`
}

// SecretConfig represents a secret with name and value in CLI configuration
type SecretConfig struct {
	Name  string      `json:"name"`            // Secret name
	Value interface{} `json:"value,omitempty"` // Secret value (can be string, number, object, etc.)
}

// DeleteConfig represents the JSON configuration for delete command
type DeleteConfig struct {
	FunctionID           string `json:"functionId"`
	VersionID            string `json:"versionId"`
	Graceful             bool   `json:"graceful,omitempty"`
	DeleteDeploymentOnly bool   `json:"deleteDeploymentOnly,omitempty"`
}

// InvokeConfig represents the JSON configuration for invoke command
type InvokeConfig struct {
	FunctionID          string                 `json:"functionId"`
	VersionID           string                 `json:"versionId"`
	InferenceURL        string                 `json:"inferenceUrl"` // Function's inference endpoint (e.g., "/echo")
	RequestBody         map[string]interface{} `json:"requestBody"`
	Timeout             int                    `json:"timeout,omitempty"`
	PollDurationSeconds int                    `json:"pollDurationSeconds,omitempty"`

	// gRPC-specific fields
	GRPCService   string `json:"grpcService,omitempty"`   // gRPC service name (e.g., "nvidia.nvcf.v1.InferenceService")
	GRPCMethod    string `json:"grpcMethod,omitempty"`    // gRPC method name (e.g., "Predict")
	GRPCPlaintext bool   `json:"grpcPlaintext,omitempty"` // Use plaintext (insecure) gRPC
}

// UpdateConfig represents the JSON configuration for updating function metadata
type UpdateConfig struct {
	FunctionID string   `json:"functionId"`
	VersionID  string   `json:"versionId"`
	Tags       []string `json:"tags,omitempty"`
}

// ============================================================================
// Flag Structures
// ============================================================================

var createFlags struct {
	// Input file
	inputFile string

	// Required fields
	name           string
	containerImage string
	inferenceURL   string
	inferencePort  int

	// Optional metadata
	description string
	tags        []string

	// Health configuration
	healthURI            string
	healthProtocol       string
	healthPort           int
	healthTimeout        string
	healthExpectedStatus int

	// Function configuration
	functionType         string
	apiBodyFormat        string
	containerArgs        string
	containerEnvironment []string

	// Helm configuration
	helmChart            string
	helmChartServiceName string

	// Secrets configuration
	secrets []string

	// Models and resources
	models    []string
	llmModels []string
	resources []string

	// Rate limiting
	rateLimit         string
	rateLimitExempted []string
	rateLimitSync     bool

	// Telemetry
	logsTelemetryId    string
	metricsTelemetryId string
	tracesTelemetryId  string
}

var deleteFlags struct {
	// Input file
	inputFile string

	functionID           string
	versionID            string
	graceful             bool
	deleteDeploymentOnly bool
}

var invokeFlags struct {
	// Input file
	inputFile string

	functionID          string
	versionID           string
	requestBody         string
	timeout             int
	pollDurationSeconds int
	useGRPC             bool // Use gRPC proxy instead of direct REST invocation
	grpcService         string
	grpcMethod          string
	grpcPlaintext       bool
}

var updateFlags struct {
	inputFile  string
	functionID string
	versionID  string
	tags       []string
}

// ============================================================================
// Init Function
// ============================================================================

func init() {
	rootCmd.AddCommand(functionCmd)

	// Add all function subcommands
	functionCmd.AddCommand(createCmd)
	functionCmd.AddCommand(deleteCmd)
	functionCmd.AddCommand(deployCmd)
	functionCmd.AddCommand(invokeCmd)
	functionCmd.AddCommand(getFunctionCmd)
	functionCmd.AddCommand(listFunctionsCmd)
	functionCmd.AddCommand(listFunctionIDsCmd)
	functionCmd.AddCommand(listVersionsCmd)
	functionCmd.AddCommand(updateCmd)
	functionCmd.AddCommand(queueCmd)

	// Add queue subcommands
	queueCmd.AddCommand(queueStatusCmd)
	queueCmd.AddCommand(queuePositionCmd)

	// Create command flags
	createCmd.Flags().StringVar(&createFlags.inputFile, "input-file", "", "JSON file with function configuration (overrides individual flags)")
	createCmd.Flags().StringVar(&createFlags.name, "name", "", "Function name (required)")
	createCmd.Flags().StringVar(&createFlags.containerImage, "image", "", "Container image (required)")
	createCmd.Flags().StringVar(&createFlags.inferenceURL, "inference-url", "", "Inference URL (required)")
	createCmd.Flags().IntVar(&createFlags.inferencePort, "inference-port", 0, "Inference port (required)")
	createCmd.Flags().StringVar(&createFlags.description, "description", "", "Function description")
	createCmd.Flags().StringSliceVar(&createFlags.tags, "tags", []string{}, "Function tags (comma-separated)")
	createCmd.Flags().StringVar(&createFlags.healthURI, "health-uri", "", "Health endpoint URI (simple)")
	createCmd.Flags().StringVar(&createFlags.healthProtocol, "health-protocol", "", "Health protocol (HTTP or gRPC)")
	createCmd.Flags().IntVar(&createFlags.healthPort, "health-port", 0, "Health endpoint port")
	createCmd.Flags().StringVar(&createFlags.healthTimeout, "health-timeout", "", "Health check timeout (ISO 8601 duration)")
	createCmd.Flags().IntVar(&createFlags.healthExpectedStatus, "health-expected-status", 200, "Expected health check status code")
	createCmd.Flags().StringVar(&createFlags.functionType, "function-type", "DEFAULT", "Function type (DEFAULT, STREAMING, or LLM)")
	createCmd.Flags().StringVar(&createFlags.apiBodyFormat, "api-body-format", "CUSTOM", "API body format")
	createCmd.Flags().StringVar(&createFlags.containerArgs, "container-args", "", "Arguments for container launch")
	createCmd.Flags().StringSliceVar(&createFlags.containerEnvironment, "container-env", []string{}, "Container environment variables (key=value)")
	createCmd.Flags().StringVar(&createFlags.helmChart, "helm-chart", "", "Helm chart specification")
	createCmd.Flags().StringVar(&createFlags.helmChartServiceName, "helm-chart-service", "", "Helm chart service name")
	createCmd.Flags().StringSliceVar(&createFlags.secrets, "secrets", []string{}, "Secrets in name=value format (e.g., API_KEY=secret123,DB_PASSWORD=pass456)")
	createCmd.Flags().StringSliceVar(&createFlags.models, "models", []string{}, "Model artifacts (format: name:version:uri)")
	createCmd.Flags().StringArrayVar(&createFlags.llmModels, "llm-model", []string{}, "LLM model config (format: name=<model>,uris=<uri>|<uri>,routingMethod=<round_robin|power_of_two|random>,tokenRateLimit=<limit>)")
	createCmd.Flags().StringSliceVar(&createFlags.resources, "resources", []string{}, "Resource artifacts (format: name:version:uri)")
	createCmd.Flags().StringVar(&createFlags.rateLimit, "rate-limit", "", "Rate limit pattern (e.g., '100-S', '50-M', '10-H', '5-D')")
	createCmd.Flags().StringSliceVar(&createFlags.rateLimitExempted, "rate-limit-exempted", []string{}, "NCA IDs exempted from rate limiting")
	createCmd.Flags().BoolVar(&createFlags.rateLimitSync, "rate-limit-sync", false, "Enable synchronous rate limit checking")
	createCmd.Flags().StringVar(&createFlags.logsTelemetryId, "logs-telemetry-id", "", "UUID for logs telemetry")
	createCmd.Flags().StringVar(&createFlags.metricsTelemetryId, "metrics-telemetry-id", "", "UUID for metrics telemetry")
	createCmd.Flags().StringVar(&createFlags.tracesTelemetryId, "traces-telemetry-id", "", "UUID for traces telemetry")

	// Delete command flags
	deleteCmd.Flags().StringVar(&deleteFlags.inputFile, "input-file", "", "JSON file with deletion configuration (overrides individual flags)")
	deleteCmd.Flags().StringVar(&deleteFlags.functionID, "function-id", "", "Function ID (optional - uses current function from state if not specified)")
	deleteCmd.Flags().StringVar(&deleteFlags.versionID, "version-id", "", "Version ID (optional - uses current version from state if not specified)")
	deleteCmd.Flags().BoolVar(&deleteFlags.graceful, "graceful", false, "Gracefully shutdown deployment (only for deployment deletion)")
	deleteCmd.Flags().BoolVar(&deleteFlags.deleteDeploymentOnly, "deployment-only", false, "Delete only the deployment, not the function itself")

	// Get function flags
	getFunctionCmd.Flags().String("function-id", "", "Function ID to get details for (required)")
	getFunctionCmd.Flags().String("version-id", "", "Version ID to get details for (required)")
	getFunctionCmd.MarkFlagRequired("function-id")
	getFunctionCmd.MarkFlagRequired("version-id")

	// Invoke command flags
	invokeCmd.Flags().StringVar(&invokeFlags.inputFile, "input-file", "", "JSON file with invocation configuration (overrides individual flags)")
	invokeCmd.Flags().StringVar(&invokeFlags.functionID, "function-id", "", "Function ID (required)")
	invokeCmd.Flags().StringVar(&invokeFlags.versionID, "version-id", "", "Version ID (required)")
	invokeCmd.Flags().StringVar(&invokeFlags.requestBody, "request-body", "", "JSON request body (required)")
	invokeCmd.Flags().IntVar(&invokeFlags.timeout, "timeout", 60, "Request timeout in seconds")
	invokeCmd.Flags().IntVar(&invokeFlags.pollDurationSeconds, "poll-duration", 5, "Invocation hold-open duration in seconds")
	invokeCmd.Flags().BoolVar(&invokeFlags.useGRPC, "grpc", false, "Use gRPC invocation (native Go client)")
	invokeCmd.Flags().StringVar(&invokeFlags.grpcService, "grpc-service", "", "gRPC service name")
	invokeCmd.Flags().StringVar(&invokeFlags.grpcMethod, "grpc-method", "", "gRPC method name")
	invokeCmd.Flags().BoolVar(&invokeFlags.grpcPlaintext, "grpc-plaintext", false, "Use plaintext (insecure) gRPC")

	// Update command flags
	updateCmd.Flags().StringVar(&updateFlags.inputFile, "input-file", "", "JSON file with metadata update configuration")
	updateCmd.Flags().StringVar(&updateFlags.functionID, "function-id", "", "Function ID (required)")
	updateCmd.Flags().StringVar(&updateFlags.versionID, "version-id", "", "Version ID (required)")
	updateCmd.Flags().StringSliceVar(&updateFlags.tags, "tags", []string{}, "Function tags (comma-separated, required)")
}

// ============================================================================
// Helper Functions
// ============================================================================

// parseArtifactString parses a string in format "name:version:uri" into ArtifactConfig
func parseArtifactString(s string) (ArtifactConfig, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 {
		return ArtifactConfig{}, fmt.Errorf("insufficient parts, need 3 (name:version:uri)")
	}
	// Join the remaining parts for the URI (in case URI contains colons)
	uri := strings.Join(parts[2:], ":")
	return ArtifactConfig{
		Name:    parts[0],
		Version: parts[1],
		URI:     uri,
	}, nil
}

// parseLLMModelString parses "name=<model>,uris=<uri>|<uri>,..." into ArtifactConfig.
func parseLLMModelString(s string) (ArtifactConfig, error) {
	fields := map[string]string{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return ArtifactConfig{}, fmt.Errorf("invalid field %q, expected key=value", item)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "name", "uris", "routingMethod", "tokenRateLimit":
			fields[key] = value
		default:
			return ArtifactConfig{}, fmt.Errorf("unknown llm model field %q", key)
		}
	}

	name := fields["name"]
	if name == "" {
		return ArtifactConfig{}, fmt.Errorf("name is required")
	}

	uris, err := parseLLMModelURIs(fields["uris"])
	if err != nil {
		return ArtifactConfig{}, err
	}

	routingMethod, err := normalizeLLMRoutingMethod(fields["routingMethod"])
	if err != nil {
		return ArtifactConfig{}, err
	}

	return ArtifactConfig{
		Name: name,
		LLMConfig: &LLMConfigInput{
			URIs:           uris,
			TokenRateLimit: optionalString(fields["tokenRateLimit"]),
			RoutingMethod:  optionalString(routingMethod),
		},
	}, nil
}

func parseLLMModelURIs(value string) ([]string, error) {
	if value == "" {
		return nil, fmt.Errorf("uris is required")
	}

	var uris []string
	for _, uri := range strings.Split(value, "|") {
		uri = strings.TrimSpace(uri)
		if uri == "" {
			return nil, fmt.Errorf("uris cannot contain empty values")
		}
		uris = append(uris, uri)
	}
	return uris, nil
}

func validateLLMModelURIs(uris []string) error {
	if len(uris) == 0 {
		return fmt.Errorf("uris is required")
	}

	for _, uri := range uris {
		if strings.TrimSpace(uri) == "" {
			return fmt.Errorf("uris cannot contain empty values")
		}
	}
	return nil
}

func normalizeLLMRoutingMethod(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	switch strings.ToLower(strings.TrimSpace(value)) {
	case "round_robin":
		return "round_robin", nil
	case "power_of_two":
		return "power_of_two", nil
	case "random":
		return "random", nil
	default:
		return "", fmt.Errorf("unsupported routingMethod %q (expected round_robin, power_of_two, or random)", value)
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func artifactConfigToClientArtifact(artifact ArtifactConfig) (client.ArtifactDto, error) {
	llmConfig, err := llmConfigInputToClient(artifact.LLMConfig)
	if err != nil {
		return client.ArtifactDto{}, fmt.Errorf("model %q: %w", artifact.Name, err)
	}

	return client.ArtifactDto{
		Name:      artifact.Name,
		Version:   artifact.Version,
		URI:       artifact.URI,
		LLMConfig: llmConfig,
	}, nil
}

func llmConfigInputToClient(input *LLMConfigInput) (*client.LLMConfigDto, error) {
	if input == nil {
		return nil, nil
	}

	if err := validateLLMModelURIs(input.URIs); err != nil {
		return nil, err
	}

	routingMethod, err := normalizeLLMRoutingMethod(optionalStringValue(input.RoutingMethod))
	if err != nil {
		return nil, err
	}

	return &client.LLMConfigDto{
		URIs:           input.URIs,
		TokenRateLimit: input.TokenRateLimit,
		RoutingMethod:  optionalString(routingMethod),
	}, nil
}

// generateDemoFolder creates a demo folder with JSON stubs for all operations
func generateDemoFolder(functionID, versionID string, createConfig *CreateConfig) error {
	// Create folder name
	folderName := fmt.Sprintf("%s_demo", versionID)

	// Create the folder
	if err := os.MkdirAll(folderName, 0755); err != nil {
		return fmt.Errorf("failed to create demo folder: %w", err)
	}

	// Generate deploy.json with functionId and versionId
	deployJSON := map[string]interface{}{
		"functionId": functionID,
		"versionId":  versionID,
		"deploymentSpecifications": []map[string]interface{}{
			{
				"gpu":          "H100",
				"maxInstances": 1,
				"minInstances": 1,
				"instanceType": "NCP.GPU.H100_1x",
			},
		},
	}

	if err := writeJSONFile(filepath.Join(folderName, "deploy.json"), deployJSON); err != nil {
		return err
	}

	// Generate invoke.json
	invokeJSON := map[string]interface{}{
		"functionId": functionID,
		"versionId":  versionID,
		"requestBody": map[string]interface{}{
			"input": "Hello, World! This is a test request.",
			"parameters": map[string]interface{}{
				"temperature": 0.7,
				"max_tokens":  100,
				"top_p":       0.9,
			},
			"model_config": map[string]interface{}{
				"batch_size":      1,
				"return_logprobs": false,
			},
		},
		"timeout":             120,
		"pollDurationSeconds": 10,
	}

	if err := writeJSONFile(filepath.Join(folderName, "invoke.json"), invokeJSON); err != nil {
		return err
	}

	// Generate delete-function.json
	deleteFunctionJSON := map[string]interface{}{
		"functionId":           functionID,
		"versionId":            versionID,
		"graceful":             true,
		"deleteDeploymentOnly": false,
	}

	if err := writeJSONFile(filepath.Join(folderName, "delete-function.json"), deleteFunctionJSON); err != nil {
		return err
	}

	// Generate delete-deployment.json
	deleteDeploymentJSON := map[string]interface{}{
		"functionId":           functionID,
		"versionId":            versionID,
		"graceful":             true,
		"deleteDeploymentOnly": true,
	}

	if err := writeJSONFile(filepath.Join(folderName, "delete-deployment.json"), deleteDeploymentJSON); err != nil {
		return err
	}

	// Generate README.md with usage instructions
	readme := fmt.Sprintf(`# Demo Files for Function %s

This folder contains JSON configuration files for NVCF operations for function ID: %s (Version: %s)

## Usage

### Deploy Function
%sscli deploy --function-id %s --version-id %s --input-file deploy.json

### Invoke Function  
%sscli invoke --input-file invoke.json

### Delete Deployment
%sscli delete --input-file delete-deployment.json

### Delete Function
%sscli delete --input-file delete-function.json

## Files Description

- **deploy.json** - Deployment specifications with H100 GPU requirements  
- **invoke.json** - Function invocation request with sample payload
- **delete-deployment.json** - Delete deployment configuration
- **delete-function.json** - Delete function configuration
- **README.md** - This usage guide

Generated automatically by NVCF CLI demo mode.
`, createConfig.Name, functionID, versionID, "./", functionID, versionID, "./", "./", "./")

	if err := os.WriteFile(filepath.Join(folderName, "README.md"), []byte(readme), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	return nil
}

// writeJSONFile writes data as formatted JSON to a file
func writeJSONFile(filename string, data interface{}) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON for %s: %w", filename, err)
	}

	if err := os.WriteFile(filename, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", filename, err)
	}

	return nil
}

// loadTokenOnlyConfig loads configuration using only NVCF_TOKEN for delete operations
func loadTokenOnlyConfig() (*client.Config, error) {
	// Use the standard config loading which handles state file, etc.
	config, err := client.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf(errLoadConfigFmt, err)
	}

	// Verify we have a token for delete operations
	if config.Token == "" {
		return nil, fmt.Errorf("NVCF_TOKEN is required for delete operations (set in environment variable or config file)")
	}

	return config, nil
}

// ============================================================================
// Configuration Loading Functions
// ============================================================================

// loadCreateConfig loads and merges configuration from JSON file and CLI flags
func loadCreateConfig(cmd *cobra.Command) (*CreateConfig, error) {
	config := &CreateConfig{
		// Set defaults that should always be applied
		HealthExpectedStatus: 200,
	}

	if err := loadCreateConfigFile(config); err != nil {
		return nil, err
	}

	if err := applyCreateFlagOverrides(cmd, config); err != nil {
		return nil, err
	}

	return config, nil
}

func loadCreateConfigFile(config *CreateConfig) error {
	if createFlags.inputFile == "" {
		return nil
	}

	data, err := os.ReadFile(createFlags.inputFile)
	if err != nil {
		return fmt.Errorf(errReadInputFileFmt, createFlags.inputFile, err)
	}

	if err := json.Unmarshal(data, config); err != nil {
		return fmt.Errorf(errParseInputFileFmt, createFlags.inputFile, err)
	}

	fmt.Printf("Loaded configuration from %s\n", createFlags.inputFile)
	return nil
}

func applyCreateFlagOverrides(cmd *cobra.Command, config *CreateConfig) error {
	applyCreateRequiredFlagOverrides(cmd, config)
	applyCreateMetadataFlagOverrides(cmd, config)
	applyCreateHealthFlagOverrides(cmd, config)
	applyCreateFunctionFlagOverrides(cmd, config)
	applyCreateHelmFlagOverrides(cmd, config)
	applyCreateRateLimitFlagOverrides(cmd, config)
	applyCreateTelemetryFlagOverrides(cmd, config)

	if err := applyCreateContainerEnvFlag(cmd, config); err != nil {
		return err
	}
	return applyCreateArtifactFlagOverrides(cmd, config)
}

func applyCreateRequiredFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("name") {
		config.Name = createFlags.name
	}
	if cmd.Flags().Changed("image") {
		config.ContainerImage = createFlags.containerImage
	}
	if cmd.Flags().Changed("inference-url") {
		config.InferenceURL = createFlags.inferenceURL
	}
	if cmd.Flags().Changed("inference-port") {
		config.InferencePort = createFlags.inferencePort
	}
}

func applyCreateMetadataFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("description") {
		config.Description = createFlags.description
	}
	if cmd.Flags().Changed("tags") {
		config.Tags = createFlags.tags
	}
}

func applyCreateHealthFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("health-uri") {
		config.HealthURI = createFlags.healthURI
	}
	if cmd.Flags().Changed("health-protocol") {
		config.HealthProtocol = createFlags.healthProtocol
	}
	if cmd.Flags().Changed("health-port") {
		config.HealthPort = createFlags.healthPort
	}
	if cmd.Flags().Changed("health-timeout") {
		config.HealthTimeout = createFlags.healthTimeout
	}
	if cmd.Flags().Changed("health-expected-status") {
		config.HealthExpectedStatus = createFlags.healthExpectedStatus
	}
}

func applyCreateFunctionFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("function-type") {
		config.FunctionType = createFlags.functionType
	}
	if cmd.Flags().Changed("api-body-format") {
		config.APIBodyFormat = createFlags.apiBodyFormat
	}
	if cmd.Flags().Changed("container-args") {
		config.ContainerArgs = createFlags.containerArgs
	}
}

func applyCreateContainerEnvFlag(cmd *cobra.Command, config *CreateConfig) error {
	if !cmd.Flags().Changed("container-env") {
		return nil
	}

	containerEnv, err := parseContainerEnvironment(createFlags.containerEnvironment)
	if err != nil {
		return err
	}
	config.ContainerEnvironment = containerEnv
	return nil
}

func parseContainerEnvironment(values []string) ([]ContainerEnvironmentEntry, error) {
	containerEnv := make([]ContainerEnvironmentEntry, 0, len(values))
	for _, env := range values {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid environment variable format '%s', expected 'key=value'", env)
		}
		containerEnv = append(containerEnv, ContainerEnvironmentEntry{
			Key:   parts[0],
			Value: parts[1],
		})
	}
	return containerEnv, nil
}

func applyCreateHelmFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("helm-chart") {
		config.HelmChart = createFlags.helmChart
	}
	if cmd.Flags().Changed("helm-chart-service") {
		config.HelmChartServiceName = createFlags.helmChartServiceName
	}
	if cmd.Flags().Changed("secrets") {
		config.Secrets = createFlags.secrets
	}
}

func applyCreateArtifactFlagOverrides(cmd *cobra.Command, config *CreateConfig) error {
	if cmd.Flags().Changed("models") {
		models, err := parseArtifactStrings(createFlags.models, "model")
		if err != nil {
			return err
		}
		config.Models = models
	}

	if cmd.Flags().Changed("llm-model") {
		models, err := parseLLMModelStrings(createFlags.llmModels)
		if err != nil {
			return err
		}
		config.Models = append(config.Models, models...)
	}

	if cmd.Flags().Changed("resources") {
		resources, err := parseArtifactStrings(createFlags.resources, "resource")
		if err != nil {
			return err
		}
		config.Resources = resources
	}
	return nil
}

func parseArtifactStrings(values []string, label string) ([]ArtifactConfig, error) {
	artifacts := make([]ArtifactConfig, 0, len(values))
	for _, value := range values {
		artifact, err := parseArtifactString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s format '%s': %w (expected format: name:version:uri)", label, value, err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func parseLLMModelStrings(values []string) ([]ArtifactConfig, error) {
	models := make([]ArtifactConfig, 0, len(values))
	for _, value := range values {
		artifact, err := parseLLMModelString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid llm model format '%s': %w", value, err)
		}
		models = append(models, artifact)
	}
	return models, nil
}

func applyCreateRateLimitFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("rate-limit") {
		config.RateLimit = createFlags.rateLimit
	}
	if cmd.Flags().Changed("rate-limit-exempted") {
		config.RateLimitExempted = createFlags.rateLimitExempted
	}
	if cmd.Flags().Changed("rate-limit-sync") {
		config.RateLimitSync = createFlags.rateLimitSync
	}
}

func applyCreateTelemetryFlagOverrides(cmd *cobra.Command, config *CreateConfig) {
	if cmd.Flags().Changed("logs-telemetry-id") {
		config.LogsTelemetryId = createFlags.logsTelemetryId
	}
	if cmd.Flags().Changed("metrics-telemetry-id") {
		config.MetricsTelemetryId = createFlags.metricsTelemetryId
	}
	if cmd.Flags().Changed("traces-telemetry-id") {
		config.TracesTelemetryId = createFlags.tracesTelemetryId
	}
}

// loadDeleteConfig loads and merges configuration from JSON file and CLI flags
func loadDeleteConfig(cmd *cobra.Command) (*DeleteConfig, error) {
	config := &DeleteConfig{}

	// Load from JSON file if provided
	if deleteFlags.inputFile != "" {
		data, err := os.ReadFile(deleteFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf(errReadInputFileFmt, deleteFlags.inputFile, err)
		}

		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf(errParseInputFileFmt, deleteFlags.inputFile, err)
		}

		fmt.Printf("Loaded deletion configuration from %s\n", deleteFlags.inputFile)
	}

	// Override with CLI flags (CLI flags take precedence)
	if cmd.Flags().Changed("function-id") {
		config.FunctionID = deleteFlags.functionID
	}
	if cmd.Flags().Changed("version-id") {
		config.VersionID = deleteFlags.versionID
	}
	if cmd.Flags().Changed("graceful") {
		config.Graceful = deleteFlags.graceful
	}
	if cmd.Flags().Changed("deployment-only") {
		config.DeleteDeploymentOnly = deleteFlags.deleteDeploymentOnly
	}

	return config, nil
}

// loadInvokeConfig loads and merges configuration from JSON file and CLI flags
func loadInvokeConfig(cmd *cobra.Command) (*InvokeConfig, error) {
	config := &InvokeConfig{
		// Set defaults
		Timeout:             60,
		PollDurationSeconds: 5,
	}

	// Load from JSON file if provided
	if invokeFlags.inputFile != "" {
		data, err := os.ReadFile(invokeFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf(errReadInputFileFmt, invokeFlags.inputFile, err)
		}

		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf(errParseInputFileFmt, invokeFlags.inputFile, err)
		}

		fmt.Printf("Loaded invocation configuration from %s\n", invokeFlags.inputFile)
	}

	// Override with CLI flags (CLI flags take precedence)
	if cmd.Flags().Changed("function-id") {
		config.FunctionID = invokeFlags.functionID
	}
	if cmd.Flags().Changed("version-id") {
		config.VersionID = invokeFlags.versionID
	}
	if cmd.Flags().Changed("request-body") {
		// Parse request body JSON from CLI flag
		var requestBody map[string]interface{}
		if err := json.Unmarshal([]byte(invokeFlags.requestBody), &requestBody); err != nil {
			return nil, fmt.Errorf("invalid JSON in request-body: %w", err)
		}
		config.RequestBody = requestBody
	}
	if cmd.Flags().Changed("timeout") {
		config.Timeout = invokeFlags.timeout
	}
	if cmd.Flags().Changed("poll-duration") {
		config.PollDurationSeconds = invokeFlags.pollDurationSeconds
	}
	if cmd.Flags().Changed("grpc-service") {
		config.GRPCService = invokeFlags.grpcService
	}
	if cmd.Flags().Changed("grpc-method") {
		config.GRPCMethod = invokeFlags.grpcMethod
	}
	if cmd.Flags().Changed("grpc-plaintext") {
		config.GRPCPlaintext = invokeFlags.grpcPlaintext
	}

	return config, nil
}

// loadUpdateConfig loads configuration for metadata updates
func loadUpdateConfig(cmd *cobra.Command) (*UpdateConfig, error) {
	config := &UpdateConfig{}

	// Load from JSON file if provided
	if updateFlags.inputFile != "" {
		data, err := os.ReadFile(updateFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf(errReadInputFileFmt, updateFlags.inputFile, err)
		}

		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf(errParseInputFileFmt, updateFlags.inputFile, err)
		}

		fmt.Printf("Loaded metadata update configuration from %s\n", updateFlags.inputFile)
	}

	// Override with CLI flags
	if cmd.Flags().Changed("function-id") {
		config.FunctionID = updateFlags.functionID
	}
	if cmd.Flags().Changed("version-id") {
		config.VersionID = updateFlags.versionID
	}
	if cmd.Flags().Changed("tags") {
		config.Tags = updateFlags.tags
	}

	return config, nil
}

// ============================================================================
// Run Functions
// ============================================================================

func runCreate(cmd *cobra.Command, args []string) error {
	config, err := loadCreateConfig(cmd)
	if err != nil {
		return err
	}

	if err := validateCreateConfig(config); err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errLoadConfigFmt, err)
	}

	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf(errCreateNVCFClientFmt, err)
	}
	defer nvcfClient.Close()

	req, health, err := buildCreateFunctionRequest(config)
	if err != nil {
		return err
	}

	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load existing state: %v", err)
	}

	ctx := context.Background()
	logging.Info("Creating function '%s'...", config.Name)
	resp, err := nvcfClient.CreateFunction(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create function: %w", err)
	}

	SetCurrentFunction(resp.Function.ID, resp.Function.VersionID, resp.Function.Name)
	if err := SaveStateForCurrentCommand(); err != nil {
		logging.Warning("Failed to save function state: %v", err)
	}

	printCreateResult(resp, config, health, clientConfig.Demo)
	return nil
}

func validateCreateConfig(config *CreateConfig) error {
	if config.Name == "" {
		return fmt.Errorf("function name is required (use --name or specify in JSON file)")
	}
	if config.InferenceURL == "" {
		return fmt.Errorf("inference URL is required (use --inference-url or specify in JSON file)")
	}
	if config.InferencePort == 0 {
		return fmt.Errorf("inference port is required (use --inference-port or specify in JSON file)")
	}
	return nil
}

func buildCreateFunctionRequest(config *CreateConfig) (*client.CreateFunctionRequest, *client.HealthDto, error) {
	secrets, err := clientSecretsFromConfig(config)
	if err != nil {
		return nil, nil, err
	}

	models, err := artifactsToClient(config.Models)
	if err != nil {
		return nil, nil, err
	}

	resources, err := artifactsToClient(config.Resources)
	if err != nil {
		return nil, nil, err
	}

	health := healthToClient(config)
	req := &client.CreateFunctionRequest{
		Name:                 config.Name,
		ContainerImage:       config.ContainerImage,
		InferenceURL:         config.InferenceURL,
		InferencePort:        config.InferencePort,
		HealthURI:            config.HealthURI,
		Health:               health,
		FunctionType:         createFunctionType(config.FunctionType),
		APIBodyFormat:        createAPIBodyFormat(config.APIBodyFormat),
		ContainerArgs:        config.ContainerArgs,
		ContainerEnvironment: containerEnvironmentToClient(config.ContainerEnvironment),
		HelmChart:            config.HelmChart,
		HelmChartServiceName: config.HelmChartServiceName,
		Secrets:              secrets,
		Models:               models,
		Resources:            resources,
		RateLimit:            rateLimitToClient(config),
		Telemetries:          telemetriesToClient(config),
		Description:          config.Description,
		Tags:                 config.Tags,
	}
	return req, health, nil
}

func containerEnvironmentToClient(entries []ContainerEnvironmentEntry) []client.ContainerEnvironmentEntry {
	containerEnv := make([]client.ContainerEnvironmentEntry, 0, len(entries))
	for _, env := range entries {
		containerEnv = append(containerEnv, client.ContainerEnvironmentEntry{
			Key:   env.Key,
			Value: env.Value,
		})
	}
	return containerEnv
}

func clientSecretsFromConfig(config *CreateConfig) ([]client.SecretDto, error) {
	secrets, err := secretsFromFlagPairs(createFlags.secrets)
	if err != nil {
		return nil, err
	}

	jsonSecrets, err := secretsFromJSONConfig(config.Secrets)
	if err != nil {
		return nil, err
	}
	return append(secrets, jsonSecrets...), nil
}

func secretsFromFlagPairs(pairs []string) ([]client.SecretDto, error) {
	secrets := make([]client.SecretDto, 0, len(pairs))
	for _, pair := range pairs {
		secret, err := secretFromString(pair)
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func secretsFromJSONConfig(raw interface{}) ([]client.SecretDto, error) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, nil
	}

	secrets := make([]client.SecretDto, 0, len(items))
	for _, item := range items {
		secret, ok, err := secretFromJSONItem(item)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
}

func secretFromJSONItem(item interface{}) (client.SecretDto, bool, error) {
	switch secret := item.(type) {
	case string:
		secretDto, err := secretFromString(secret)
		return secretDto, true, err
	case map[string]interface{}:
		return secretFromMap(secret), true, nil
	default:
		return client.SecretDto{}, false, nil
	}
}

func secretFromString(value string) (client.SecretDto, error) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return client.SecretDto{}, fmt.Errorf("invalid secret format '%s': must be name=value", value)
	}
	return client.SecretDto{Name: parts[0], Value: parts[1]}, nil
}

func secretFromMap(secret map[string]interface{}) client.SecretDto {
	secretDto := client.SecretDto{}
	if name, ok := secret["name"].(string); ok {
		secretDto.Name = name
	}
	if value, exists := secret["value"]; exists {
		secretDto.Value = value
	}
	return secretDto
}

func artifactsToClient(configs []ArtifactConfig) ([]client.ArtifactDto, error) {
	artifacts := make([]client.ArtifactDto, 0, len(configs))
	for _, config := range configs {
		artifact, err := artifactConfigToClientArtifact(config)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func rateLimitToClient(config *CreateConfig) *client.RateLimitDto {
	if config.RateLimit == "" {
		return nil
	}
	return &client.RateLimitDto{
		RateLimit:      config.RateLimit,
		ExemptedNcaIds: config.RateLimitExempted,
		SyncCheck:      config.RateLimitSync,
	}
}

func telemetriesToClient(config *CreateConfig) *client.TelemetriesDto {
	if config.LogsTelemetryId == "" && config.MetricsTelemetryId == "" && config.TracesTelemetryId == "" {
		return nil
	}
	return &client.TelemetriesDto{
		LogsTelemetryId:    config.LogsTelemetryId,
		MetricsTelemetryId: config.MetricsTelemetryId,
		TracesTelemetryId:  config.TracesTelemetryId,
	}
}

func healthToClient(config *CreateConfig) *client.HealthDto {
	if config.Health != nil {
		return healthInputToClient(config.Health)
	}
	if config.HealthProtocol != "" && config.HealthPort > 0 {
		return detailedHealthToClient(config)
	}
	if config.HealthURI != "" {
		return simpleHealthToClient(config)
	}
	return nil
}

func healthInputToClient(input *HealthConfigInput) *client.HealthDto {
	return &client.HealthDto{
		Protocol:           input.Protocol,
		URI:                input.URI,
		Port:               input.Port,
		Timeout:            input.Timeout,
		ExpectedStatusCode: input.ExpectedStatusCode,
	}
}

func detailedHealthToClient(config *CreateConfig) *client.HealthDto {
	health := &client.HealthDto{
		Protocol: config.HealthProtocol,
		URI:      config.HealthURI,
		Port:     config.HealthPort,
	}
	applyOptionalHealthFields(health, config)
	return health
}

func simpleHealthToClient(config *CreateConfig) *client.HealthDto {
	health := &client.HealthDto{
		Protocol: "HTTP",
		URI:      config.HealthURI,
		Port:     config.InferencePort,
	}
	applyOptionalHealthFields(health, config)
	return health
}

func applyOptionalHealthFields(health *client.HealthDto, config *CreateConfig) {
	if config.HealthTimeout != "" {
		health.Timeout = config.HealthTimeout
	}
	if config.HealthExpectedStatus > 0 {
		health.ExpectedStatusCode = config.HealthExpectedStatus
	}
}

func createFunctionType(functionType string) string {
	if functionType == "" {
		return "DEFAULT"
	}
	return functionType
}

func createAPIBodyFormat(apiBodyFormat string) string {
	if apiBodyFormat == "" {
		return "CUSTOM"
	}
	return apiBodyFormat
}

func printCreateResult(resp *client.CreateFunctionResponse, config *CreateConfig, health *client.HealthDto, demo bool) {
	logging.Success("Function created successfully!")
	logging.Plain("Function ID: %s", resp.Function.ID)
	logging.Plain("Version ID: %s", resp.Function.VersionID)
	logging.Plain("Name: %s", resp.Function.Name)
	logging.Plain("Status: %s", resp.Function.Status)
	logging.Plain("Creation Time: %s", resp.Function.CreationTime)

	if demo {
		if err := generateDemoFolder(resp.Function.ID, resp.Function.VersionID, config); err != nil {
			logging.Warning("Failed to generate demo folder: %v", err)
		} else {
			logging.Success("Demo folder '%s_demo' created with JSON stubs!", resp.Function.VersionID)
		}
	}

	if health != nil {
		logging.Plain("Health Configuration:")
		logging.Plain("  Protocol: %s", health.Protocol)
		logging.Plain("  URI: %s", health.URI)
		logging.Plain("  Port: %d", health.Port)
		if health.Timeout != "" {
			logging.Plain("  Timeout: %s", health.Timeout)
		}
		if health.ExpectedStatusCode > 0 {
			logging.Plain("  Expected Status Code: %d", health.ExpectedStatusCode)
		}
	} else if config.HealthURI != "" {
		logging.Plain("Health URI: %s", config.HealthURI)
	}
}

func runDelete(cmd *cobra.Command, args []string) error {
	// Load and merge configuration
	config, err := loadDeleteConfig(cmd)
	if err != nil {
		return err
	}

	// Priority 1: Explicit arguments override everything
	if len(args) >= 1 && args[0] != "" {
		config.FunctionID = args[0]
	}
	if len(args) >= 2 && args[1] != "" {
		config.VersionID = args[1]
	}

	// Priority 4: Fallback to current function state if still not set
	if config.FunctionID == "" || config.VersionID == "" {
		if err := LoadStateForCurrentCommand(); err != nil {
			logging.Warning("Could not load existing state: %v", err)
		}

		currentState := GetCurrentState()
		if !HasCurrentFunction() {
			return fmt.Errorf("no function specified and no current function in state - provide function ID and version ID, or run 'nvcf-cli function create' first")
		}

		if config.FunctionID == "" {
			config.FunctionID = currentState.FunctionID
			logging.Info("Using function ID from state: %s", config.FunctionID)
		}
		if config.VersionID == "" {
			config.VersionID = currentState.VersionID
			logging.Info("Using version ID from state: %s", config.VersionID)
		}
	}

	// Final validation
	if config.FunctionID == "" {
		return fmt.Errorf("function ID is required - provide as argument, flag, in JSON file, or ensure current function is set in state")
	}
	if config.VersionID == "" {
		return fmt.Errorf("version ID is required - provide as argument, flag, in JSON file, or ensure current function is set in state")
	}

	// Load client configuration with NVCF_TOKEN only for delete operations
	clientConfig, err := loadTokenOnlyConfig()
	if err != nil {
		return fmt.Errorf(errLoadConfigFmt, err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf(errCreateNVCFClientFmt, err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	if config.DeleteDeploymentOnly {
		// Delete only the deployment
		verb := "Deleting deployment for"
		if config.Graceful {
			verb = "Gracefully deleting deployment for"
		}
		logging.Info("%s function %s (version %s)...", verb, config.FunctionID, config.VersionID)

		if err := nvcfClient.DeleteDeployment(ctx, config.FunctionID, config.VersionID, config.Graceful); err != nil {
			return fmt.Errorf("failed to delete deployment: %w", err)
		}

		logging.Success("Deployment for function %s (version %s) deleted successfully!", config.FunctionID, config.VersionID)
	} else {
		// Delete the entire function
		logging.Info("Deleting function %s (version %s)...", config.FunctionID, config.VersionID)

		if err := nvcfClient.DeleteFunction(ctx, config.FunctionID, config.VersionID); err != nil {
			return fmt.Errorf("failed to delete function: %w", err)
		}

		logging.Success("Function %s (version %s) deleted successfully!", config.FunctionID, config.VersionID)

		// Clear function state if we deleted the current function
		if err := LoadStateForCurrentCommand(); err == nil {
			currentState := GetCurrentState()
			if currentState.FunctionID == config.FunctionID && currentState.VersionID == config.VersionID {
				sm := GetStateManagerForCurrentCommand()
				sm.ClearFunction()
				if err := SaveStateForCurrentCommand(); err != nil {
					logging.Warning("Failed to clear function from state: %v", err)
				} else {
					logging.Info("Cleared current function from state")
				}
			}
		}
	}

	return nil
}

func runGetFunction(cmd *cobra.Command, args []string) error {
	functionID, _ := cmd.Flags().GetString("function-id")
	versionID, _ := cmd.Flags().GetString("version-id")

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	if !IsJSONOutput() {
		fmt.Printf("Getting details for function %s version %s...\n", functionID, versionID)
	}
	result, err := c.GetFunctionDetails(ctx, functionID, versionID)
	if err != nil {
		return fmt.Errorf("failed to get function: %w", err)
	}

	// If JSON output is requested, marshal and print raw JSON
	if IsJSONOutput() {
		return OutputJSON(result)
	}

	// Pretty print the function details
	fmt.Printf("\nFunction Details:\n")
	fmt.Printf("================\n")
	fmt.Printf("Function ID: %s\n", result.ID)
	fmt.Printf("Version ID: %s\n", result.VersionID)
	fmt.Printf("NCA ID: %s\n", result.NCAID)
	fmt.Printf("Name: %s\n", result.Name)
	fmt.Printf("Status: %s\n", result.Status)
	fmt.Printf("Function Type: %s\n", result.FunctionType)
	fmt.Printf("Created: %s\n", result.CreatedAt)

	if result.Description != "" {
		fmt.Printf("Description: %s\n", result.Description)
	}

	if result.OwnedByDifferentAccount {
		fmt.Printf("Owned by Different Account: Yes\n")
	}

	fmt.Printf("\nContainer Configuration:\n")
	fmt.Printf("=======================\n")
	if result.ContainerImage != "" {
		fmt.Printf("Image: %s\n", result.ContainerImage)
	}
	if result.InferenceURL != "" {
		fmt.Printf("Inference URL: %s\n", result.InferenceURL)
	}
	if result.InferencePort > 0 {
		fmt.Printf("Inference Port: %d\n", result.InferencePort)
	}
	if result.ContainerArgs != "" {
		fmt.Printf("Container Args: %s\n", result.ContainerArgs)
	}
	if result.APIBodyFormat != "" {
		fmt.Printf("API Body Format: %s\n", result.APIBodyFormat)
	}

	if len(result.ContainerEnvironment) > 0 {
		fmt.Printf("\nEnvironment Variables:\n")
		fmt.Printf("=====================\n")
		for _, env := range result.ContainerEnvironment {
			fmt.Printf("  %s = %s\n", env.Key, env.Value)
		}
	}

	if result.Health != nil {
		fmt.Printf("\nHealth Configuration:\n")
		fmt.Printf("====================\n")
		if result.Health.Protocol != "" {
			fmt.Printf("Protocol: %s\n", result.Health.Protocol)
		}
		if result.Health.Port > 0 {
			fmt.Printf("Port: %d\n", result.Health.Port)
		}
		if result.Health.URI != "" {
			fmt.Printf("URI: %s\n", result.Health.URI)
		}
		if result.Health.Timeout != "" {
			fmt.Printf("Timeout: %s\n", result.Health.Timeout)
		}
		if result.Health.ExpectedStatusCode > 0 {
			fmt.Printf("Expected Status: %d\n", result.Health.ExpectedStatusCode)
		}
	} else if result.HealthURI != "" {
		fmt.Printf("\nHealth Configuration:\n")
		fmt.Printf("====================\n")
		fmt.Printf("URI: %s\n", result.HealthURI)
	}

	if result.HelmChart != "" {
		fmt.Printf("\nHelm Configuration:\n")
		fmt.Printf("==================\n")
		fmt.Printf("Chart: %s\n", result.HelmChart)
		if result.HelmChartServiceName != "" {
			fmt.Printf("Service Name: %s\n", result.HelmChartServiceName)
		}
	}

	if len(result.Secrets) > 0 {
		fmt.Printf("\nSecrets:\n")
		fmt.Printf("========\n")
		for _, secret := range result.Secrets {
			fmt.Printf("  - %s\n", secret)
		}
	}

	if len(result.Tags) > 0 {
		fmt.Printf("\nTags:\n")
		fmt.Printf("=====\n")
		for _, tag := range result.Tags {
			fmt.Printf("  - %s\n", tag)
		}
	}

	if result.RateLimit != nil {
		fmt.Printf("\nRate Limit Configuration:\n")
		fmt.Printf("========================\n")
		fmt.Printf("Rate Limit: %s\n", result.RateLimit.RateLimit)
		if len(result.RateLimit.ExemptedNcaIds) > 0 {
			fmt.Printf("Exempted NCA IDs: %v\n", result.RateLimit.ExemptedNcaIds)
		}
		if result.RateLimit.SyncCheck {
			fmt.Printf("Sync Check: enabled\n")
		}
	}

	return nil
}

func runInvoke(cmd *cobra.Command, args []string) error {
	// Load state to get saved function context if needed
	if err := state.Load(); err != nil {
		logging.Warning("Could not load state: %v", err)
	}

	// Load and merge configuration
	config, err := loadInvokeConfig(cmd)
	if err != nil {
		return err
	}

	// Use saved function context if function ID/version not specified
	currentState := state.GetState()
	applySavedInvokeContext(config, currentState)
	if err := validateInvokeConfig(config); err != nil {
		return err
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errLoadConfigFmt, err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf(errCreateNVCFClientFmt, err)
	}
	defer nvcfClient.Close()

	// Check for gRPC invocation mode
	if invokeFlags.useGRPC {
		logging.Info("Using gRPC proxy invocation for function %s (version %s)...", config.FunctionID, config.VersionID)
		return invokeViaGRPC(clientConfig, currentState, config)
	}

	return invokeViaREST(context.Background(), nvcfClient, config)
}

func applySavedInvokeContext(config *InvokeConfig, currentState *state.State) {
	if config.FunctionID == "" && currentState.FunctionID != "" {
		config.FunctionID = currentState.FunctionID
		logging.Info("Using saved function ID: %s", config.FunctionID)
	}
	if config.VersionID == "" && currentState.VersionID != "" {
		config.VersionID = currentState.VersionID
		logging.Info("Using saved version ID: %s", config.VersionID)
	}
}

func validateInvokeConfig(config *InvokeConfig) error {
	if config.FunctionID == "" {
		return fmt.Errorf("function ID is required (use --function-id, specify in JSON file, or create a function first)")
	}
	if config.VersionID == "" {
		return fmt.Errorf("version ID is required (use --version-id, specify in JSON file, or create a function first)")
	}
	if config.RequestBody == nil {
		return fmt.Errorf("request body is required (use --request-body or specify in JSON file)")
	}
	return nil
}

func invokeViaREST(ctx context.Context, nvcfClient *client.Client, config *InvokeConfig) error {
	logging.Info("Using direct REST invocation for function %s (version %s)...", config.FunctionID, config.VersionID)

	// Invoke function via direct REST
	resp, err := nvcfClient.InvokeFunctionWithOptions(
		ctx,
		config.FunctionID,
		config.VersionID,
		config.RequestBody,
		config.Timeout,
		invokeOptionsFromConfig(config),
	)
	if err != nil {
		return fmt.Errorf("failed to invoke function: %w", err)
	}
	return outputInvokeResponse(resp)
}

func invokeOptionsFromConfig(config *InvokeConfig) *client.InvokeFunctionOptions {
	if config.InferenceURL == "" && config.PollDurationSeconds <= 0 {
		return nil
	}
	return &client.InvokeFunctionOptions{
		InferenceURL:        config.InferenceURL,
		PollDurationSeconds: config.PollDurationSeconds,
	}
}

func outputInvokeResponse(resp *client.InvokeFunctionResponse) error {
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	fmt.Printf("Function invocation completed!\n")
	fmt.Printf("Status: %s\n", resp.Status)

	if resp.RequestID != "" {
		fmt.Printf("Request ID: %s\n", resp.RequestID)
	}

	if resp.PercentComplete != "" {
		fmt.Printf("Progress: %s%%\n", resp.PercentComplete)
	}

	if resp.LocationURL != "" {
		fmt.Printf("Result Location: %s\n", resp.LocationURL)
	}

	if resp.ResponseBody != nil {
		printInvokePayload(resp.ResponseBody)
		return nil
	}
	if resp.Response != nil {
		printInvokePayload(resp.Response)
	}

	return nil
}

func printInvokePayload(payload interface{}) {
	fmt.Printf("\nResponse:\n")
	output, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Printf("%v\n", payload)
		return
	}
	fmt.Printf("%s\n", string(output))
}

// invokeViaGRPC invokes a function using gRPC protocol
func invokeViaGRPC(clientConfig *client.Config, currentState *state.State, config *InvokeConfig) error {
	// Use native Go gRPC client for direct invocation
	return invokeViaGRPCDirect(clientConfig, currentState, config)
}

// invokeViaGRPCCluster invokes a function using grpcurl via kubectl (cluster mode)
func invokeViaGRPCCluster(clientConfig *client.Config, currentState *state.State, config *InvokeConfig) error {
	if clientConfig.ClusterConfig == nil {
		return fmt.Errorf("cluster configuration not available for gRPC invocation")
	}

	// Check if API key is available (required for invocation)
	apiKey := currentState.APIKey
	if apiKey == "" {
		logging.Error("No API key found for function invocation")
		logging.Info("Generate an API key using: nvcf-cli api-key generate")
		return fmt.Errorf("API key required for function invocation")
	}

	if !state.IsAPIKeyValid() {
		logging.Warning("API key may be expired")
	}

	// Get gRPC service and method names
	grpcService := config.GRPCService
	grpcMethod := config.GRPCMethod

	// Use defaults if not specified
	if grpcService == "" {
		grpcService = "Echo"
		logging.Info("Using default gRPC service: %s", grpcService)
	}
	if grpcMethod == "" {
		grpcMethod = "EchoMessage"
		logging.Info("Using default gRPC method: %s", grpcMethod)
	}

	// Convert request body to JSON string
	requestBodyJSON, err := json.Marshal(config.RequestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Build gRPC target - use the grpc-proxy service
	grpcTarget := "nvcf-grpc-proxy.nvcf-grpc-proxy.svc.cluster.local:10081"
	if clientConfig.ClusterConfig != nil && clientConfig.ClusterConfig.GRPCService != "" {
		grpcTarget = clientConfig.ClusterConfig.GRPCService
	}

	// Build full service method
	fullMethod := fmt.Sprintf("%s/%s", grpcService, grpcMethod)

	logging.Info("Invoking via gRPC (cluster mode with grpcurl)...")
	logging.Info("Target: %s", grpcTarget)
	logging.Info("Service/Method: %s", fullMethod)
	logging.Info("Function ID: %s", config.FunctionID)

	// Execute gRPC invocation via kubectl run with grpcurl
	// Using fullstorydev/grpcurl image which has ENTRYPOINT set to "grpcurl"
	// So we only pass the flags/arguments, NOT "grpcurl" itself

	// Build grpcurl arguments as a proper array (no "grpcurl" command)
	args := []string{
		"-v",
		"-plaintext",
		"-H", "function-id: " + config.FunctionID,
		"-H", "Content-Type: application/json",
		"-H", "Authorization: Bearer " + apiKey,
		"-d", string(requestBodyJSON),
		grpcTarget,
		fullMethod,
	}

	if clientConfig.Debug {
		logging.Debug("grpcurl arguments (ENTRYPOINT=grpcurl): %v", args)
	}

	output, err := executeKubectlRunWithImage(clientConfig, "invoke-function-grpc", "fullstorydev/grpcurl:latest", args)
	if err != nil {
		return fmt.Errorf("gRPC invocation failed: %w", err)
	}

	// Display results
	logging.Success("gRPC invocation completed!")
	logging.Plain("Response:")
	fmt.Println(output)

	// Try to parse JSON response
	if strings.TrimSpace(output) != "" {
		var jsonResponse map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &jsonResponse); err == nil {
			logging.Plain("\nParsed JSON response:")
			logging.PrintJSON(output)
		}
	}

	return nil
}

// invokeViaGRPCDirect invokes a function using native Go gRPC client (direct mode)
func invokeViaGRPCDirect(clientConfig *client.Config, currentState *state.State, config *InvokeConfig) error {
	// Check if API key is available
	apiKey := currentState.APIKey
	if apiKey == "" {
		logging.Error("No API key found for function invocation")
		logging.Info("Generate an API key using: nvcf-cli api-key generate")
		return fmt.Errorf("API key required for function invocation")
	}

	if !state.IsAPIKeyValid() {
		logging.Warning("API key may be expired")
	}

	// Get gRPC service and method names
	grpcService := config.GRPCService
	grpcMethod := config.GRPCMethod

	if grpcService == "" {
		grpcService = "nvidia.nvcf.v1.InferenceService"
	}
	if grpcMethod == "" {
		grpcMethod = "Predict"
	}

	grpcTarget := clientConfig.BaseGRPCURL

	logging.Info("Invoking via gRPC (native Go client)...")
	logging.Info("Target: %s", grpcTarget)
	logging.Info("Service/Method: %s/%s", grpcService, grpcMethod)

	// Set up gRPC dial options (use default proto codec)
	var dialOpts []grpc.DialOption
	if config.GRPCPlaintext {
		logging.Info("Using plaintext gRPC (insecure)")
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		creds := credentials.NewTLS(nil) // Uses system cert pool
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	}

	// Connect to gRPC endpoint
	logging.Debug("Connecting to gRPC endpoint: %s", grpcTarget)
	conn, err := grpc.Dial(grpcTarget, dialOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect to gRPC endpoint: %w", err)
	}
	defer conn.Close()

	// Prepare metadata with authentication and function routing
	// Based on NVCF gRPC proxy documentation
	md := metadata.Pairs(
		"authorization", "Bearer "+apiKey,
		"function-id", config.FunctionID,
		"function-version-id", config.VersionID,
	)

	// Add hold-open duration if specified
	if config.PollDurationSeconds > 0 {
		md.Append("nvcf-poll-seconds", fmt.Sprintf("%d", config.PollDurationSeconds))
	}

	// Create context with metadata and timeout
	ctx := metadata.NewOutgoingContext(context.Background(), md)
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(config.Timeout)*time.Second)
		defer cancel()
	}

	// Build full method path
	fullMethod := fmt.Sprintf("/%s/%s", grpcService, grpcMethod)
	logging.Debug("Full gRPC method: %s", fullMethod)
	logging.Debug("Metadata headers: function-id=%s, function-version-id=%s", config.FunctionID, config.VersionID)

	// Convert JSON request body to protobuf Struct
	// This allows us to send arbitrary JSON as a proto message
	requestStruct, err := structpb.NewStruct(config.RequestBody)
	if err != nil {
		return fmt.Errorf("failed to convert request body to protobuf: %w", err)
	}

	requestJSON, _ := json.Marshal(config.RequestBody)
	logging.Debug("Request payload: %s", string(requestJSON))

	// Prepare response as protobuf Struct
	responseStruct := &structpb.Struct{}

	// Invoke the gRPC method
	logging.Info("Sending gRPC request...")
	err = conn.Invoke(ctx, fullMethod, requestStruct, responseStruct)
	if err != nil {
		return fmt.Errorf("gRPC invocation failed: %w", err)
	}

	// Convert response Struct back to JSON
	responseMap := responseStruct.AsMap()
	responseJSON, err := json.MarshalIndent(responseMap, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	logging.Success("gRPC invocation successful!")
	if IsJSONOutput() {
		return OutputJSON(responseMap)
	}
	fmt.Println("\nResponse:")
	fmt.Println(string(responseJSON))

	return nil
}

func runListFunctions(cmd *cobra.Command, args []string) error {
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	if IsJSONOutput() {
		// no banner
	} else {
		fmt.Println("Listing functions...")
	}
	result, err := c.ListFunctions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list functions: %w", err)
	}

	if len(result.Functions) == 0 {
		if IsJSONOutput() {
			return OutputJSON(result)
		}
		fmt.Println("No functions found.")
		return nil
	}
	if IsJSONOutput() {
		return OutputJSON(result)
	}

	fmt.Printf("Found %d functions:\n\n", len(result.Functions))
	for _, function := range result.Functions {
		fmt.Printf("ID: %s\n", function.ID)
		fmt.Printf("Version ID: %s\n", function.VersionID)
		fmt.Printf("Name: %s\n", function.Name)
		fmt.Printf("Status: %s\n", function.Status)
		if function.Description != "" {
			fmt.Printf("Description: %s\n", function.Description)
		}
		fmt.Printf("Created: %s\n", function.CreatedAt)
		if function.ContainerImage != "" {
			fmt.Printf("Image: %s\n", function.ContainerImage)
		}
		if len(function.Tags) > 0 {
			fmt.Printf("Tags: %v\n", function.Tags)
		}
		fmt.Println("---")
	}

	return nil
}

func runListFunctionIDs(cmd *cobra.Command, args []string) error {
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	if IsJSONOutput() {
		// no banner
	} else {
		fmt.Println("Listing function IDs...")
	}
	result, err := c.ListFunctionIDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list function IDs: %w", err)
	}

	if len(result.FunctionIDs) == 0 {
		if IsJSONOutput() {
			return OutputJSON(result)
		}
		fmt.Println("No functions found.")
		return nil
	}
	if IsJSONOutput() {
		return OutputJSON(result)
	}

	fmt.Printf("Found %d functions:\n", len(result.FunctionIDs))
	for _, id := range result.FunctionIDs {
		fmt.Println(id)
	}

	return nil
}

func runListVersions(cmd *cobra.Command, args []string) error {
	functionID := args[0]

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	if IsJSONOutput() {
		// no banner
	} else {
		fmt.Printf("Listing versions for function %s...\n", functionID)
	}
	result, err := c.ListFunctionVersions(ctx, functionID)
	if err != nil {
		return fmt.Errorf("failed to list function versions: %w", err)
	}

	if len(result.Functions) == 0 {
		if IsJSONOutput() {
			return OutputJSON(result)
		}
		fmt.Println("No versions found.")
		return nil
	}
	if IsJSONOutput() {
		return OutputJSON(result)
	}

	fmt.Printf("Found %d versions:\n\n", len(result.Functions))
	for _, function := range result.Functions {
		fmt.Printf("Version ID: %s\n", function.VersionID)
		fmt.Printf("Name: %s\n", function.Name)
		fmt.Printf("Status: %s\n", function.Status)
		if function.Description != "" {
			fmt.Printf("Description: %s\n", function.Description)
		}
		fmt.Printf("Created: %s\n", function.CreatedAt)
		fmt.Println("---")
	}

	return nil
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// Load and merge configuration
	config, err := loadUpdateConfig(cmd)
	if err != nil {
		return err
	}

	// Validate required fields
	if config.FunctionID == "" {
		return fmt.Errorf("function ID is required (use --function-id or specify in JSON file)")
	}
	if config.VersionID == "" {
		return fmt.Errorf("version ID is required (use --version-id or specify in JSON file)")
	}

	// Tags are required
	if len(config.Tags) == 0 {
		return fmt.Errorf("tags are required (use --tags or specify in JSON file)")
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf(errLoadConfigFmt, err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf(errCreateNVCFClientFmt, err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Printf("Updating tags for function %s (version %s)...\n", config.FunctionID, config.VersionID)

	// Update function tags
	if err := nvcfClient.UpdateFunctionMetadata(ctx, config.FunctionID, config.VersionID, &client.UpdateFunctionMetadataRequest{
		Tags: config.Tags,
	}); err != nil {
		return fmt.Errorf("failed to update function tags: %w", err)
	}

	fmt.Printf("Function tags updated successfully!\n")
	fmt.Printf("Tags: %s\n", strings.Join(config.Tags, ", "))

	return nil
}

func runQueueStatus(cmd *cobra.Command, args []string) error {
	functionID := args[0]
	var versionID string
	if len(args) > 1 {
		versionID = args[1]
	}

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	var result *client.GetQueuesResponse

	if versionID != "" {
		fmt.Printf("Getting queue details for function %s version %s...\n", functionID, versionID)
		result, err = c.GetQueueDetailsForVersion(ctx, functionID, versionID)
	} else {
		fmt.Printf("Getting queue details for function %s...\n", functionID)
		result, err = c.GetQueueDetails(ctx, functionID)
	}

	if err != nil {
		return fmt.Errorf("failed to get queue details: %w", err)
	}

	if len(result.Queues) == 0 {
		fmt.Println("No queue information available.")
		return nil
	}

	fmt.Printf("Found %d queues:\n\n", len(result.Queues))
	for i, queue := range result.Queues {
		fmt.Printf("Queue %d:\n", i+1)
		if queue.FunctionID != "" {
			fmt.Printf("  Function ID: %s\n", queue.FunctionID)
		}
		if queue.FunctionVersionID != "" {
			fmt.Printf("  Version ID: %s\n", queue.FunctionVersionID)
		}
		fmt.Printf("  Queue Size: %d\n", queue.Size)
		if queue.EstimatedWaitTime > 0 {
			waitTime := time.Duration(queue.EstimatedWaitTime) * time.Second
			fmt.Printf("  Estimated Wait Time: %s\n", waitTime.String())
		}
		fmt.Println()
	}

	return nil
}

func runQueuePosition(cmd *cobra.Command, args []string) error {
	requestID := args[0]

	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	c, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultTimeout)
	defer cancel()

	fmt.Printf("Getting queue position for request %s...\n", requestID)
	result, err := c.GetQueuePosition(ctx, requestID)
	if err != nil {
		return fmt.Errorf("failed to get queue position: %w", err)
	}

	if result.Position > 0 {
		if result.Position <= 1000 {
			fmt.Printf("Your request is at position %d in the queue.\n", result.Position)
		} else {
			fmt.Printf("Your request is at position %d+ in the queue (exact position shown up to 1000).\n", result.Position)
		}
	} else {
		fmt.Println("Your request is not currently in the queue (may be processing or completed).")
	}

	return nil
}
