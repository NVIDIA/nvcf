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
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/eventledger"
	"nvcf-cli/internal/logging"
)

const eventLedgerFlagFunctionVersion = "function-version-id"
const eventLedgerHostService = "events"

var functionVersionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	eventLedgerFunctionVersionFlag string
	statsEventFilter               []string
	eventsInstanceID               string
	eventsDeploymentID             string
	eventsGPUSpecificationID       string
	eventsClusterID                string
	eventsResourceID               string
	eventsAttributeKey             string
	eventsAttributeValue           string
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show Event Ledger statistics for a function version",
}

var statsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the latest event for every instance of a function version",
	Long: `Show the latest Event Ledger event for every instance (context) of a function version.

Requires an API key with the fnds:getEvents read scope; generate one with
"api-key generate --for event-ledger". The Event Ledger is
reached at EVENT_LEDGER_URL or event_ledger_url; when none
is set the URL is derived from base_http_url by replacing the leading "api."
with "events.".

Examples:
  nvcf-cli stats get --function-version-id <uuid>
  nvcf-cli stats get --function-version-id <uuid> --event-filter Started,Failed`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runStatsGet,
}

var stagesCmd = &cobra.Command{
	Use:   "stages",
	Short: "Inspect function deployment stages recorded by the Event Ledger",
}

var stagesEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Event Ledger event history",
}

var stagesEventsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the event history of one instance of a function version",
	Long: `List the Event Ledger event history for one context of a function version, most recent first.

The context filters are optional and must match how the events were ingested. With
no filter, the command returns the events stored without a context. Run 'stats get'
first to see the contexts that exist. Requires an API key with the
fnds:getEvents read scope.

Examples:
  nvcf-cli stages events list --function-version-id <uuid>
  nvcf-cli stages events list --function-version-id <uuid> --deployment-id <uuid> --gpu-specification-id gpu-a100
  nvcf-cli stages events list --function-version-id <uuid> --resource-id <id> --attribute-key <key> --attribute-value <value>`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runStagesEventsList,
}

func init() {
	rootCmd.AddCommand(statsCmd)
	statsCmd.AddCommand(statsGetCmd)
	rootCmd.AddCommand(stagesCmd)
	stagesCmd.AddCommand(stagesEventsCmd)
	stagesEventsCmd.AddCommand(stagesEventsListCmd)

	for _, c := range []*cobra.Command{statsGetCmd, stagesEventsListCmd} {
		c.Flags().StringVar(&eventLedgerFunctionVersionFlag, eventLedgerFlagFunctionVersion, "", "function version ID (default: the saved version ID)")
	}
	statsGetCmd.Flags().StringSliceVar(&statsEventFilter, "event-filter", nil, "only include these event names (comma-separated)")
	stagesEventsListCmd.Flags().StringVar(&eventsInstanceID, "instance-id", "", "instance (pod) ID")
	stagesEventsListCmd.Flags().StringVar(&eventsDeploymentID, "deployment-id", "", "deployment ID")
	stagesEventsListCmd.Flags().StringVar(&eventsGPUSpecificationID, "gpu-specification-id", "", "GPU specification ID")
	stagesEventsListCmd.Flags().StringVar(&eventsClusterID, "cluster-id", "", "cluster ID")
	stagesEventsListCmd.Flags().StringVar(&eventsResourceID, "resource-id", "", "resource ID (for example an ICMS request)")
	stagesEventsListCmd.Flags().StringVar(&eventsAttributeKey, "attribute-key", "", "only events whose attribute with this key equals --attribute-value")
	stagesEventsListCmd.Flags().StringVar(&eventsAttributeValue, "attribute-value", "", "attribute value to match; requires --attribute-key")
}

func runStatsGet(cmd *cobra.Command, _ []string) error {
	c, namespace, timeout, err := newEventLedgerClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stats, err := c.GetStats(ctx, namespace, eventledger.StatsOptions{EventFilter: statsEventFilter})
	if err != nil {
		return fmt.Errorf("failed to get event stats: %w", err)
	}
	if jsonOutput {
		return OutputJSON(stats)
	}
	renderStats(cmd.OutOrStdout(), stats)
	return nil
}

func runStagesEventsList(cmd *cobra.Command, _ []string) error {
	ec := eventledger.EventsContext{
		InstanceID:       eventsInstanceID,
		DeploymentID:     eventsDeploymentID,
		GPUSpecification: eventsGPUSpecificationID,
		ClusterID:        eventsClusterID,
		ResourceID:       eventsResourceID,
	}
	if (eventsAttributeKey == "") != (eventsAttributeValue == "") {
		return fmt.Errorf("--attribute-key and --attribute-value must be used together")
	}
	c, namespace, timeout, err := newEventLedgerClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	events, err := c.ListEvents(ctx, namespace, ec, eventledger.EventsAttribute{Key: eventsAttributeKey, Value: eventsAttributeValue})
	if err != nil {
		return fmt.Errorf("failed to list events: %w", err)
	}
	if jsonOutput {
		return OutputJSON(events)
	}
	renderEvents(cmd.OutOrStdout(), events)
	return nil
}

func newEventLedgerClient() (*eventledger.Client, string, time.Duration, error) {
	config, err := client.LoadConfigWithoutAuth()
	if err != nil {
		return nil, "", 0, fmt.Errorf(errFailedToLoadConfig, err)
	}
	namespace := eventLedgerFunctionVersionFlag
	if namespace == "" {
		if err := LoadStateForCurrentCommand(); err != nil {
			logging.Warning("Could not load state: %v", err)
		}
		namespace = GetCurrentState().VersionID
		if namespace != "" {
			logging.Info("Using saved version ID: %s", namespace)
		}
	}
	if namespace == "" {
		return nil, "", 0, fmt.Errorf("--%s is required", eventLedgerFlagFunctionVersion)
	}
	if !functionVersionIDPattern.MatchString(namespace) {
		return nil, "", 0, fmt.Errorf("invalid function version ID %q: must be a UUID", namespace)
	}
	if config.EventLedgerAPIKey == "" {
		return nil, "", 0, fmt.Errorf("no Event Ledger API key configured, run 'nvcf-cli api-key generate --for event-ledger'")
	}
	c, err := eventledger.NewClient(eventledger.Config{
		ServiceURL: resolveEventLedgerURL(config.BaseHTTPURL),
		HostHeader: viper.GetString("event_ledger_host"),
		APIKey:     config.EventLedgerAPIKey,
		Timeout:    config.DefaultTimeout,
	})
	if err != nil {
		return nil, "", 0, err
	}
	return c, namespace, config.DefaultTimeout, nil
}

// resolveEventLedgerURL picks the Event Ledger URL: EVENT_LEDGER_URL /
// event_ledger_url, then the "events." sibling of the API URL.
func resolveEventLedgerURL(baseHTTPURL string) string {
	if v := viper.GetString("event_ledger_url"); v != "" {
		return v
	}
	if derived := deriveSiblingHTTPServiceURL(baseHTTPURL, eventLedgerHostService); derived != baseHTTPURL {
		return derived
	}
	return ""
}

func renderStats(w io.Writer, stats *eventledger.StatsResponse) {
	fmt.Fprintf(w, "Function version: %s\n", stats.Namespace)
	fmt.Fprintf(w, "Contexts: %d\n", stats.Summary.TotalContexts)
	names := make([]string, 0, len(stats.Summary.ByEvent))
	for name := range stats.Summary.ByEvent {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %s: %d\n", name, stats.Summary.ByEvent[name])
	}
	if len(stats.Contexts) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CONTEXT\tEVENT\tTIMESTAMP\tFIRST SEEN")
	for _, c := range stats.Contexts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Context, c.EventName, c.Timestamp, c.FirstSeen)
	}
	_ = tw.Flush()
}

func renderEvents(w io.Writer, events *eventledger.EventsResponse) {
	fmt.Fprintf(w, "Function version: %s\n", events.Namespace)
	fmt.Fprintf(w, "Context: %s\n", events.Context)
	if len(events.Events) == 0 {
		fmt.Fprintln(w, "No events found.")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIMESTAMP\tEVENT\tSEVERITY\tMESSAGE")
	for _, e := range events.Events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Timestamp, e.EventName, e.Details.Severity, formatEventBody(e.Details.Body))
	}
	_ = tw.Flush()
}

// formatEventBody renders a string or structured event body for the text table.
func formatEventBody(body any) string {
	s, ok := body.(string)
	if ok {
		return strings.TrimSpace(s)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("%v", body)
	}
	return string(b)
}
