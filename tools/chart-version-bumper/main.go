// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command chart-version-bumper moves a chart's version fields to a newly
// released service version.
//
//	chart-version-bumper --tag src/control-plane-services/notary/v1.9.0 [--write]
//
// This is the first hop of the service to chart to stack cascade. A service
// releases, the charts that deploy it move their appVersion and image tag, and
// the chart release that follows is what stack-pin-resolver then acts on.
//
// Which charts deploy the service comes from the deploys list in
// tools/ci/github-release-subprojects.json; see chart-service-edge.
//
// Which field to move is the awkward part, because a chart states its version
// twice. Chart.yaml has appVersion at a fixed location. values.yaml has an image
// tag at a path that differs per chart, and the two have drifted apart in real
// charts: api-keys-colocated reads 0.0.4 against a tag of 1.5.0, ratelimiter
// 1.0.0 against 1.15.2.
//
// Rather than declare one of them authoritative and silently overwrite the
// other, this uses their agreement as the evidence:
//
//	they agree      both move together. The current value identifies exactly
//	                which tag lines belong to this service, so no per-chart
//	                path configuration is needed.
//	no tag is set   appVersion moves alone. The chart resolves its image from
//	                Chart.AppVersion or the operator supplies a tag.
//	they differ     nothing moves, and the chart is reported. Someone chose
//	                that split, or it is a bug; either way it is not something
//	                to resolve by fiat during an automated bump.
//	the tag floats  nothing moves. A tag of latest is not a pin, and replacing
//	                it with a version would be a behaviour change rather than
//	                a bump.
//
// The refusals are the point. A bumper that guesses which of two disagreeing
// fields to move will eventually move the wrong one, and the result looks like
// a routine version bump in review.
//
// Exit codes: 0 nothing to report, 3 at least one chart refused (the rest were
// still applied), anything else a failure that applied nothing.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// RefusedExit is returned when at least one chart refused the bump.
//
// Its own code, because a caller has to tell a refusal from a failure and
// cannot do it by looking at stderr: an unresolvable tag writes there too, so
// it would read exactly like a refused chart. A refusal means the charts that
// could move did move; a failure means nothing did. 3 rather than 2 because
// flag parsing already exits 2 on a usage error.
const RefusedExit = 3

func main() {
	tag := flag.String("tag", "", "service release tag, for example src/control-plane-services/notary/v1.9.0")
	write := flag.Bool("write", false, "apply the changes rather than only reporting them")
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	if *tag == "" {
		fmt.Fprintln(os.Stderr, "error: --tag is required")
		flag.Usage()
		os.Exit(2)
	}

	code, err := Run(*root, *tag, *write, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// Run resolves the tag, plans every chart that deploys the released service,
// and applies the ones that can move safely.
func Run(root, tag string, write bool, out, errOut io.Writer) (int, error) {
	meta, err := LoadMetadata(root)
	if err != nil {
		return 1, err
	}

	serviceID, version, err := meta.ServiceForTag(tag)
	if err != nil {
		return 1, err
	}
	charts := meta.ChartsDeploying(serviceID)
	fmt.Fprintf(out, "%s -> service %s, version %s\n", tag, serviceID, version)

	if len(charts) == 0 {
		// Not an error. Plenty of services ship no chart, and chart-service-edge
		// is what reports charts that have not declared an edge yet.
		fmt.Fprintf(out, "no chart declares that it deploys %s; nothing to do\n", serviceID)
		return 0, nil
	}

	refused := 0
	for _, chart := range charts {
		p, err := PlanFor(root, chart, version)
		if err != nil {
			return 1, err
		}
		switch p.Action {
		case ActionRefuse:
			fmt.Fprintf(errOut, "  %s: REFUSED, %s\n", chart.ID, p.Detail)
			refused++
		case ActionSkip:
			fmt.Fprintf(out, "  %s: skipped, %s\n", chart.ID, p.Detail)
		default:
			if p.Current == version {
				fmt.Fprintf(out, "  %s: already %s\n", chart.ID, version)
				continue
			}
			fmt.Fprintf(out, "  %s: %s -> %s (%s)\n", chart.ID, p.Current, version, p.Detail)
			if write {
				if err := Apply(root, chart, version, p); err != nil {
					return 1, err
				}
			}
		}
	}

	// A refusal is a real finding: the chart wanted a bump and could not take
	// one safely. Exiting non-zero puts it in front of a person.
	if refused > 0 {
		return RefusedExit, nil
	}
	return 0, nil
}
