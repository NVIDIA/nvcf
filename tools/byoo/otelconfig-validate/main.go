/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	wrapperType := flag.String("wrapper-type", "", "Wrapper type: function or task (required)")
	workloadType := flag.String("workload-type", "", "Workload type: container or helm (required)")
	cloudProvider := flag.String("cloud-provider", "", "Cloud provider: gfn or non-gfn (required)")
	configPath := flag.String("config", "../../src/libraries/go/lib/validator/otelconfig/validator-config.yaml", "Path to the configuration file")
	startTime := flag.String("start", "", "Start time in RFC3339 format (default: 24 hours ago)")
	endTime := flag.String("end", "", "End time in RFC3339 format (default: now)")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warning, error, critical")
	extraFilters := flag.String("extra-promql-filters", "", "Additional PromQL filters")
	golden := flag.Bool("golden", false, "Enable golden metrics comparison")

	flag.Parse()

	// Positional argument: id.
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: positional argument 'id' is required")
		flag.Usage()
		os.Exit(1)
	}
	id := flag.Arg(0)

	// Validate required flags.
	if *wrapperType == "" {
		log.Fatalf("--wrapper-type is required (function or task)")
	}
	if *wrapperType != string(WrapperTypeFunction) && *wrapperType != string(WrapperTypeTask) {
		log.Fatalf("--wrapper-type must be 'function' or 'task', got %q", *wrapperType)
	}

	if *workloadType == "" {
		log.Fatalf("--workload-type is required (container or helm)")
	}
	if *workloadType != string(WorkloadTypeContainer) && *workloadType != string(WorkloadTypeHelm) {
		log.Fatalf("--workload-type must be 'container' or 'helm', got %q", *workloadType)
	}

	if *cloudProvider == "" {
		log.Fatalf("--cloud-provider is required (gfn or non-gfn)")
	}
	if *cloudProvider != string(CloudProviderGFN) && *cloudProvider != string(CloudProviderNonGFN) {
		log.Fatalf("--cloud-provider must be 'gfn' or 'non-gfn', got %q", *cloudProvider)
	}

	// Set log verbosity (only suppress debug output when not "debug").
	_ = *logLevel // placeholder — stdlib log doesn't have levels; debug logging
	// in the validator uses log.Printf which is always visible.

	// Default time range: last 24 hours.
	now := time.Now().UTC()
	if *startTime == "" {
		*startTime = now.Add(-24 * time.Hour).Format(time.RFC3339)
	}
	if *endTime == "" {
		*endTime = now.Format(time.RFC3339)
	}

	// Banner.
	log.Println("###########################################################")
	log.Printf(" Wrapper Type        : %s", *wrapperType)
	log.Printf(" Workload Type       : %s", *workloadType)
	log.Printf(" Cloud Provider      : %s", *cloudProvider)
	log.Printf(" ID                  : %s", id)
	log.Printf(" Start               : %s", *startTime)
	log.Printf(" End                 : %s", *endTime)
	log.Printf(" Extra PromQL Filters: %s", *extraFilters)
	log.Println("###########################################################")
	log.Println()

	validator, err := NewMetricsValidator(*configPath)
	if err != nil {
		log.Fatalf("Failed to create validator: %v", err)
	}

	results, err := validator.Validate(
		WrapperType(*wrapperType),
		WorkloadType(*workloadType),
		CloudProvider(*cloudProvider),
		id,
		*startTime,
		*endTime,
		*extraFilters,
		*golden,
	)
	if err != nil {
		log.Fatalf("Validation failed: %v", err)
	}

	// Print results.
	log.Println("###########################################################")
	log.Println(" Validation Result:")
	hasInvalid := false
	for job, status := range results {
		log.Printf("  - %-30s: %s", job, colorResult(status))
		if status == ResultInvalid {
			hasInvalid = true
		}
	}
	log.Println("###########################################################")
	log.Println()

	if hasInvalid {
		os.Exit(1)
	}
}
