// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command chart-version-bumper moves a chart's version fields to a newly
// released service artifact version.
//
//	chart-version-bumper --tag src/control-plane-services/notary/v1.9.0 [--base origin/main] [--write]
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
// A pin only moves up. A release whose version is not above the pinned one is
// skipped, and with --base so is a release tagged on a commit the base branch
// does not contain: a backport cut on a release branch is that branch's
// release, and its version can sort below what the base branch already pins.
//
// Exit codes: 0 nothing to report, 3 at least one chart refused (the rest were
// still applied), anything else a failure that applied nothing.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
	base := flag.String("base", "", "skip the release unless its tagged commit is reachable from this ref")
	flag.Parse()

	if *tag == "" {
		fmt.Fprintln(os.Stderr, "error: --tag is required")
		flag.Usage()
		os.Exit(2)
	}

	code, err := Run(*root, *tag, *base, *write, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// Run resolves the tag, plans every chart that deploys the released service,
// and applies the ones that can move safely. A non-empty base skips a tag
// whose commit that ref does not contain.
func Run(root, tag, base string, write bool, out, errOut io.Writer) (int, error) {
	if base != "" {
		onBase, err := TagOnBase(root, tag, base)
		if err != nil {
			return 1, err
		}
		if !onBase {
			fmt.Fprintf(out, "%s: skipped, its commit is not on %s; a release cut on another branch moves no pin here\n", tag, base)
			return 0, nil
		}
	}

	meta, err := LoadMetadata(root)
	if err != nil {
		return 1, err
	}

	release, err := meta.ReleaseForTag(root, tag)
	if err != nil {
		return 1, err
	}
	charts := meta.ChartsDeploying(release.ServiceID)
	fmt.Fprintf(out, "%s -> service %s, version %s", tag, release.ServiceID, release.Version)
	if release.ArtifactVersion != release.Version {
		fmt.Fprintf(out, ", artifact %s", release.ArtifactVersion)
	}
	fmt.Fprintln(out)

	if len(charts) == 0 {
		// Not an error. Plenty of services ship no chart, and chart-service-edge
		// is what reports charts that have not declared an edge yet.
		fmt.Fprintf(out, "no chart declares that it deploys %s; nothing to do\n", release.ServiceID)
		return 0, nil
	}

	refused := 0
	level := ""
	for _, chart := range charts {
		var p Plan
		var err error
		if len(chart.ValuesPaths) > 0 || len(chart.ValuesFiles) > 0 {
			p, err = PlanForValuesPaths(root, chart.Entry, release.ArtifactVersion, chart.ValuesPaths, chart.ValuesFiles, chart.AppVersion)
		} else {
			p, err = PlanFor(root, chart.Entry, release.ArtifactVersion)
		}
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
			if p.Current == release.ArtifactVersion {
				fmt.Fprintf(out, "  %s: already %s\n", chart.ID, release.ArtifactVersion)
				continue
			}
			currentRelease, err := release.currentReleaseVersion(p.Current)
			if err != nil {
				fmt.Fprintf(errOut, "  %s: REFUSED, %s\n", chart.ID, err)
				refused++
				continue
			}
			// Never down, and never sideways to another spelling of the same
			// version. A version that does not parse falls through to the bump,
			// as it always has.
			if order, ok := CompareVersions(release.Version, currentRelease); ok && order <= 0 {
				which := "older"
				if order == 0 {
					which = "same-version"
				}
				fmt.Fprintf(out, "  %s: skipped %s release %s; keeping %s\n", chart.ID, which, release.ArtifactVersion, p.Current)
				continue
			}
			fmt.Fprintf(out, "  %s: %s -> %s (%s)\n", chart.ID, p.Current, release.ArtifactVersion, p.Detail)
			level = HigherLevel(level, BumpLevel(currentRelease, release.Version))
			if write {
				if len(chart.ValuesPaths) > 0 || len(chart.ValuesFiles) > 0 {
					err = ApplyValuesPaths(root, chart.Entry, release.ArtifactVersion, chart.ValuesPaths, chart.ValuesFiles, chart.AppVersion)
				} else {
					err = Apply(root, chart.Entry, release.ArtifactVersion, p)
				}
				if err != nil {
					return 1, err
				}
			}
		}
	}

	// A refusal is a real finding: the chart wanted a bump and could not take
	// one safely. Exiting non-zero puts it in front of a person.
	// One machine-readable line for the workflow: the largest semver step any
	// chart took. Absent when nothing moved or no version parsed.
	if level != "" {
		fmt.Fprintf(out, "bump: %s\n", level)
	}
	if refused > 0 {
		return RefusedExit, nil
	}
	return 0, nil
}

// TagOnBase reports whether the commit a release tag names is reachable from
// base. Exit 1 from git merge-base --is-ancestor means it is not; any other
// failure, such as a tag or ref that does not exist, is an error rather than
// a skip, so a missing tag cannot pass for a backport.
func TagOnBase(root, tag, base string) (bool, error) {
	// refs/tags/ in front, so a tag can never be read as a git option.
	cmd := exec.Command("git", "-C", root, "merge-base", "--is-ancestor", "refs/tags/"+tag+"^{commit}", base)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is on %s: %w: %s", tag, base, err, strings.TrimSpace(string(output)))
}
