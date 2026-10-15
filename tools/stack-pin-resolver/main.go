// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command stack-pin-resolver resolves which self-managed stack pins a released
// chart, and edits them.
//
//	stack-pin-resolver --audit
//	stack-pin-resolver --tag deploy/helm/nats/v0.8.0 [--write]
//
// --audit enumerates every release in the stack and reports the chart each one
// pins. It exits non-zero when any release cannot be resolved.
//
// --tag takes a chart release tag and reports the pins that should move. With
// --write it edits them in place.
//
// Why an audit mode exists at all: the failure this guards against is silence.
// A chart releases, nothing in the stack resolves to it, no pin moves, and the
// run reports success. A bumper that iterates only over what it understands
// reproduces that exactly. So resolution is enumerated over the whole stack and
// an unresolved release is an error, not a skipped iteration.
//
// Resolution has three steps, all from data already declared in the repository:
//
//	released tag   deploy/helm/<dir>/v<version>
//	-> chart path  deploy/helm/<dir>          (the tag prefix)
//	-> chart name  tools/ci/github-release-subprojects.json, the service_name of
//	               the entry whose path matches
//	-> stack pins  the helmfile releases naming that chart
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var tagRE = regexp.MustCompile(`^(deploy/helm/.+)/v(.+)$`)

// The version out of a tag is written verbatim into a shipped helmfile, so it
// is validated before it gets there rather than after. A tag is attacker
// influenceable by anyone who can push one, and `.+` would accept a value
// carrying spaces, quotes or a newline, which would corrupt the file or smuggle
// in an adjacent key.
var versionRE = regexp.MustCompile(`^[0-9][A-Za-z0-9._+-]*$`)

func main() {
	auditMode := flag.Bool("audit", false, "report the chart every stack release pins")
	tag := flag.String("tag", "", "chart release tag, for example deploy/helm/nats/v0.8.0")
	write := flag.Bool("write", false, "apply the changes rather than only reporting them")
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	var code int
	var err error
	switch {
	case *auditMode:
		code, err = Audit(*root, os.Stdout, os.Stderr)
	case *tag != "":
		code, err = Bump(*root, *tag, *write, os.Stdout, os.Stderr)
	default:
		flag.Usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// Audit enumerates the stack and reports what each release pins.
func Audit(root string, out, errOut io.Writer) (int, error) {
	releases, err := LoadStack(root)
	if err != nil {
		return 1, err
	}
	var bad []Release
	for _, r := range releases {
		state := "-> " + r.Chart
		if r.Unresolved != "" {
			state = r.Unresolved
			bad = append(bad, r)
		}
		version := r.Version
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(out, "%-28s %-20s %s\n", r.Name, version, state)
	}
	fmt.Fprintf(out, "\n%d releases, %d unresolved\n", len(releases), len(bad))

	if len(bad) > 0 {
		fmt.Fprintln(errOut, "\nUnresolved releases cannot receive an automated bump:")
		for _, r := range bad {
			fmt.Fprintf(errOut, "  %s (%s): %s\n", r.Name, r.File, r.Unresolved)
		}
		return 1, nil
	}
	return 0, nil
}

// Bump moves every stack pin that names the chart the tag released.
func Bump(root, tag string, write bool, out, errOut io.Writer) (int, error) {
	m := tagRE.FindStringSubmatch(tag)
	if m == nil {
		return 1, fmt.Errorf("not a chart release tag: %s", tag)
	}
	chartPath, version := m[1], m[2]
	if !versionRE.MatchString(version) {
		return 1, fmt.Errorf("tag %s carries a version that is not a plain version string: %q", tag, version)
	}

	chart, err := ChartNameForPath(root, chartPath)
	if err != nil {
		return 1, err
	}

	releases, err := LoadStack(root)
	if err != nil {
		return 1, err
	}

	var unresolved []Release
	for _, r := range releases {
		if r.Unresolved != "" {
			unresolved = append(unresolved, r)
		}
	}
	if len(unresolved) > 0 {
		// Refuse to act on a partially understood stack. A release that cannot
		// be read might be the one that pins this chart, and bumping the others
		// would look like success.
		fmt.Fprintln(errOut, "refusing to bump: the stack has unresolved releases")
		for _, r := range unresolved {
			fmt.Fprintf(errOut, "  %s (%s): %s\n", r.Name, r.File, r.Unresolved)
		}
		return 1, nil
	}

	var targets []Release
	for _, r := range releases {
		if r.Chart == chart {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		return 1, fmt.Errorf("no stack release pins %s (from %s)", chart, tag)
	}

	changed := 0
	for _, r := range targets {
		if r.Version == version {
			fmt.Fprintf(out, "%s: already %s\n", r.Name, version)
			continue
		}
		fmt.Fprintf(out, "%s: %s -> %s\n", r.Name, r.Version, version)
		changed++
		if write {
			if err := WritePin(root, r, version); err != nil {
				return 1, err
			}
		}
	}
	if changed == 0 {
		fmt.Fprintln(out, "nothing to change")
	}
	return 0, nil
}

// ChartNameForPath returns the published chart name for a chart directory.
func ChartNameForPath(root, chartPath string) (string, error) {
	meta, err := LoadMetadata(root)
	if err != nil {
		return "", err
	}
	for _, e := range meta.Services {
		if e.Path != chartPath {
			continue
		}
		if strings.TrimSpace(e.ServiceName) == "" {
			return "", fmt.Errorf("%s has no service_name in release metadata", chartPath)
		}
		return e.ServiceName, nil
	}
	return "", fmt.Errorf("no release-metadata entry with path %s", chartPath)
}
