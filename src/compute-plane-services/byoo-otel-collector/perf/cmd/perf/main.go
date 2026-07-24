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

// Command perf is the entrypoint for the BYOO collector performance test
// suite. In this first milestone only "render" is fully wired; "run" and
// "cleanup" are scaffolding for the deployment/load milestones (S4+).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/profile"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/validate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "cleanup":
		err = cmdCleanup(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `perf - BYOO OpenTelemetry collector performance test suite

Usage:
  perf <command> [flags]

Commands:
  render    Render the production workload shape via icms-translate and validate it (no cluster required).
  run       Deploy, drive load, and measure (not yet implemented; see S4+).
  cleanup   Remove test resources (not yet implemented; see S5+).

Run "perf <command> -h" for command flags.
`)
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	shapeFlag := fs.String("shape", "both", `deployment shape: "container", "helm", or "both"`)
	profileFlag := fs.String("profile", "dev", `execution profile: "dev" or "baseline"`)
	collectorImage := fs.String("collector-image", spec.DefaultCollectorImage, "BYOO collector image reference")
	namespace := fs.String("namespace", "byoo-perf", "namespace for rendered objects")
	output := fs.String("output", "summary", `output format: "summary", "yaml", or "json"`)
	if err := fs.Parse(args); err != nil {
		return err
	}

	prof, err := profile.Lookup(*profileFlag)
	if err != nil {
		return err
	}
	shapes, err := shapesFromFlag(*shapeFlag)
	if err != nil {
		return err
	}

	opts := spec.DefaultOptions()
	opts.Namespace = *namespace
	opts.CollectorImage = *collectorImage

	exp := validate.Expectations{
		Image:     opts.CollectorImage,
		Resources: common.GetDefaultContainerResourcesBYOO(),
	}

	fmt.Printf("profile=%s warmup=%s window=%s reps=%d\n\n", prof.Name, prof.Warmup, prof.MeasurementWindow, prof.Repetitions)

	for _, shape := range shapes {
		res, err := render.Render(shape, opts)
		if err != nil {
			return fmt.Errorf("render %s: %w", shape, err)
		}
		if err := validate.Render(res, exp); err != nil {
			return err
		}

		switch *output {
		case "summary":
			printSummary(res)
		case "yaml", "json":
			if err := printObjects(res, *namespace, *output); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown output %q (want \"summary\", \"yaml\", or \"json\")", *output)
		}
	}
	return nil
}

func printSummary(res *render.Result) {
	fmt.Printf("[%s] VALID\n", res.Shape)
	fmt.Printf("  collector image : %s\n", res.Collector.Image)
	fmt.Printf("  config version  : %s\n", res.OTelVersion)
	fmt.Printf("  owner pod       : %s\n", res.OwnerPod)
	if res.Service != nil {
		fmt.Printf("  otlp service    : %s\n", res.Service.Name)
	}
	fmt.Printf("  ports           : %s\n", portSummary(res))
	fmt.Printf("  objects         : %d translated\n\n", len(res.Objects))
}

func portSummary(res *render.Result) string {
	parts := make([]string, 0, len(res.Collector.Ports))
	for _, p := range res.Collector.Ports {
		parts = append(parts, fmt.Sprintf("%s:%d", p.Name, p.ContainerPort))
	}
	return strings.Join(parts, " ")
}

func printObjects(res *render.Result, namespace, format string) error {
	pod := res.BenchPod(namespace)
	out, err := yaml.Marshal(pod)
	if err != nil {
		return fmt.Errorf("marshal bench pod: %w", err)
	}
	if format == "json" {
		out, err = yaml.YAMLToJSON(out)
		if err != nil {
			return fmt.Errorf("convert to json: %w", err)
		}
	}
	fmt.Printf("# shape=%s benchmark workload (authentic collector + emptyDir stand-ins)\n", res.Shape)
	fmt.Printf("%s\n", out)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	_ = fs.String("shape", "both", `deployment shape: "container", "helm", or "both"`)
	_ = fs.String("profile", "dev", `execution profile: "dev" or "baseline"`)
	_ = fs.String("mode", "k3d", `deployment mode: "k3d" or "remote"`)
	_ = fs.Bool("retain", false, "retain test resources for debugging instead of cleaning up")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return fmt.Errorf("`run` is not implemented yet; it lands with the deployment/load milestones (S4-S9)")
}

func cmdCleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	_ = fs.String("mode", "k3d", `deployment mode: "k3d" or "remote"`)
	_ = fs.String("namespace", "byoo-perf", "namespace to clean up")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return fmt.Errorf("`cleanup` is not implemented yet; it lands with the deployment milestone (S5)")
}

func shapesFromFlag(s string) ([]spec.Shape, error) {
	switch s {
	case "container":
		return []spec.Shape{spec.ShapeContainer}, nil
	case "helm":
		return []spec.Shape{spec.ShapeHelm}, nil
	case "both":
		return []spec.Shape{spec.ShapeContainer, spec.ShapeHelm}, nil
	default:
		return nil, fmt.Errorf("unknown shape %q (want \"container\", \"helm\", or \"both\")", s)
	}
}
