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

const (
	eventLedgerFlagURL             = "event-ledger-url"
	eventLedgerFlagFunctionVersion = "function-version-id"
)

var (
	eventLedgerURLFlag             string
	eventLedgerFunctionVersionFlag string
	statsEventFilter               []string
	eventsInstanceID               string
	eventsDeploymentID             string
	eventsGPUSpecificationID       string
	eventsClusterID                string
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
reached at --event-ledger-url, EVENT_LEDGER_URL, or event_ledger_url; when none
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

The context filters must match how the events were ingested. Run 'stats get'
first to see the contexts that exist. Requires an API key with the
fnds:getEvents read scope.

Examples:
  nvcf-cli stages events list --function-version-id <uuid> --deployment-id <uuid> --gpu-specification-id gpu-a100`,
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
		c.Flags().StringVar(&eventLedgerURLFlag, eventLedgerFlagURL, "", "Event Ledger base URL (default: derived from base_http_url)")
		c.Flags().StringVar(&eventLedgerFunctionVersionFlag, eventLedgerFlagFunctionVersion, "", "function version ID (default: the saved version ID)")
	}
	statsGetCmd.Flags().StringSliceVar(&statsEventFilter, "event-filter", nil, "only include these event names (comma-separated)")
	stagesEventsListCmd.Flags().StringVar(&eventsInstanceID, "instance-id", "", "instance (pod) ID")
	stagesEventsListCmd.Flags().StringVar(&eventsDeploymentID, "deployment-id", "", "deployment ID")
	stagesEventsListCmd.Flags().StringVar(&eventsGPUSpecificationID, "gpu-specification-id", "", "GPU specification ID")
	stagesEventsListCmd.Flags().StringVar(&eventsClusterID, "cluster-id", "", "cluster ID")
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
	}
	if ec == (eventledger.EventsContext{}) {
		return fmt.Errorf("at least one of --instance-id, --deployment-id, --gpu-specification-id, or --cluster-id is required; run 'nvcf-cli stats get' to list contexts")
	}
	c, namespace, timeout, err := newEventLedgerClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	events, err := c.ListEvents(ctx, namespace, ec)
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
	config, err := client.LoadConfig()
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
	c, err := eventledger.NewClient(eventledger.Config{
		ServiceURL: resolveEventLedgerURL(eventLedgerURLFlag, config.BaseHTTPURL),
		HostHeader: viper.GetString("event_ledger_host"),
		APIKey:     eventLedgerAPIKey(config),
		Timeout:    config.DefaultTimeout,
	})
	if err != nil {
		return nil, "", 0, err
	}
	return c, namespace, config.DefaultTimeout, nil
}

// resolveEventLedgerURL picks the Event Ledger URL: flag, then
// EVENT_LEDGER_URL / event_ledger_url, then the "events." sibling of the API URL.
func resolveEventLedgerURL(flagValue, baseHTTPURL string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := viper.GetString("event_ledger_url"); v != "" {
		return v
	}
	if derived := deriveSiblingHTTPServiceURL(baseHTTPURL, eventLedgerHostService); derived != baseHTTPURL {
		return derived
	}
	return ""
}

// eventLedgerAPIKey prefers the dedicated Event Ledger key and falls back to the
// general API key.
func eventLedgerAPIKey(config *client.Config) string {
	if config.EventLedgerAPIKey != "" {
		return config.EventLedgerAPIKey
	}
	return config.APIKey
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Timestamp, e.EventName, e.Details.Severity, strings.TrimSpace(e.Details.Body))
	}
	_ = tw.Flush()
}
