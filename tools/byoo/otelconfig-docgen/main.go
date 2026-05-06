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

// docgen generates platform metrics documentation from source-config.yaml.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type sourceConfig struct {
	Global  []globalEntry   `yaml:"global"`
	Metrics []metricSection `yaml:"metrics"`
}

type globalEntry struct {
	Backend  string `yaml:"backend"`
	Workload string `yaml:"workload"`
	Metrics  struct {
		RequiredAttributes []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		} `yaml:"required_attributes"`
	} `yaml:"metrics"`
}

type metricSection struct {
	FunctionType string `yaml:"function_type"`
	Jobs         []struct {
		Name            string `yaml:"name"`
		MetricAllowList []struct {
			Catagory  string `yaml:"catagory"` //nolint:misspell // intentional typo matching YAML
			Docstring string `yaml:"docstring"`
			List      []struct {
				Name    string `yaml:"name"`
				Comment string `yaml:"comment"`
			} `yaml:"list"`
		} `yaml:"metric_allow_list"`
	} `yaml:"jobs"`
}

func main() {
	configPath := flag.String("c", "../../src/libraries/go/lib/pkg/otelconfig/backendconfig/source-config.yaml", "Path to source-config.yaml")
	outputPath := flag.String("o", "docs/platform-metrics.md", "Output markdown file path")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Error reading config: %v", err)
	}

	var cfg sourceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("Error parsing YAML: %v", err)
	}

	var md strings.Builder
	md.WriteString("# Platform Metrics\n\n")

	// Global section
	if len(cfg.Global) > 0 {
		md.WriteString("## Telemetry Attributes\n\n")
		md.WriteString("All traces, logs and metrics have the following attributes added to their metadata:\n")
		md.WriteString("| Backend | Workload | Required Attributes |\n")
		md.WriteString("| --- | --- | --- |\n")
		for _, g := range cfg.Global {
			var attrs []string
			for _, a := range g.Metrics.RequiredAttributes {
				attrs = append(attrs, a.Name)
			}
			fmt.Fprintf(&md, "| %s | %s | %s |\n", g.Backend, g.Workload, strings.Join(attrs, ", "))
		}
		md.WriteString("\n")
	}

	// Metrics section
	if len(cfg.Metrics) > 0 {
		md.WriteString("## Metrics Details\n\n")
		for _, section := range cfg.Metrics {
			fmt.Fprintf(&md, "## %s\n", section.FunctionType)
			for _, job := range section.Jobs {
				fmt.Fprintf(&md, "### %s\n", job.Name)
				for _, cat := range job.MetricAllowList {
					if cat.Catagory != "" && strings.ToLower(cat.Catagory) != "generic" { //nolint:misspell // intentional
						fmt.Fprintf(&md, "##### %s\n", cat.Catagory) //nolint:misspell // intentional
					}
					if cat.Docstring != "" {
						md.WriteString(cat.Docstring + "\n")
					}
					for _, metric := range cat.List {
						if metric.Comment != "" {
							fmt.Fprintf(&md, "* %s (%s)\n", metric.Name, metric.Comment)
						} else {
							fmt.Fprintf(&md, "* %s\n", metric.Name)
						}
					}
					md.WriteString("\n")
				}
			}
			md.WriteString("\n")
		}
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0755); err != nil {
		log.Fatalf("Error creating output directory: %v", err)
	}
	if err := os.WriteFile(*outputPath, []byte(md.String()), 0644); err != nil {
		log.Fatalf("Error writing output: %v", err)
	}
	fmt.Printf("Documentation written to %s\n", *outputPath)
}
