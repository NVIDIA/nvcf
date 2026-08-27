// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command chart-service-edge reports which charts declare the service they
// deploy.
//
//	chart-service-edge --audit            report, exit 0
//	chart-service-edge --audit --strict   report, exit 1 if any chart is undeclared
//
// A chart release moves a stack pin, and stack-pin-resolver handles that
// because a released tag names its own chart. The other direction does not work
// that way: when a service releases, nothing in the repository says which chart
// deploys it, so nothing can bump that chart's appVersion or image pin.
//
// That edge has to be declared, because it cannot be derived. Three attempts,
// all of which fail:
//
// Image repository. Charts deliberately leave registry and repository empty so
// an operator supplies them, so most charts name no image at all.
//
// Directory name. Only 12 of 22 chart directories share a name with a service,
// and two of those matches are wrong: deploy/helm/cassandra and
// deploy/helm/openbao match the migrations service rather than the service
// itself, because infra/cassandra and migrations/cassandra share a leaf name.
// A derivation that is wrong is worse than one that is missing.
//
// Chart name. The chart at deploy/helm/icms publishes as helm-nvcf-sis and
// carries the instance-cluster-management service. Directory, published name,
// and service are three different strings; the chart was never renamed after
// the service was.
//
// So a chart entry in tools/ci/github-release-subprojects.json may carry:
//
//	"deploys": ["<service id>", ...]
//
// listing the release-metadata ids of the services whose images it ships. A
// chart that ships no first-party image (an upstream dependency, or resources
// only) declares "deploys": [] to say so deliberately.
//
// This starts in report mode on purpose. Turning it strict before the
// declarations exist would fail every build and teach people to route around
// it. Land the declarations, then add --strict to CI.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	audit := flag.Bool("audit", false, "report the declared and undeclared chart to service edges")
	strict := flag.Bool("strict", false, "exit non-zero when a chart has not declared its edge")
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	if !*audit {
		flag.Usage()
		os.Exit(1)
	}
	code, err := run(*root, *strict, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(root string, strict bool, out, errOut io.Writer) (int, error) {
	meta, err := LoadMetadata(filepath.Join(root, MetadataPath))
	if err != nil {
		return 1, err
	}
	return Audit(meta, strict, out, errOut), nil
}

// Audit prints the edge report and returns the process exit code.
func Audit(meta *Metadata, strict bool, out, errOut io.Writer) int {
	charts := meta.Charts()
	serviceIDs := meta.ServiceIDs()

	var declared, undeclared []Entry
	type badEntry struct {
		chart   Entry
		unknown []string
	}
	var bad []badEntry

	sort.Slice(charts, func(i, j int) bool { return charts[i].ID < charts[j].ID })
	for _, c := range charts {
		// Absent and empty must stay distinguishable. An undeclared chart is
		// outstanding work; an empty list is a decision that the chart ships no
		// first-party image. Both look like "no service" if you only test
		// whether the list is empty.
		if c.Deploys == nil {
			undeclared = append(undeclared, c)
			continue
		}
		var unknown []string
		for _, s := range c.Deploys {
			if !serviceIDs[s] {
				unknown = append(unknown, s)
			}
		}
		if len(unknown) > 0 {
			bad = append(bad, badEntry{c, unknown})
		} else {
			declared = append(declared, c)
		}
	}

	for _, c := range declared {
		target := "(no first-party image)"
		if len(c.Deploys) > 0 {
			target = strings.Join(c.Deploys, ", ")
		}
		fmt.Fprintf(out, "  %-26s -> %s\n", c.ID, target)
	}
	for _, b := range bad {
		fmt.Fprintf(out, "  %-26s -> UNKNOWN SERVICE ID: %s\n", b.chart.ID, strings.Join(b.unknown, ", "))
	}
	for _, c := range undeclared {
		fmt.Fprintf(out, "  %-26s -> undeclared\n", c.ID)
	}

	fmt.Fprintf(out, "\n%d charts: %d declared, %d undeclared, %d naming an unknown service\n",
		len(charts), len(declared), len(undeclared), len(bad))

	// An id that does not resolve is always an error: it means a service was
	// renamed or removed and this edge was left pointing at nothing.
	if len(bad) > 0 {
		fmt.Fprintln(errOut, "\nThese charts name a service id that does not exist:")
		for _, b := range bad {
			fmt.Fprintf(errOut, "  %s: %s\n", b.chart.ID, strings.Join(b.unknown, ", "))
		}
		return 1
	}

	if len(undeclared) > 0 && strict {
		fmt.Fprintln(errOut, "\nThese charts do not declare the service they deploy, so a service release cannot reach them:")
		for _, c := range undeclared {
			fmt.Fprintf(errOut, "  %s (%s)\n", c.ID, c.Path)
		}
		return 1
	}

	return 0
}
