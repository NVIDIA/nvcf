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

// Command gds-admin inspects and repairs a GDS store directly, without a
// running gds-server:
//
//	gds-admin [store flags] describe [--clusterset NAME] [--output text|json]
//	gds-admin [store flags] delete-cluster --clusterset NAME --cluster NAME --yes
//
// Store flags select the backend as gds-server does (--store,
// --dynamodb-table, --dynamodb-endpoint, --region), with the same GDS_
// environment fallbacks. Commands live in a table; a backend-specific
// command such as the DynamoDB store's create-table registers itself from
// its own file with registerCommand.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/storefactory"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, storefactory.Open, time.Now))
}

type opener func(context.Context, *config.Config) (store.Store, error)

// cmdEnv is what a command runs with.
type cmdEnv struct {
	cfg    *config.Config
	stdout io.Writer
	now    time.Time
	open   opener
	st     store.Store
}

// store opens the configured store once.
func (e *cmdEnv) store(ctx context.Context) (store.Store, error) {
	if e.st == nil {
		st, err := e.open(ctx, e.cfg)
		if err != nil {
			return nil, err
		}
		e.st = st
	}
	return e.st, nil
}

// command is one gds-admin subcommand.
type command struct {
	summary string
	run     func(ctx context.Context, e *cmdEnv, args []string) error
}

var commands = map[string]command{}

// registerCommand adds a subcommand.
func registerCommand(name string, c command) { commands[name] = c }

func init() {
	registerCommand("describe", command{
		summary: "list clustersets, or the records of one clusterset",
		run: func(ctx context.Context, e *cmdEnv, args []string) error {
			st, err := e.store(ctx)
			if err != nil {
				return err
			}
			return describe(ctx, st, args, e.stdout, e.now, e.cfg.LeaseSkew)
		},
	})
	registerCommand("delete-cluster", command{
		summary: "delete a cluster and everything it owns",
		run: func(ctx context.Context, e *cmdEnv, args []string) error {
			st, err := e.store(ctx)
			if err != nil {
				return err
			}
			return deleteCluster(ctx, st, args, e.stdout)
		},
	})
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, "Usage: gds-admin [store flags] <command> [command flags]\n\nCommands:\n")
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		fmt.Fprintf(w, "  %-15s %s\n", name, commands[name].summary)
	}
	fmt.Fprint(w, "\nStore flags:\n")
	fs.PrintDefaults()
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer, open opener, now func() time.Time) int {
	cfg := config.Default()
	fs := flag.NewFlagSet("gds-admin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr, fs) }
	fs.StringVar(&cfg.Store, "store", cfg.Store, "store backend: memory or dynamodb")
	fs.StringVar(&cfg.DynamoDBTable, "dynamodb-table", cfg.DynamoDBTable, "DynamoDB table name")
	fs.StringVar(&cfg.DynamoDBEndpoint, "dynamodb-endpoint", cfg.DynamoDBEndpoint, "DynamoDB endpoint override, for dynamodb-local")
	fs.StringVar(&cfg.Region, "region", cfg.Region, "AWS region; falls back to AWS_REGION")
	fs.DurationVar(&cfg.LeaseSkew, "lease-skew", cfg.LeaseSkew, "clock skew allowance when showing lease states")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := config.ApplyEnv(fs, lookupEnv); err != nil {
		fmt.Fprintf(stderr, "gds-admin: %v\n", err)
		return 2
	}
	if cfg.Region == "" {
		cfg.Region, _ = lookupEnv("AWS_REGION")
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	name := fs.Arg(0)
	cmd, ok := commands[name]
	if !ok {
		if name == "create-table" {
			fmt.Fprintln(stderr, "gds-admin: create-table is part of the DynamoDB store, which is not built into this binary yet")
			return 2
		}
		fmt.Fprintf(stderr, "gds-admin: unknown command %q\n", name)
		fs.Usage()
		return 2
	}

	e := &cmdEnv{cfg: cfg, stdout: stdout, now: now(), open: open}
	defer func() {
		if e.st != nil {
			_ = e.st.Close()
		}
	}()
	if err := cmd.run(ctx, e, fs.Args()[1:]); err != nil {
		fmt.Fprintf(stderr, "gds-admin %s: %v\n", name, err)
		var usageErr usageError
		if errors.As(err, &usageErr) {
			return 2
		}
		return 1
	}
	return 0
}

type usageError struct{ error }

// description is the JSON output of describe for one clusterset.
type description struct {
	ClusterSet   string           `json:"clusterset"`
	Clusters     []clusterRow     `json:"clusters"`
	Exports      []exportRow      `json:"exports"`
	EndpointSets []endpointSetRow `json:"endpointSets"`
	Pools        []poolRow        `json:"pools"`
	PoolAgents   []poolAgentRow   `json:"poolAgents"`
	Counts       map[string]int   `json:"counts"`
}

type clusterRow struct {
	Cluster    string    `json:"cluster"`
	Region     string    `json:"region"`
	LeaseState string    `json:"leaseState"`
	Epoch      uint64    `json:"epoch"`
	RenewedAt  time.Time `json:"renewedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Generation uint64    `json:"generation"`
	Agent      string    `json:"agent,omitempty"`
}

type exportRow struct {
	Cluster    string    `json:"cluster"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Role       string    `json:"role,omitempty"`
	Generation uint64    `json:"generation"`
	CreatedAt  time.Time `json:"exportCreationTimestamp"`
}

type endpointSetRow struct {
	Cluster    string `json:"cluster"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Endpoints  int    `json:"endpoints"`
	Ready      int    `json:"ready"`
	Generation uint64 `json:"generation"`
}

type poolRow struct {
	Cluster      string `json:"cluster"`
	PoolID       string `json:"poolId"`
	Name         string `json:"name"`
	DesiredState string `json:"desiredState,omitempty"`
	LeaseState   string `json:"leaseState,omitempty"`
	Generation   uint64 `json:"generation"`
}

// poolAgentRow counts the agent registrations (RegisterPoolAgent) of one
// role of one pool. A pool known only through its agents has no poolRow.
type poolAgentRow struct {
	Cluster       string `json:"cluster"`
	PoolID        string `json:"poolId"`
	Pool          string `json:"pool"`
	Role          string `json:"role"`
	Registrations int    `json:"registrations"`
	Active        int    `json:"active"`
}

func leaseState(l store.Lease, now time.Time, skew time.Duration) string {
	if l.Epoch == 0 {
		return ""
	}
	return string(registry.EvaluateLease(l.RenewedAt, l.TTL, now, skew))
}

func describeClusterSet(snap store.ClusterSetSnapshot, now time.Time, skew time.Duration) description {
	d := description{ClusterSet: snap.ClusterSet, Counts: map[string]int{}}
	for _, c := range snap.Clusters {
		row := clusterRow{
			Cluster: c.Key.Cluster, Region: c.Region, LeaseState: leaseState(c.Lease, now, skew), Epoch: c.Lease.Epoch,
			RenewedAt: c.Lease.RenewedAt, ExpiresAt: c.Lease.ExpiresAt(), Generation: c.Generation, Agent: c.AgentID,
		}
		d.Clusters = append(d.Clusters, row)
		if row.LeaseState == string(registry.LeaseStateActive) {
			d.Counts["activeClusters"]++
		}
	}
	for _, e := range snap.Exports {
		d.Exports = append(d.Exports, exportRow{
			Cluster: e.Key.Cluster, Namespace: e.Key.Namespace, Name: e.Key.Name, Type: string(e.Spec.Type),
			Role: e.Spec.Labels[api.LabelRole], Generation: e.Generation, CreatedAt: e.Spec.ExportCreationTimestamp,
		})
	}
	for _, s := range snap.EndpointSets {
		row := endpointSetRow{Cluster: s.Key.Cluster, Namespace: s.Key.Namespace, Name: s.Key.Name, Endpoints: len(s.Endpoints), Generation: s.Generation}
		for _, e := range s.Endpoints {
			if e.Conditions.Ready {
				row.Ready++
			}
		}
		d.EndpointSets = append(d.EndpointSets, row)
		d.Counts["endpoints"] += row.Endpoints
	}
	for _, p := range snap.Pools {
		d.Pools = append(d.Pools, poolRow{
			Cluster: p.Key.Cluster, PoolID: p.Key.PoolID, Name: p.Name, DesiredState: string(p.Spec.DesiredState),
			LeaseState: leaseState(p.Lease, now, skew), Generation: p.Generation,
		})
	}
	// The snapshot is sorted by key, so the registrations of one pool role
	// are adjacent.
	for _, a := range snap.PoolAgents {
		n := len(d.PoolAgents)
		if n == 0 || d.PoolAgents[n-1].Cluster != a.Key.Cluster || d.PoolAgents[n-1].PoolID != a.Key.PoolID ||
			d.PoolAgents[n-1].Role != string(a.Key.Role) {
			d.PoolAgents = append(d.PoolAgents, poolAgentRow{
				Cluster: a.Key.Cluster, PoolID: a.Key.PoolID, Pool: a.PoolName, Role: string(a.Key.Role),
			})
			n++
		}
		d.PoolAgents[n-1].Registrations++
		if leaseState(a.Lease, now, skew) == string(registry.LeaseStateActive) {
			d.PoolAgents[n-1].Active++
		}
	}
	d.Counts["clusters"] = len(d.Clusters)
	d.Counts["exports"] = len(d.Exports)
	d.Counts["endpointSets"] = len(d.EndpointSets)
	d.Counts["pools"] = len(d.Pools)
	d.Counts["poolAgentRegistrations"] = len(snap.PoolAgents)
	return d
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printText(w io.Writer, d description) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Clusterset %s: %d clusters (%d active), %d exports, %d endpoint sets (%d endpoints), %d pools, %d pool agent registrations\n\n",
		d.ClusterSet, d.Counts["clusters"], d.Counts["activeClusters"], d.Counts["exports"], d.Counts["endpointSets"],
		d.Counts["endpoints"], d.Counts["pools"], d.Counts["poolAgentRegistrations"])
	if len(d.Clusters) > 0 {
		fmt.Fprintln(tw, "CLUSTER\tREGION\tLEASE\tEPOCH\tRENEWED\tEXPIRES\tGENERATION")
		for _, c := range d.Clusters {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%d\n", c.Cluster, orDash(c.Region), c.LeaseState, c.Epoch,
				formatTime(c.RenewedAt), formatTime(c.ExpiresAt), c.Generation)
		}
		fmt.Fprintln(tw)
	}
	if len(d.Exports) > 0 {
		fmt.Fprintln(tw, "EXPORT\tCLUSTER\tTYPE\tROLE\tGENERATION\tCREATED")
		for _, e := range d.Exports {
			fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%d\t%s\n", e.Namespace, e.Name, e.Cluster, orDash(e.Type), orDash(e.Role),
				e.Generation, formatTime(e.CreatedAt))
		}
		fmt.Fprintln(tw)
	}
	if len(d.EndpointSets) > 0 {
		fmt.Fprintln(tw, "ENDPOINT SET\tCLUSTER\tENDPOINTS\tREADY\tGENERATION")
		for _, s := range d.EndpointSets {
			fmt.Fprintf(tw, "%s/%s\t%s\t%d\t%d\t%d\n", s.Namespace, s.Name, s.Cluster, s.Endpoints, s.Ready, s.Generation)
		}
		fmt.Fprintln(tw)
	}
	if len(d.Pools) > 0 {
		fmt.Fprintln(tw, "POOL\tID\tCLUSTER\tDESIRED\tLEASE\tGENERATION")
		for _, p := range d.Pools {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", p.Name, p.PoolID, p.Cluster, orDash(p.DesiredState), orDash(p.LeaseState), p.Generation)
		}
		if len(d.PoolAgents) > 0 {
			fmt.Fprintln(tw)
		}
	}
	if len(d.PoolAgents) > 0 {
		fmt.Fprintln(tw, "POOL AGENT\tPOOL ID\tCLUSTER\tREGISTRATIONS\tACTIVE")
		for _, a := range d.PoolAgents {
			fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%d\t%d\n", orDash(a.Pool), a.Role, a.PoolID, a.Cluster, a.Registrations, a.Active)
		}
	}
	_ = tw.Flush()
}

func describe(ctx context.Context, st store.Store, args []string, w io.Writer, now time.Time, skew time.Duration) error {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clusterSet := fs.String("clusterset", "", "clusterset to describe; all clustersets when empty")
	output := fs.String("output", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if *output != "text" && *output != "json" {
		return usageError{fmt.Errorf("--output %q: want text or json", *output)}
	}
	names := []string{*clusterSet}
	if *clusterSet == "" {
		var err error
		if names, err = st.ListClusterSets(ctx); err != nil {
			return fmt.Errorf("list clustersets: %w", err)
		}
	}
	var out []description
	for _, name := range names {
		snap, err := st.LoadClusterSet(ctx, name)
		if err != nil {
			return fmt.Errorf("load clusterset %q: %w", name, err)
		}
		snap.ClusterSet = name
		out = append(out, describeClusterSet(snap, now, skew))
	}
	if *output == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(out) == 0 {
		fmt.Fprintln(w, "No clustersets.")
		return nil
	}
	for i, d := range out {
		if i > 0 {
			fmt.Fprintln(w, strings.Repeat("-", 40))
		}
		printText(w, d)
	}
	return nil
}

func deleteCluster(ctx context.Context, st store.Store, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("delete-cluster", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clusterSet := fs.String("clusterset", "", "clusterset of the cluster")
	cluster := fs.String("cluster", "", "cluster to delete")
	yes := fs.Bool("yes", false, "confirm the deletion")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if !registry.IsDNSLabel(*clusterSet) || !registry.IsDNSLabel(*cluster) {
		return usageError{errors.New("--clusterset and --cluster must be DNS labels")}
	}
	snap, err := st.LoadClusterSet(ctx, *clusterSet)
	if err != nil {
		return fmt.Errorf("load clusterset %q: %w", *clusterSet, err)
	}
	var exports, sets, pools, agents int
	for _, e := range snap.Exports {
		if e.Key.Cluster == *cluster {
			exports++
		}
	}
	for _, s := range snap.EndpointSets {
		if s.Key.Cluster == *cluster {
			sets++
		}
	}
	for _, p := range snap.Pools {
		if p.Key.Cluster == *cluster {
			pools++
		}
	}
	for _, a := range snap.PoolAgents {
		if a.Key.Cluster == *cluster {
			agents++
		}
	}
	summary := fmt.Sprintf("cluster %s/%s: %d exports, %d endpoint sets, %d pools, %d pool agent registrations",
		*clusterSet, *cluster, exports, sets, pools, agents)
	if !*yes {
		return usageError{fmt.Errorf("would delete %s; pass --yes to delete", summary)}
	}
	if err := st.DeleteCluster(ctx, store.ClusterKey{ClusterSet: *clusterSet, Cluster: *cluster}); err != nil {
		return fmt.Errorf("delete cluster: %w", err)
	}
	fmt.Fprintf(w, "Deleted %s.\n", summary)
	return nil
}
