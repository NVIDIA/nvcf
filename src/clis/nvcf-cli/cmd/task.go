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

	"github.com/spf13/cobra"
)

// ============================================================================
// Command Definitions
// ============================================================================

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage NVIDIA Cloud Tasks (NVCT)",
	Long: `Manage NVIDIA Cloud Tasks (NVCT) - GPU-backed batch jobs.

Authentication:
  NVCT requires its own API key. Run 'nvcf-cli init' first, then:
    nvcf-cli api-key generate --for task
  The key is saved separately from the NVCF API key and used automatically
  for all task subcommands.

Available subcommands:
  create          Create and launch a new task
  list            List tasks (optionally filtered by status)
  bulk            Get basic details for a set of task IDs
  get             Get full details for a single task
  delete          Delete a task
  cancel          Cancel a queued or running task
  events          Stream lifecycle events for a task
  results         List results emitted by a task
  update-secrets  Update user secrets associated with a task

Examples:
  # Launch a task from a JSON spec
  nvcf-cli task create --input-file task.json

  # List all running tasks
  nvcf-cli task list --status RUNNING

  # Get the current task (uses state set by 'task create')
  nvcf-cli task get

  # Cancel and then delete a task
  nvcf-cli task cancel <taskId>
  nvcf-cli task delete <taskId>`,
}

var taskCreateCmd = &cobra.Command{
	Use:          "create",
	Short:        "Create and launch a new task",
	SilenceUsage: true,
	Long: `Creates and launches a new NVCT task.

Input options:
  1. Use --input-file with a JSON configuration (recommended).
  2. Use individual flags for inline configuration.
  3. Combine both - flags override values from the JSON file.

Required fields:
  --name              Task name (1-128 chars, ^[a-z0-9A-Z][a-z0-9A-Z\-_]*$)
  --gpu               GPU name (gpuSpecification.gpu)
  --instance-type     Instance type (gpuSpecification.instanceType)

Example JSON file:
  {
    "name": "my-training-job",
    "gpuSpecification": {
      "gpu": "H100",
      "instanceType": "GPU.H100_1x",
      "backend": "GFN"
    },
    "containerImage": "my-registry/training:latest",
    "containerArgs": "--epochs 10",
    "containerEnvironment": [
      {"key": "MODEL", "value": "llama"}
    ],
    "maxRuntimeDuration": "PT4H",
    "resultHandlingStrategy": "UPLOAD",
    "resultsLocation": "my-org/my-team/my-model",
    "secrets": [
      {"name": "NGC_API_KEY", "value": "nvapi-..."}
    ]
  }`,
	RunE: runTaskCreate,
}

var taskListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List tasks",
	SilenceUsage: true,
	Long:         `Lists tasks for the authenticated NVIDIA Cloud Account, optionally filtered by status.`,
	RunE:         runTaskList,
}

var taskBulkCmd = &cobra.Command{
	Use:          "bulk",
	Short:        "Get basic details for a set of task IDs",
	SilenceUsage: true,
	Long: `Retrieves basic details (id, name, status) for an explicit list of task IDs.

Provide IDs via --task-ids or via --input-file pointing to a JSON file with the
shape { "taskIds": ["id1", "id2", ...] }.`,
	RunE: runTaskBulk,
}

var taskGetCmd = &cobra.Command{
	Use:          "get [taskId]",
	Short:        "Get task details",
	SilenceUsage: true,
	Long:         `Get full details for a task. If taskId is omitted, the current task from state is used.`,
	Args:         cobra.RangeArgs(0, 1),
	RunE:         runTaskGet,
}

var taskDeleteCmd = &cobra.Command{
	Use:          "delete [taskId]",
	Short:        "Delete a task",
	SilenceUsage: true,
	Long:         `Permanently deletes a task. If taskId is omitted, the current task from state is used.`,
	Args:         cobra.RangeArgs(0, 1),
	RunE:         runTaskDelete,
}

var taskCancelCmd = &cobra.Command{
	Use:          "cancel [taskId]",
	Short:        "Cancel a task",
	SilenceUsage: true,
	Long:         `Cancels a queued or running task. If taskId is omitted, the current task from state is used.`,
	Args:         cobra.RangeArgs(0, 1),
	RunE:         runTaskCancel,
}

var taskEventsCmd = &cobra.Command{
	Use:          "events [taskId]",
	Short:        "List task events",
	SilenceUsage: true,
	Long:         `List lifecycle events for a task (paginated). If taskId is omitted, the current task from state is used.`,
	Args:         cobra.RangeArgs(0, 1),
	RunE:         runTaskEvents,
}

var taskResultsCmd = &cobra.Command{
	Use:          "results [taskId]",
	Short:        "List task results",
	SilenceUsage: true,
	Long:         `List results emitted by a task (paginated). If taskId is omitted, the current task from state is used.`,
	Args:         cobra.RangeArgs(0, 1),
	RunE:         runTaskResults,
}

var taskUpdateSecretsCmd = &cobra.Command{
	Use:          "update-secrets [taskId]",
	Short:        "Update user secrets for a task",
	SilenceUsage: true,
	Long: `Update user secrets associated with a task.

Secrets are merged: supplied secrets are added or updated by name, and existing
secrets not included in the request are preserved. Provide secrets via
--secrets KEY=value pairs or via --input-file pointing to a JSON file shaped
like { "secrets": [{"name": "...", "value": "..."}] }.`,
	Args: cobra.RangeArgs(0, 1),
	RunE: runTaskUpdateSecrets,
}


// ============================================================================
// Configuration structs
// ============================================================================

// TaskCreateConfig represents the JSON input for `task create`. It is a
// near 1:1 mapping of CreateTaskRequest with secret values typed loosely so
// users can drop in either strings or objects.
type TaskCreateConfig struct {
	Name                           string                             `json:"name"`
	GpuSpecification               *TaskGpuSpecificationInput         `json:"gpuSpecification,omitempty"`
	ContainerImage                 string                             `json:"containerImage,omitempty"`
	ContainerArgs                  string                             `json:"containerArgs,omitempty"`
	ContainerEnvironment           []ContainerEnvironmentEntry        `json:"containerEnvironment,omitempty"`
	Models                         []ArtifactConfig                   `json:"models,omitempty"`
	Resources                      []ArtifactConfig                   `json:"resources,omitempty"`
	Tags                           []string                           `json:"tags,omitempty"`
	Description                    string                             `json:"description,omitempty"`
	MaxRuntimeDuration             string                             `json:"maxRuntimeDuration,omitempty"`
	MaxQueuedDuration              string                             `json:"maxQueuedDuration,omitempty"`
	TerminationGracePeriodDuration string                             `json:"terminationGracePeriodDuration,omitempty"`
	ResultHandlingStrategy         string                             `json:"resultHandlingStrategy,omitempty"`
	ResultsLocation                string                             `json:"resultsLocation,omitempty"`
	HelmChart                      string                             `json:"helmChart,omitempty"`
	Telemetries                    *TaskTelemetriesInput              `json:"telemetries,omitempty"`
	Secrets                        interface{}                        `json:"secrets,omitempty"` // []string or []SecretConfig
}

// TaskGpuSpecificationInput maps to GpuSpecificationDto.
type TaskGpuSpecificationInput struct {
	GPU                  string                   `json:"gpu"`
	Backend              string                   `json:"backend,omitempty"`
	Clusters             []string                 `json:"clusters,omitempty"`
	InstanceType         string                   `json:"instanceType"`
	Configuration        map[string]any           `json:"configuration,omitempty"`
	HelmValidationPolicy *TaskHelmValidationInput `json:"helmValidationPolicy,omitempty"`
}

// TaskHelmValidationInput maps to HelmValidationPolicyDto.
type TaskHelmValidationInput struct {
	Name                 string                  `json:"name"`
	ExtraKubernetesTypes []TaskKubernetesTypeIn  `json:"extraKubernetesTypes,omitempty"`
}

// TaskKubernetesTypeIn maps to KubernetesType.
type TaskKubernetesTypeIn struct {
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// TaskTelemetriesInput maps to TelemetriesDto for task creation.
type TaskTelemetriesInput struct {
	LogsTelemetryId    string `json:"logsTelemetryId,omitempty"`
	MetricsTelemetryId string `json:"metricsTelemetryId,omitempty"`
	TracesTelemetryId  string `json:"tracesTelemetryId,omitempty"`
}

// TaskUpdateSecretsConfig represents the JSON input for `task update-secrets`.
type TaskUpdateSecretsConfig struct {
	Secrets []SecretConfig `json:"secrets,omitempty"`
}

// TaskBulkConfig represents the JSON input for `task bulk`.
type TaskBulkConfig struct {
	TaskIDs []string `json:"taskIds"`
}

// ============================================================================
// Flag structs
// ============================================================================

var taskCreateFlags struct {
	inputFile string

	name           string
	gpu            string
	instanceType   string
	backend        string
	clusters       []string

	containerImage       string
	containerArgs        string
	containerEnvironment []string

	tags        []string
	description string

	maxRuntimeDuration             string
	maxQueuedDuration              string
	terminationGracePeriodDuration string

	resultHandlingStrategy string
	resultsLocation        string

	helmChart string

	logsTelemetryId    string
	metricsTelemetryId string
	tracesTelemetryId  string

	models    []string
	resources []string
	secrets   []string
}

var taskListFlags struct {
	limit  int
	status string
	cursor string
}

var taskBulkFlags struct {
	inputFile string
	taskIDs   []string
}

var taskGetFlags struct {
	includeSecrets bool
}

var taskPaginationFlags struct {
	limit  int
	cursor string
}

var taskResultsPaginationFlags struct {
	limit  int
	cursor string
}

var taskUpdateSecretsFlags struct {
	inputFile string
	secrets   []string
}


// ============================================================================
// Init
// ============================================================================

func init() {
	rootCmd.AddCommand(taskCmd)

	taskCmd.AddCommand(taskCreateCmd)
	taskCmd.AddCommand(taskListCmd)
	taskCmd.AddCommand(taskBulkCmd)
	taskCmd.AddCommand(taskGetCmd)
	taskCmd.AddCommand(taskDeleteCmd)
	taskCmd.AddCommand(taskCancelCmd)
	taskCmd.AddCommand(taskEventsCmd)
	taskCmd.AddCommand(taskResultsCmd)
	taskCmd.AddCommand(taskUpdateSecretsCmd)

	// task create flags
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.inputFile, "input-file", "", "JSON file with task configuration (overrides individual flags)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.name, "name", "", "Task name (required)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.gpu, "gpu", "", "GPU name, e.g. H100 (required)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.instanceType, "instance-type", "", "Instance type, e.g. GPU.H100_1x (required)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.backend, "backend", "", "Backend / CSP")
	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.clusters, "clusters", []string{}, "Specific clusters within instance/worker node")

	taskCreateCmd.Flags().StringVar(&taskCreateFlags.containerImage, "image", "", "Container image")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.containerArgs, "container-args", "", "Args passed when launching the container")
	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.containerEnvironment, "container-env", []string{}, "Container environment variables (key=value)")

	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.tags, "tags", []string{}, "Task tags (comma-separated)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.description, "description", "", "Task description")

	taskCreateCmd.Flags().StringVar(&taskCreateFlags.maxRuntimeDuration, "max-runtime", "", "Max runtime duration as ISO 8601 (e.g. PT4H30M)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.maxQueuedDuration, "max-queued", "", "Max queued duration as ISO 8601 (default PT72H)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.terminationGracePeriodDuration, "termination-grace", "", "Termination grace period as ISO 8601 (default PT1H)")

	taskCreateCmd.Flags().StringVar(&taskCreateFlags.resultHandlingStrategy, "result-strategy", "", "Result handling strategy (UPLOAD or NONE)")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.resultsLocation, "results-location", "", "Results location (org-name/[team-name/]model-name) - required when result-strategy=UPLOAD")

	taskCreateCmd.Flags().StringVar(&taskCreateFlags.helmChart, "helm-chart", "", "Optional Helm Chart")

	taskCreateCmd.Flags().StringVar(&taskCreateFlags.logsTelemetryId, "logs-telemetry-id", "", "UUID for logs telemetry")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.metricsTelemetryId, "metrics-telemetry-id", "", "UUID for metrics telemetry")
	taskCreateCmd.Flags().StringVar(&taskCreateFlags.tracesTelemetryId, "traces-telemetry-id", "", "UUID for traces telemetry")

	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.models, "models", []string{}, "Model artifacts (format: name:version:uri)")
	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.resources, "resources", []string{}, "Resource artifacts (format: name:version:uri)")
	taskCreateCmd.Flags().StringSliceVar(&taskCreateFlags.secrets, "secrets", []string{}, "Secrets in name=value format (e.g. NGC_API_KEY=nvapi-...)")

	// task list flags
	taskListCmd.Flags().IntVar(&taskListFlags.limit, "limit", 0, "Maximum number of tasks to return")
	taskListCmd.Flags().StringVar(&taskListFlags.status, "status", "", "Filter by status (QUEUED, LAUNCHED, RUNNING, ERRORED, CANCELED, EXCEEDED_MAX_RUNTIME_DURATION, EXCEEDED_MAX_QUEUED_DURATION, COMPLETED)")
	taskListCmd.Flags().StringVar(&taskListFlags.cursor, "cursor", "", "Pagination cursor returned by previous response")

	// task bulk flags
	taskBulkCmd.Flags().StringVar(&taskBulkFlags.inputFile, "input-file", "", "JSON file with { taskIds: [...] } payload")
	taskBulkCmd.Flags().StringSliceVar(&taskBulkFlags.taskIDs, "task-ids", []string{}, "Task IDs (comma-separated or repeated)")

	// task get flags
	taskGetCmd.Flags().BoolVar(&taskGetFlags.includeSecrets, "include-secrets", false, "Include secret values in the response (subject to authorization)")

	// task events flags
	taskEventsCmd.Flags().IntVar(&taskPaginationFlags.limit, "limit", 0, "Maximum number of events to return")
	taskEventsCmd.Flags().StringVar(&taskPaginationFlags.cursor, "cursor", "", "Pagination cursor returned by previous response")

	// task results flags
	taskResultsCmd.Flags().IntVar(&taskResultsPaginationFlags.limit, "limit", 0, "Maximum number of results to return")
	taskResultsCmd.Flags().StringVar(&taskResultsPaginationFlags.cursor, "cursor", "", "Pagination cursor returned by previous response")

	// task update-secrets flags
	taskUpdateSecretsCmd.Flags().StringVar(&taskUpdateSecretsFlags.inputFile, "input-file", "", "JSON file with { secrets: [{name, value}, ...] } payload")
	taskUpdateSecretsCmd.Flags().StringSliceVar(&taskUpdateSecretsFlags.secrets, "secrets", []string{}, "Secrets in name=value format (e.g. NGC_API_KEY=nvapi-...)")

}

// ============================================================================
// Helpers
// ============================================================================

// resolveTaskID returns the task ID from positional args, or falls back to the
// current task in state.
func resolveTaskID(args []string) (string, error) {
	if len(args) >= 1 && args[0] != "" {
		return args[0], nil
	}
	if !HasCurrentTask() {
		if err := LoadStateForCurrentCommand(); err != nil {
			logging.Warning("Could not load existing state: %v", err)
		}
	}
	if HasCurrentTask() {
		taskID := GetCurrentState().TaskID
		logging.Info("Using task ID from state: %s", taskID)
		return taskID, nil
	}
	return "", fmt.Errorf("task ID is required - provide as argument or run 'nvcf-cli task create' first")
}

// HasCurrentTask reports whether the current state context has a task ID set.
func HasCurrentTask() bool {
	sm := GetStateManagerForCurrentCommand()
	return sm.HasTask()
}

// SetCurrentTask saves a task ID to the active state context.
func SetCurrentTask(taskID, taskName string) {
	sm := GetStateManagerForCurrentCommand()
	sm.SetTask(taskID, taskName)
}

// parseSecretsList converts CLI/JSON `name=value` style entries (or a slice of
// SecretConfig objects) into the API-friendly []client.SecretDto form.
func parseSecretsList(raw interface{}, cliPairs []string) ([]client.SecretDto, error) {
	var secrets []client.SecretDto

	for _, pair := range cliPairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid secret format %q: must be name=value", pair)
		}
		secrets = append(secrets, client.SecretDto{Name: parts[0], Value: parts[1]})
	}

	if raw == nil {
		return secrets, nil
	}

	switch v := raw.(type) {
	case []interface{}:
		for _, item := range v {
			switch s := item.(type) {
			case string:
				parts := strings.SplitN(s, "=", 2)
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid secret format %q: must be name=value", s)
				}
				secrets = append(secrets, client.SecretDto{Name: parts[0], Value: parts[1]})
			case map[string]interface{}:
				secret := client.SecretDto{}
				if name, ok := s["name"].(string); ok {
					secret.Name = name
				}
				if val, ok := s["value"]; ok {
					secret.Value = val
				}
				secrets = append(secrets, secret)
			default:
				return nil, fmt.Errorf("unsupported secret entry type: %T", item)
			}
		}
	case []SecretConfig:
		for _, s := range v {
			secrets = append(secrets, client.SecretDto{Name: s.Name, Value: s.Value})
		}
	default:
		return nil, fmt.Errorf("unsupported secrets payload type: %T", raw)
	}

	return secrets, nil
}

// parseArtifactsList converts a slice of ArtifactConfig + CLI flag strings
// ("name:version:uri") into the wire-level []client.ArtifactDto.
func parseArtifactsList(specs []ArtifactConfig, cliEntries []string) ([]client.ArtifactDto, error) {
	var artifacts []client.ArtifactDto
	for _, a := range specs {
		artifacts = append(artifacts, client.ArtifactDto{Name: a.Name, Version: a.Version, URI: a.URI})
	}
	for _, raw := range cliEntries {
		parsed, err := parseArtifactString(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid artifact %q: %w", raw, err)
		}
		artifacts = append(artifacts, client.ArtifactDto{Name: parsed.Name, Version: parsed.Version, URI: parsed.URI})
	}
	return artifacts, nil
}

func loadTaskCreateConfig(cmd *cobra.Command) (*TaskCreateConfig, error) {
	cfg := &TaskCreateConfig{}

	if taskCreateFlags.inputFile != "" {
		data, err := os.ReadFile(taskCreateFlags.inputFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read input file %q: %w", taskCreateFlags.inputFile, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse JSON file %q: %w", taskCreateFlags.inputFile, err)
		}
		if !IsJSONOutput() {
			fmt.Printf("Loaded task configuration from %s\n", taskCreateFlags.inputFile)
		}
	}

	// CLI overrides
	if cmd.Flags().Changed("name") {
		cfg.Name = taskCreateFlags.name
	}
	if cmd.Flags().Changed("gpu") || cmd.Flags().Changed("instance-type") || cmd.Flags().Changed("backend") || cmd.Flags().Changed("clusters") {
		if cfg.GpuSpecification == nil {
			cfg.GpuSpecification = &TaskGpuSpecificationInput{}
		}
		if cmd.Flags().Changed("gpu") {
			cfg.GpuSpecification.GPU = taskCreateFlags.gpu
		}
		if cmd.Flags().Changed("instance-type") {
			cfg.GpuSpecification.InstanceType = taskCreateFlags.instanceType
		}
		if cmd.Flags().Changed("backend") {
			cfg.GpuSpecification.Backend = taskCreateFlags.backend
		}
		if cmd.Flags().Changed("clusters") {
			cfg.GpuSpecification.Clusters = taskCreateFlags.clusters
		}
	}
	if cmd.Flags().Changed("image") {
		cfg.ContainerImage = taskCreateFlags.containerImage
	}
	if cmd.Flags().Changed("container-args") {
		cfg.ContainerArgs = taskCreateFlags.containerArgs
	}
	if cmd.Flags().Changed("container-env") {
		var env []ContainerEnvironmentEntry
		for _, e := range taskCreateFlags.containerEnvironment {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid container-env %q: expected key=value", e)
			}
			env = append(env, ContainerEnvironmentEntry{Key: parts[0], Value: parts[1]})
		}
		cfg.ContainerEnvironment = env
	}
	if cmd.Flags().Changed("tags") {
		cfg.Tags = taskCreateFlags.tags
	}
	if cmd.Flags().Changed("description") {
		cfg.Description = taskCreateFlags.description
	}
	if cmd.Flags().Changed("max-runtime") {
		cfg.MaxRuntimeDuration = taskCreateFlags.maxRuntimeDuration
	}
	if cmd.Flags().Changed("max-queued") {
		cfg.MaxQueuedDuration = taskCreateFlags.maxQueuedDuration
	}
	if cmd.Flags().Changed("termination-grace") {
		cfg.TerminationGracePeriodDuration = taskCreateFlags.terminationGracePeriodDuration
	}
	if cmd.Flags().Changed("result-strategy") {
		cfg.ResultHandlingStrategy = taskCreateFlags.resultHandlingStrategy
	}
	if cmd.Flags().Changed("results-location") {
		cfg.ResultsLocation = taskCreateFlags.resultsLocation
	}
	if cmd.Flags().Changed("helm-chart") {
		cfg.HelmChart = taskCreateFlags.helmChart
	}
	if cmd.Flags().Changed("logs-telemetry-id") || cmd.Flags().Changed("metrics-telemetry-id") || cmd.Flags().Changed("traces-telemetry-id") {
		if cfg.Telemetries == nil {
			cfg.Telemetries = &TaskTelemetriesInput{}
		}
		if cmd.Flags().Changed("logs-telemetry-id") {
			cfg.Telemetries.LogsTelemetryId = taskCreateFlags.logsTelemetryId
		}
		if cmd.Flags().Changed("metrics-telemetry-id") {
			cfg.Telemetries.MetricsTelemetryId = taskCreateFlags.metricsTelemetryId
		}
		if cmd.Flags().Changed("traces-telemetry-id") {
			cfg.Telemetries.TracesTelemetryId = taskCreateFlags.tracesTelemetryId
		}
	}
	if cmd.Flags().Changed("models") {
		var models []ArtifactConfig
		for _, raw := range taskCreateFlags.models {
			parsed, err := parseArtifactString(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid model %q: %w", raw, err)
			}
			models = append(models, parsed)
		}
		cfg.Models = models
	}
	if cmd.Flags().Changed("resources") {
		var resources []ArtifactConfig
		for _, raw := range taskCreateFlags.resources {
			parsed, err := parseArtifactString(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid resource %q: %w", raw, err)
			}
			resources = append(resources, parsed)
		}
		cfg.Resources = resources
	}
	if cmd.Flags().Changed("secrets") {
		// Secrets from CLI flags are merged in runTaskCreate via parseSecretsList.
	}

	return cfg, nil
}

// ============================================================================
// Run functions
// ============================================================================

func runTaskCreate(cmd *cobra.Command, args []string) error {
	cfg, err := loadTaskCreateConfig(cmd)
	if err != nil {
		return err
	}

	if cfg.Name == "" {
		return fmt.Errorf("task name is required (use --name or specify in JSON file)")
	}
	if cfg.GpuSpecification == nil {
		return fmt.Errorf("gpuSpecification is required (use --gpu and --instance-type or specify in JSON file)")
	}
	if cfg.GpuSpecification.GPU == "" {
		return fmt.Errorf("gpuSpecification.gpu is required")
	}
	if cfg.GpuSpecification.InstanceType == "" {
		return fmt.Errorf("gpuSpecification.instanceType is required")
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

	containerEnv := make([]client.ContainerEnvironmentEntry, 0, len(cfg.ContainerEnvironment))
	for _, e := range cfg.ContainerEnvironment {
		containerEnv = append(containerEnv, client.ContainerEnvironmentEntry{Key: e.Key, Value: e.Value})
	}

	models, err := parseArtifactsList(cfg.Models, nil)
	if err != nil {
		return err
	}
	resources, err := parseArtifactsList(cfg.Resources, nil)
	if err != nil {
		return err
	}

	secrets, err := parseSecretsList(cfg.Secrets, taskCreateFlags.secrets)
	if err != nil {
		return err
	}

	gpuSpec := &client.GpuSpecificationDto{
		GPU:           cfg.GpuSpecification.GPU,
		Backend:       cfg.GpuSpecification.Backend,
		Clusters:      cfg.GpuSpecification.Clusters,
		InstanceType:  cfg.GpuSpecification.InstanceType,
		Configuration: cfg.GpuSpecification.Configuration,
	}
	if cfg.GpuSpecification.HelmValidationPolicy != nil {
		hvp := &client.HelmValidationPolicyDto{Name: cfg.GpuSpecification.HelmValidationPolicy.Name}
		for _, kt := range cfg.GpuSpecification.HelmValidationPolicy.ExtraKubernetesTypes {
			hvp.ExtraKubernetesTypes = append(hvp.ExtraKubernetesTypes, client.KubernetesType{
				Group:   kt.Group,
				Version: kt.Version,
				Kind:    kt.Kind,
			})
		}
		gpuSpec.HelmValidationPolicy = hvp
	}

	var telemetries *client.TelemetriesDto
	if cfg.Telemetries != nil && (cfg.Telemetries.LogsTelemetryId != "" || cfg.Telemetries.MetricsTelemetryId != "" || cfg.Telemetries.TracesTelemetryId != "") {
		telemetries = &client.TelemetriesDto{
			LogsTelemetryId:    cfg.Telemetries.LogsTelemetryId,
			MetricsTelemetryId: cfg.Telemetries.MetricsTelemetryId,
			TracesTelemetryId:  cfg.Telemetries.TracesTelemetryId,
		}
	}

	req := &client.CreateTaskRequest{
		Name:                           cfg.Name,
		GpuSpecification:               gpuSpec,
		ContainerImage:                 cfg.ContainerImage,
		ContainerArgs:                  cfg.ContainerArgs,
		ContainerEnvironment:           containerEnv,
		Models:                         models,
		Resources:                      resources,
		Tags:                           cfg.Tags,
		Description:                    cfg.Description,
		MaxRuntimeDuration:             cfg.MaxRuntimeDuration,
		MaxQueuedDuration:              cfg.MaxQueuedDuration,
		TerminationGracePeriodDuration: cfg.TerminationGracePeriodDuration,
		ResultHandlingStrategy:         cfg.ResultHandlingStrategy,
		ResultsLocation:                cfg.ResultsLocation,
		HelmChart:                      cfg.HelmChart,
		Telemetries:                    telemetries,
		Secrets:                        secrets,
	}

	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load existing state: %v", err)
	}

	logging.Info("Creating task %q...", cfg.Name)
	resp, err := nvcfClient.CreateTask(context.Background(), req)
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	SetCurrentTask(resp.Task.ID, resp.Task.Name)
	if err := SaveStateForCurrentCommand(); err != nil {
		logging.Warning("Failed to save task state: %v", err)
	}

	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	logging.Success("Task created successfully!")
	logging.Plain("Task ID: %s", resp.Task.ID)
	logging.Plain("Name: %s", resp.Task.Name)
	logging.Plain("Status: %s", resp.Task.Status)
	if resp.Task.GpuSpecification != nil {
		logging.Plain("GPU: %s (%s)", resp.Task.GpuSpecification.GPU, resp.Task.GpuSpecification.InstanceType)
	}
	if resp.Task.CreatedAt != "" {
		logging.Plain("Created: %s", resp.Task.CreatedAt)
	}
	return nil
}

func runTaskList(cmd *cobra.Command, args []string) error {
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	opts := &client.ListTasksOptions{
		Limit:  taskListFlags.limit,
		Status: taskListFlags.status,
		Cursor: taskListFlags.cursor,
	}

	if !IsJSONOutput() {
		fmt.Println("Listing tasks...")
	}

	result, err := c.ListTasks(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(result)
	}

	if len(result.Tasks) == 0 {
		fmt.Println("No tasks found.")
		return nil
	}

	fmt.Printf("Found %d tasks:\n\n", len(result.Tasks))
	for _, t := range result.Tasks {
		fmt.Printf("ID: %s\n", t.ID)
		fmt.Printf("Name: %s\n", t.Name)
		fmt.Printf("Status: %s\n", t.Status)
		if t.GpuSpecification != nil {
			fmt.Printf("GPU: %s (%s)\n", t.GpuSpecification.GPU, t.GpuSpecification.InstanceType)
		}
		if t.CreatedAt != "" {
			fmt.Printf("Created: %s\n", t.CreatedAt)
		}
		if t.PercentComplete > 0 {
			fmt.Printf("Progress: %d%%\n", t.PercentComplete)
		}
		fmt.Println("---")
	}
	if result.Cursor != "" {
		fmt.Printf("Next cursor: %s (limit=%d)\n", result.Cursor, result.Limit)
	}
	return nil
}

func runTaskBulk(cmd *cobra.Command, args []string) error {
	taskIDs := taskBulkFlags.taskIDs

	if taskBulkFlags.inputFile != "" {
		data, err := os.ReadFile(taskBulkFlags.inputFile)
		if err != nil {
			return fmt.Errorf("failed to read input file %q: %w", taskBulkFlags.inputFile, err)
		}
		var cfg TaskBulkConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("failed to parse JSON file %q: %w", taskBulkFlags.inputFile, err)
		}
		taskIDs = append(taskIDs, cfg.TaskIDs...)
	}

	if len(taskIDs) == 0 {
		return fmt.Errorf("at least one task ID is required (--task-ids or --input-file)")
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	result, err := c.ListBasicTaskDetails(context.Background(), taskIDs)
	if err != nil {
		return fmt.Errorf("failed to fetch bulk task details: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(result)
	}

	fmt.Printf("Account: %s\n", result.NCAID)
	fmt.Printf("Found %d tasks:\n\n", len(result.Tasks))
	for _, t := range result.Tasks {
		fmt.Printf("  %s  %s  %s\n", t.ID, t.Status, t.Name)
	}
	return nil
}

func runTaskGet(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	resp, err := c.GetTask(context.Background(), taskID, taskGetFlags.includeSecrets)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	t := resp.Task
	fmt.Println("\nTask Details:")
	fmt.Println("=============")
	fmt.Printf("ID: %s\n", t.ID)
	fmt.Printf("Name: %s\n", t.Name)
	fmt.Printf("NCA ID: %s\n", t.NCAID)
	fmt.Printf("Status: %s\n", t.Status)
	if t.PercentComplete > 0 {
		fmt.Printf("Progress: %d%%\n", t.PercentComplete)
	}
	if t.CreatedAt != "" {
		fmt.Printf("Created: %s\n", t.CreatedAt)
	}
	if t.LastUpdatedAt != "" {
		fmt.Printf("Last Updated: %s\n", t.LastUpdatedAt)
	}
	if t.LastHeartbeatAt != "" {
		fmt.Printf("Last Heartbeat: %s\n", t.LastHeartbeatAt)
	}
	if t.Description != "" {
		fmt.Printf("Description: %s\n", t.Description)
	}
	if t.GpuSpecification != nil {
		fmt.Println("\nGPU Specification:")
		fmt.Printf("  GPU: %s\n", t.GpuSpecification.GPU)
		fmt.Printf("  Instance Type: %s\n", t.GpuSpecification.InstanceType)
		if t.GpuSpecification.Backend != "" {
			fmt.Printf("  Backend: %s\n", t.GpuSpecification.Backend)
		}
		if len(t.GpuSpecification.Clusters) > 0 {
			fmt.Printf("  Clusters: %v\n", t.GpuSpecification.Clusters)
		}
	}
	if t.ContainerImage != "" {
		fmt.Println("\nContainer:")
		fmt.Printf("  Image: %s\n", t.ContainerImage)
		if t.ContainerArgs != "" {
			fmt.Printf("  Args: %s\n", t.ContainerArgs)
		}
		for _, env := range t.ContainerEnvironment {
			fmt.Printf("  Env: %s=%s\n", env.Key, env.Value)
		}
	}
	if len(t.Tags) > 0 {
		fmt.Printf("\nTags: %s\n", strings.Join(t.Tags, ", "))
	}
	if len(t.Secrets) > 0 {
		fmt.Printf("\nSecret keys: %s\n", strings.Join(t.Secrets, ", "))
	}
	if t.ResultHandlingStrategy != "" {
		fmt.Printf("\nResult Strategy: %s\n", t.ResultHandlingStrategy)
		if t.ResultsLocation != "" {
			fmt.Printf("Results Location: %s\n", t.ResultsLocation)
		}
	}
	if len(t.Instances) > 0 {
		fmt.Printf("\nInstances (%d):\n", len(t.Instances))
		for _, inst := range t.Instances {
			fmt.Printf("  - %s [%s] %s @ %s/%s\n", inst.InstanceID, inst.InstanceState, inst.InstanceType, inst.Backend, inst.Location)
		}
	}
	return nil
}

func runTaskDelete(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	logging.Info("Deleting task %s...", taskID)
	if err := c.DeleteTask(context.Background(), taskID); err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	logging.Success("Task %s deleted.", taskID)

	if err := LoadStateForCurrentCommand(); err == nil {
		if GetCurrentState().TaskID == taskID {
			GetStateManagerForCurrentCommand().ClearTask()
			if err := SaveStateForCurrentCommand(); err != nil {
				logging.Warning("Failed to clear task from state: %v", err)
			}
		}
	}
	return nil
}

func runTaskCancel(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	logging.Info("Canceling task %s...", taskID)
	resp, err := c.CancelTask(context.Background(), taskID)
	if err != nil {
		return fmt.Errorf("failed to cancel task: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(resp)
	}
	logging.Success("Task %s canceled (status=%s).", resp.Task.ID, resp.Task.Status)
	return nil
}

func runTaskEvents(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	resp, err := c.GetTaskEvents(context.Background(), taskID, &client.PaginationOptions{
		Limit:  taskPaginationFlags.limit,
		Cursor: taskPaginationFlags.cursor,
	})
	if err != nil {
		return fmt.Errorf("failed to get task events: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	if len(resp.Events) == 0 {
		fmt.Println("No events found.")
		return nil
	}
	fmt.Printf("Found %d events:\n\n", len(resp.Events))
	for _, e := range resp.Events {
		fmt.Printf("[%s] %s\n", e.CreatedAt, e.Message)
	}
	if resp.Cursor != "" {
		fmt.Printf("\nNext cursor: %s (limit=%d)\n", resp.Cursor, resp.Limit)
	}
	return nil
}

func runTaskResults(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	resp, err := c.GetTaskResults(context.Background(), taskID, &client.PaginationOptions{
		Limit:  taskResultsPaginationFlags.limit,
		Cursor: taskResultsPaginationFlags.cursor,
	})
	if err != nil {
		return fmt.Errorf("failed to get task results: %w", err)
	}

	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	if len(resp.Results) == 0 {
		fmt.Println("No results found.")
		return nil
	}
	fmt.Printf("Found %d results:\n\n", len(resp.Results))
	for _, r := range resp.Results {
		fmt.Printf("- %s (%s) created %s\n", r.Name, r.ResultID, r.CreatedAt)
		if len(r.Metadata) > 0 {
			meta, _ := json.MarshalIndent(r.Metadata, "  ", "  ")
			fmt.Printf("  metadata: %s\n", string(meta))
		}
	}
	if resp.Cursor != "" {
		fmt.Printf("\nNext cursor: %s (limit=%d)\n", resp.Cursor, resp.Limit)
	}
	return nil
}

func runTaskUpdateSecrets(cmd *cobra.Command, args []string) error {
	taskID, err := resolveTaskID(args)
	if err != nil {
		return err
	}

	var rawSecrets interface{}
	if taskUpdateSecretsFlags.inputFile != "" {
		data, err := os.ReadFile(taskUpdateSecretsFlags.inputFile)
		if err != nil {
			return fmt.Errorf("failed to read input file %q: %w", taskUpdateSecretsFlags.inputFile, err)
		}
		var cfg TaskUpdateSecretsConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("failed to parse JSON file %q: %w", taskUpdateSecretsFlags.inputFile, err)
		}
		rawSecrets = cfg.Secrets
	}

	secrets, err := parseSecretsList(rawSecrets, taskUpdateSecretsFlags.secrets)
	if err != nil {
		return err
	}
	if len(secrets) == 0 {
		return fmt.Errorf("at least one secret must be provided (use --secrets or --input-file)")
	}

	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	logging.Info("Updating secrets for task %s...", taskID)
	if err := c.UpdateTaskSecrets(context.Background(), taskID, secrets); err != nil {
		return fmt.Errorf("failed to update task secrets: %w", err)
	}
	logging.Success("Updated %d secret(s) for task %s.", len(secrets), taskID)
	return nil
}

