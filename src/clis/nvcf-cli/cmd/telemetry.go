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
	"io"
	"os"
	"sort"
	"strings"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

const (
	telemetryTypeLogs    = "LOGS"
	telemetryTypeMetrics = "METRICS"
	telemetryTypeTraces  = "TRACES"
)

// telemetryProviderSpec records what the BYOO collector can render for a
// provider: the signal types it exports and the secret shape it reads.
type telemetryProviderSpec struct {
	types []string
	// secretKeys lists the JSON object keys the collector reads. An empty
	// list means the secret is a plain string token.
	secretKeys []string
	// tlsSecret marks providers whose secret is an object of optional TLS
	// material (clientCert and clientKey together, caFile, or both).
	tlsSecret bool
}

var telemetryProviderSpecs = map[string]telemetryProviderSpec{
	"AZURE_MONITOR": {
		types:      []string{telemetryTypeLogs, telemetryTypeMetrics, telemetryTypeTraces},
		secretKeys: []string{"instrumentationKey", "applicationId", "liveEndpoint"},
	},
	"DATADOG": {
		types: []string{telemetryTypeLogs, telemetryTypeMetrics, telemetryTypeTraces},
	},
	"GRAFANA_CLOUD": {
		types:      []string{telemetryTypeLogs, telemetryTypeMetrics, telemetryTypeTraces},
		secretKeys: []string{"instanceId", "apiKey"},
	},
	"KRATOS": {
		types:      []string{telemetryTypeLogs},
		secretKeys: []string{"collectorId", "clientCert", "clientKey"},
	},
	"KRATOS_THANOS": {
		types:     []string{telemetryTypeMetrics},
		tlsSecret: true,
	},
	"OTEL_COLLECTOR": {
		types: []string{telemetryTypeLogs, telemetryTypeMetrics, telemetryTypeTraces},
	},
	"PROMETHEUS": {
		types:     []string{telemetryTypeMetrics},
		tlsSecret: true,
	},
	"SERVICENOW": {
		types: []string{telemetryTypeTraces},
	},
	"SPLUNK": {
		types: []string{telemetryTypeLogs},
	},
}

var telemetryProtocols = []string{"HTTP", "GRPC"}

var telemetryTLSSecretKeys = []string{"clientCert", "clientKey", "caFile"}

var telemetryCmd = &cobra.Command{
	Use:   "telemetry",
	Short: "Manage telemetry endpoints",
	Long: `Manage telemetry endpoints for Bring Your Own Observability (BYOO).

A telemetry endpoint tells the OpenTelemetry collector sidecar where to export
a function's or task's logs, metrics, or traces and which credentials to use.
Attach an endpoint with --logs-telemetry-id, --metrics-telemetry-id, or
--traces-telemetry-id on "function create" or "task create".

Endpoint management requires the 'manage_telemetries' scope, which the
default function API key includes. Updating credentials also requires the
'update_secrets' scope, which it does not; see "telemetry update-secret --help".

Available Commands:
  create          Register a telemetry endpoint
  list            List telemetry endpoints
  get             Get a telemetry endpoint
  delete          Delete a telemetry endpoint
  update-secret   Replace the credentials of a telemetry endpoint

` + telemetryProviderHelp(),
}

var telemetryCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Register a telemetry endpoint",
	Long: `Register a telemetry endpoint with the BYOO collector.

The name is also the name of the stored secret and must be unique in the
account. Pass the secret with --secret-file (a path, or '-' for standard input)
or --secret. A value that starts with '{' is sent as a JSON object; anything
else is sent as a string.

` + telemetryProviderHelp() + `
Examples:
  # Grafana Cloud for logs, metrics and traces
  nvcf-cli telemetry create \
    --name grafana \
    --endpoint https://otlp-gateway.example.com/otlp \
    --provider GRAFANA_CLOUD \
    --protocol HTTP \
    --type LOGS --type METRICS --type TRACES \
    --secret-file grafana-secret.json

  # Generic OTLP collector, token from standard input
  echo -n "$TOKEN" | nvcf-cli telemetry create \
    --name my-otlp \
    --endpoint https://collector.example.com:4318 \
    --provider OTEL_COLLECTOR \
    --type TRACES \
    --secret-file -`,
	Args: cobra.NoArgs,
	RunE: runTelemetryCreate,
}

var telemetryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List telemetry endpoints",
	Long: `List the telemetry endpoints registered in the authenticated account.

Example:
  nvcf-cli telemetry list`,
	Args: cobra.NoArgs,
	RunE: runTelemetryList,
}

var telemetryGetCmd = &cobra.Command{
	Use:   "get <telemetry-id>",
	Short: "Get a telemetry endpoint",
	Long: `Get the details of a telemetry endpoint. Credentials are never returned.

Example:
  nvcf-cli telemetry get 12345678-1234-1234-1234-123456789012`,
	Args: cobra.ExactArgs(1),
	RunE: runTelemetryGet,
}

var telemetryDeleteCmd = &cobra.Command{
	Use:   "delete <telemetry-id>",
	Short: "Delete a telemetry endpoint",
	Long: `Delete a telemetry endpoint and its stored credentials.

The API rejects the request while a function still references the endpoint.

Examples:
  nvcf-cli telemetry delete 12345678-1234-1234-1234-123456789012

  # Skip confirmation prompt
  nvcf-cli telemetry delete 12345678-1234-1234-1234-123456789012 --force`,
	Args: cobra.ExactArgs(1),
	RunE: runTelemetryDelete,
}

var telemetryUpdateSecretCmd = &cobra.Command{
	Use:   "update-secret <telemetry-id>",
	Short: "Replace the credentials of a telemetry endpoint",
	Long: `Replace the stored credentials of a telemetry endpoint in the
authenticated account. The new secret must match the endpoint's provider.
Running workloads pick up the new value without a redeploy.

Requires the 'update_secrets' scope, plus 'manage_telemetries' to look up the
endpoint. The default function API key does not include 'update_secrets'.
Generate a function key that has both; --scopes replaces the default scopes,
so list every scope the key needs:

  nvcf-cli api-key generate --for function \
    --scopes manage_telemetries,update_secrets

Cross-account updates use "admin secrets update-telemetry".

Examples:
  nvcf-cli telemetry update-secret 12345678-1234-1234-1234-123456789012 \
    --secret-file grafana-secret.json

  echo -n "$TOKEN" | nvcf-cli telemetry update-secret \
    12345678-1234-1234-1234-123456789012 --secret-file -`,
	Args: cobra.ExactArgs(1),
	RunE: runTelemetryUpdateSecret,
}

var telemetryCreateFlags struct {
	name       string
	endpoint   string
	provider   string
	protocol   string
	types      []string
	secret     string
	secretFile string
}

var telemetrySecretFlags struct {
	secret     string
	secretFile string
}

var telemetryDeleteFlags struct {
	force bool
}

// telemetryStdin is the reader used for --secret-file -.
var telemetryStdin io.Reader = os.Stdin

func init() {
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.name, "name", "", "Telemetry name, also used as the secret name (required)")
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.endpoint, "endpoint", "", "Telemetry endpoint URL (required)")
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.provider, "provider", "", "Telemetry provider (required): "+strings.Join(sortedTelemetryProviders(), ", "))
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.protocol, "protocol", "HTTP", "Export protocol: "+strings.Join(telemetryProtocols, " or "))
	telemetryCreateCmd.Flags().StringSliceVar(&telemetryCreateFlags.types, "type", nil, "Telemetry type: LOGS, METRICS or TRACES (required, repeatable or comma-separated)")
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.secretFile, "secret-file", "", "File with the secret value, or '-' for standard input")
	telemetryCreateCmd.Flags().StringVar(&telemetryCreateFlags.secret, "secret", "", "Secret value (WARNING: visible in process listings; prefer --secret-file)")
	_ = telemetryCreateCmd.MarkFlagRequired("name")
	_ = telemetryCreateCmd.MarkFlagRequired("endpoint")
	_ = telemetryCreateCmd.MarkFlagRequired("provider")
	_ = telemetryCreateCmd.MarkFlagRequired("type")

	telemetryUpdateSecretCmd.Flags().StringVar(&telemetrySecretFlags.secretFile, "secret-file", "", "File with the secret value, or '-' for standard input")
	telemetryUpdateSecretCmd.Flags().StringVar(&telemetrySecretFlags.secret, "secret", "", "Secret value (WARNING: visible in process listings; prefer --secret-file)")

	telemetryDeleteCmd.Flags().BoolVar(&telemetryDeleteFlags.force, "force", false, "Skip confirmation prompt")

	telemetryCmd.AddCommand(telemetryCreateCmd)
	telemetryCmd.AddCommand(telemetryListCmd)
	telemetryCmd.AddCommand(telemetryGetCmd)
	telemetryCmd.AddCommand(telemetryDeleteCmd)
	telemetryCmd.AddCommand(telemetryUpdateSecretCmd)

	rootCmd.AddCommand(telemetryCmd)
}

func sortedTelemetryProviders() []string {
	providers := make([]string, 0, len(telemetryProviderSpecs))
	for p := range telemetryProviderSpecs {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return providers
}

func telemetryProviderHelp() string {
	var b strings.Builder
	b.WriteString("Supported providers (types; secret):\n")
	for _, p := range sortedTelemetryProviders() {
		spec := telemetryProviderSpecs[p]
		fmt.Fprintf(&b, "  %-15s %s; %s\n", p, strings.Join(spec.types, ", "), telemetrySecretDescription(spec))
	}
	return b.String()
}

func telemetrySecretDescription(spec telemetryProviderSpec) string {
	switch {
	case spec.tlsSecret:
		return `JSON object with optional "clientCert" and "clientKey" (together) and "caFile"`
	case len(spec.secretKeys) > 0:
		return "JSON object with " + quoteJoin(spec.secretKeys)
	default:
		return "string token"
	}
}

func quoteJoin(keys []string) string {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = `"` + k + `"`
	}
	return strings.Join(quoted, ", ")
}

func normalizeTelemetryProvider(provider string) (string, error) {
	p := strings.ToUpper(strings.TrimSpace(provider))
	if _, ok := telemetryProviderSpecs[p]; !ok {
		return "", fmt.Errorf("unsupported telemetry provider %q: must be one of %s",
			provider, strings.Join(sortedTelemetryProviders(), ", "))
	}
	return p, nil
}

func normalizeTelemetryProtocol(protocol string) (string, error) {
	p := strings.ToUpper(strings.TrimSpace(protocol))
	for _, supported := range telemetryProtocols {
		if p == supported {
			return p, nil
		}
	}
	return "", fmt.Errorf("unsupported telemetry protocol %q: must be one of %s",
		protocol, strings.Join(telemetryProtocols, ", "))
}

// normalizeTelemetryTypes upper-cases and de-duplicates the requested types
// and checks that the collector can export each of them for the provider.
func normalizeTelemetryTypes(provider string, types []string) ([]string, error) {
	spec := telemetryProviderSpecs[provider]
	seen := map[string]bool{}
	var result []string
	for _, raw := range types {
		t := strings.ToUpper(strings.TrimSpace(raw))
		if t == "" || seen[t] {
			continue
		}
		switch t {
		case telemetryTypeLogs, telemetryTypeMetrics, telemetryTypeTraces:
		default:
			return nil, fmt.Errorf("unsupported telemetry type %q: must be LOGS, METRICS or TRACES", raw)
		}
		if !containsString(spec.types, t) {
			return nil, fmt.Errorf("provider %s does not support %s: supported types are %s",
				provider, t, strings.Join(spec.types, ", "))
		}
		seen[t] = true
		result = append(result, t)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one --type is required (LOGS, METRICS or TRACES)")
	}
	return result, nil
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// readTelemetrySecret returns the secret value from exactly one of the inline
// value or the secret file ('-' reads standard input). A value starting with
// '{' is parsed as a JSON object; any other value is returned as a string.
func readTelemetrySecret(secret, secretFile string, stdin io.Reader) (interface{}, error) {
	if secret != "" && secretFile != "" {
		return nil, fmt.Errorf("only one of --secret or --secret-file can be provided")
	}
	if secret == "" && secretFile == "" {
		return nil, fmt.Errorf("either --secret-file or --secret must be provided")
	}

	raw := secret
	source := "--secret"
	if secretFile != "" {
		var data []byte
		var err error
		if secretFile == "-" {
			source = "standard input"
			data, err = io.ReadAll(stdin)
		} else {
			source = fmt.Sprintf("file '%s'", secretFile)
			data, err = os.ReadFile(secretFile)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read secret from %s: %w", source, err)
		}
		raw = trimTrailingLineEnding(string(data))
	} else {
		fmt.Fprintln(os.Stderr,
			"WARNING: --secret exposes the value in process listings (ps, /proc); prefer --secret-file")
	}

	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("secret from %s is empty", source)
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return raw, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("secret from %s starts with '{' but is not a valid JSON object: %w", source, err)
	}
	return obj, nil
}

// validateTelemetrySecret checks that the secret has the shape the collector
// reads for the provider.
func validateTelemetrySecret(provider string, value interface{}) error {
	spec := telemetryProviderSpecs[provider]
	if !spec.tlsSecret && len(spec.secretKeys) == 0 {
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("provider %s expects the secret to be a string token", provider)
		}
		return nil
	}

	obj, ok := value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("provider %s expects the secret to be a %s", provider, telemetrySecretDescription(spec))
	}
	present := map[string]bool{}
	for k, v := range obj {
		s, isString := v.(string)
		if !isString {
			return fmt.Errorf("secret key %q must be a string", k)
		}
		present[k] = strings.TrimSpace(s) != ""
	}

	if spec.tlsSecret {
		return validateTelemetryTLSSecret(provider, obj, present)
	}
	var missing []string
	for _, k := range spec.secretKeys {
		if !present[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("provider %s secret is missing %s", provider, quoteJoin(missing))
	}
	return nil
}

func validateTelemetryTLSSecret(provider string, obj map[string]interface{}, present map[string]bool) error {
	for k := range obj {
		if !containsString(telemetryTLSSecretKeys, k) {
			return fmt.Errorf("provider %s secret has unknown key %q: allowed keys are %s",
				provider, k, quoteJoin(telemetryTLSSecretKeys))
		}
	}
	if present["clientCert"] != present["clientKey"] {
		return fmt.Errorf(`provider %s secret must set "clientCert" and "clientKey" together`, provider)
	}
	if !present["clientCert"] && !present["caFile"] {
		return fmt.Errorf(`provider %s secret must set "clientCert" and "clientKey", "caFile", or both`, provider)
	}
	return nil
}

func buildTelemetryRequest() (*client.TelemetryRequest, error) {
	provider, err := normalizeTelemetryProvider(telemetryCreateFlags.provider)
	if err != nil {
		return nil, err
	}
	protocol, err := normalizeTelemetryProtocol(telemetryCreateFlags.protocol)
	if err != nil {
		return nil, err
	}
	types, err := normalizeTelemetryTypes(provider, telemetryCreateFlags.types)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(telemetryCreateFlags.name)
	if name == "" {
		return nil, fmt.Errorf("--name is required")
	}
	endpoint := strings.TrimSpace(telemetryCreateFlags.endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("--endpoint is required")
	}
	secret, err := readTelemetrySecret(telemetryCreateFlags.secret, telemetryCreateFlags.secretFile, telemetryStdin)
	if err != nil {
		return nil, err
	}
	if err := validateTelemetrySecret(provider, secret); err != nil {
		return nil, err
	}
	return &client.TelemetryRequest{
		Endpoint: endpoint,
		Protocol: protocol,
		Provider: provider,
		Types:    types,
		Secret:   client.SecretDto{Name: name, Value: secret},
	}, nil
}

func newTelemetryClient() (*client.Client, error) {
	config, err := client.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	c, err := client.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create NVCF client: %w", err)
	}
	return c, nil
}

func runTelemetryCreate(cmd *cobra.Command, args []string) error {
	req, err := buildTelemetryRequest()
	if err != nil {
		return err
	}
	c, err := newTelemetryClient()
	if err != nil {
		return err
	}
	defer c.Close()

	logging.Info("Creating telemetry endpoint '%s'...", req.Secret.Name)
	resp, err := c.CreateTelemetry(context.Background(), req)
	if err != nil {
		return fmt.Errorf("failed to create telemetry: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}
	logging.Success("Telemetry endpoint created successfully!")
	printTelemetryDetails(resp.Telemetry)
	return nil
}

func runTelemetryList(cmd *cobra.Command, args []string) error {
	c, err := newTelemetryClient()
	if err != nil {
		return err
	}
	defer c.Close()

	resp, err := c.ListTelemetries(context.Background())
	if err != nil {
		return fmt.Errorf("failed to list telemetries: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}
	if len(resp.Telemetries) == 0 {
		logging.Plain("No telemetry endpoints found.")
		return nil
	}
	logging.Plain("Found %d telemetry endpoint(s):", len(resp.Telemetries))
	for _, t := range resp.Telemetries {
		logging.Plain("")
		printTelemetryDetails(t)
	}
	return nil
}

func runTelemetryGet(cmd *cobra.Command, args []string) error {
	c, err := newTelemetryClient()
	if err != nil {
		return err
	}
	defer c.Close()

	resp, err := c.GetTelemetry(context.Background(), args[0])
	if err != nil {
		return fmt.Errorf("failed to get telemetry: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}
	printTelemetryDetails(resp.Telemetry)
	return nil
}

func runTelemetryDelete(cmd *cobra.Command, args []string) error {
	telemetryID := args[0]
	c, err := newTelemetryClient()
	if err != nil {
		return err
	}
	defer c.Close()

	ctx := context.Background()
	if !telemetryDeleteFlags.force {
		resp, err := c.GetTelemetry(ctx, telemetryID)
		if err != nil {
			return fmt.Errorf("failed to get telemetry for confirmation: %w", err)
		}
		logging.Warning("You are about to delete the following telemetry endpoint:")
		printTelemetryDetails(resp.Telemetry)
		logging.Plain("")
		logging.Plain("Type 'yes' to confirm deletion: ")

		var confirmation string
		if _, err := fmt.Fscanln(telemetryStdin, &confirmation); err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
			logging.Info("Delete cancelled.")
			return nil
		}
	}

	logging.Info("Deleting telemetry endpoint '%s'...", telemetryID)
	if err := c.DeleteTelemetry(ctx, telemetryID); err != nil {
		return fmt.Errorf("failed to delete telemetry: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(map[string]string{
			"status":      "deleted",
			"telemetryId": telemetryID,
		})
	}
	logging.Success("Telemetry endpoint deleted successfully!")
	return nil
}

func runTelemetryUpdateSecret(cmd *cobra.Command, args []string) error {
	telemetryID := args[0]
	secret, err := readTelemetrySecret(telemetrySecretFlags.secret, telemetrySecretFlags.secretFile, telemetryStdin)
	if err != nil {
		return err
	}
	c, err := newTelemetryClient()
	if err != nil {
		return err
	}
	defer c.Close()

	ctx := context.Background()
	resp, err := c.GetTelemetry(ctx, telemetryID)
	if err != nil {
		return fmt.Errorf("failed to get telemetry: %w", err)
	}
	telemetry := resp.Telemetry
	if _, known := telemetryProviderSpecs[telemetry.Provider]; known {
		if err := validateTelemetrySecret(telemetry.Provider, secret); err != nil {
			return err
		}
	}

	logging.Info("Updating secret for telemetry endpoint '%s'...", telemetryID)
	req := &client.UpdateTelemetrySecretRequest{
		Secret: client.SecretDto{Name: telemetry.Name, Value: secret},
	}
	if err := c.UpdateTelemetrySecrets(ctx, telemetryID, req); err != nil {
		return fmt.Errorf("failed to update telemetry secret: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(map[string]string{
			"status":      "ok",
			"telemetryId": telemetryID,
		})
	}
	logging.Success("Telemetry secret updated successfully!")
	return nil
}

func printTelemetryDetails(t client.TelemetryDto) {
	logging.Plain("ID: %s", t.TelemetryID)
	logging.Plain("Name: %s", t.Name)
	logging.Plain("Provider: %s", t.Provider)
	logging.Plain("Protocol: %s", t.Protocol)
	logging.Plain("Endpoint: %s", t.Endpoint)
	logging.Plain("Types: %s", strings.Join(t.Types, ", "))
	if t.CreatedAt != "" {
		logging.Plain("Created At: %s", formatTimestamp(t.CreatedAt))
	}
}

// attachedTelemetryLines renders the telemetry endpoints attached to a
// function or task, one "Type: ID" line per configured signal.
func attachedTelemetryLines(t *client.TelemetriesDto) []string {
	if t == nil {
		return nil
	}
	var lines []string
	if t.LogsTelemetryId != "" {
		lines = append(lines, "Logs: "+t.LogsTelemetryId)
	}
	if t.MetricsTelemetryId != "" {
		lines = append(lines, "Metrics: "+t.MetricsTelemetryId)
	}
	if t.TracesTelemetryId != "" {
		lines = append(lines, "Traces: "+t.TracesTelemetryId)
	}
	return lines
}
