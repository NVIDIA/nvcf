// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command github-rulesets plans, checks, and explicitly applies reviewed policy.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type github struct {
	endpoint string
	stderr   io.Writer
}

func (g github) request(endpoint, method string, payload Object, paginate bool, output any) error {
	args := []string{"api", "--hostname", "github.com", "--method", method, endpoint}
	if paginate {
		args = append(args, "--paginate", "--slurp")
	}
	var input []byte
	if payload != nil {
		var err error
		input, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		args = append(args, "--input", "-")
	}
	command := exec.Command("gh", args...)
	command.Stdin = bytes.NewReader(input)
	command.Stderr = g.stderr
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("gh api %s %s: %w", method, endpoint, err)
	}
	return json.Unmarshal(data, output)
}

// Snapshot reads only repository-owned rulesets named by the policy.
func (g github) Snapshot(policy Policy) ([]Object, error) {
	names := map[string]bool{}
	for _, desired := range policy.Rulesets {
		names[desired["name"].(string)] = true
	}
	var pages [][]Object
	if err := g.request(g.endpoint+"?includes_parents=false&per_page=100", "GET", nil, true, &pages); err != nil {
		return nil, err
	}
	selected := []Object{}
	for _, page := range pages {
		for _, summary := range page {
			name, _ := summary["name"].(string)
			if !names[name] {
				continue
			}
			id, ok := summary["id"].(float64)
			if !ok || id < 1 || id != float64(int64(id)) {
				return nil, fmt.Errorf("%s: invalid ruleset ID", name)
			}
			var ruleset Object
			if err := g.request(fmt.Sprintf("%s/%d", g.endpoint, int64(id)), "GET", nil, false, &ruleset); err != nil {
				return nil, err
			}
			selected = append(selected, ruleset)
		}
	}
	return selected, nil
}

// Write creates or updates exactly the ruleset identified by a reviewed change.
func (g github) Write(change Change) error {
	endpoint, method := g.endpoint, "POST"
	if change.ID != nil {
		endpoint, method = fmt.Sprintf("%s/%d", endpoint, *change.ID), "PUT"
	}
	var result Object
	return g.request(endpoint, method, change.After, false, &result)
}

func run(args []string, stdout, stderr io.Writer, newAPI func(string) RulesetAPI) int {
	flags := flag.NewFlagSet("github-rulesets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "policy JSON file")
	planPath := flags.String("plan", "", "reviewed plan JSON file, required for apply")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: github-rulesets --policy FILE [--plan FILE] plan|check|apply")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *policyPath == "" || flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	mode := flags.Arg(0)
	if mode != "plan" && mode != "check" && mode != "apply" {
		flags.Usage()
		return 2
	}
	err := execute(*policyPath, *planPath, mode, stdout, newAPI)
	if err != nil {
		if err == errDrift {
			return 1
		}
		fmt.Fprintf(stderr, "ruleset policy: %v\n", err)
		return 2
	}
	return 0
}

var errDrift = fmt.Errorf("ruleset policy differs from live settings")

func execute(policyPath, planPath, mode string, stdout io.Writer, newAPI func(string) RulesetAPI) error {
	if mode == "apply" && planPath == "" {
		return fmt.Errorf("apply requires --plan with a reviewed plan file")
	}
	if mode != "apply" && planPath != "" {
		return fmt.Errorf("--plan is only accepted with apply")
	}
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return err
	}
	policy, err := DecodePolicy(data)
	if err != nil {
		return err
	}
	api := newAPI(policy.Repository)
	if mode == "apply" {
		data, err := os.ReadFile(planPath)
		if err != nil {
			return err
		}
		var plan Plan
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("plan must contain exactly one JSON document")
		}
		if err := ApplyPlan(policy, plan, api); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Applied reviewed ruleset changes and verified readback.")
		return nil
	}
	live, err := api.Snapshot(policy)
	if err != nil {
		return err
	}
	plan, err := MakePlan(policy, live)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		return err
	}
	if mode == "check" && len(plan.Changes) != 0 {
		return errDrift
	}
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, func(repository string) RulesetAPI {
		return github{endpoint: "repos/" + repository + "/rulesets", stderr: os.Stderr}
	}))
}
