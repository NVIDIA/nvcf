// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command stack-upgrade-policy reports the schema migrations a stack release
// candidate introduces, and fails when they do not match the version bump the
// candidate is proposing.
//
//	stack-upgrade-policy --stack <id> --bump <conventional-commit type>
//
// The upgrade contract this enforces is that a customer installs each stack
// major version in order. That rule is derived from the version numbers
// themselves, so nothing has to publish a catalog or maintain a floor field
// per release. What the rule needs in exchange is that a major boundary is
// actually cut whenever the candidate contains something a customer cannot
// safely skip past.
//
// Deciding that from a stack-pin diff is not possible by eye: the diff is a
// list of chart versions moving, and nothing in "1.5.3 -> 2.0.0" says a
// migration landed. In a monorepo the evidence is computable instead, which
// also means it cannot be forgotten the way a declaration written in a pull
// request two months earlier can.
//
// Additive migrations never qualify. golang-migrate applies the ordered set
// per keyspace, so a cluster many versions behind still arrives at the right
// schema. Destructive migrations and deleted migration files do qualify: a
// drop is unrecoverable without a restore, and a deleted migration is how a
// compatibility bridge stops shipping, leaving a cluster that arrives later
// with no way to run it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	stack := flag.String("stack", "", "release-metadata id of the stack, for example nvcf-self-managed-stack")
	bump := flag.String("bump", "", "proposed bump as a conventional-commit type, as reported by tools/ci/release-bump-type")
	root := flag.String("root", ".", "repository root")
	asJSON := flag.Bool("json", false, "emit the evidence report as JSON")
	flag.Parse()

	if *stack == "" || *bump == "" {
		flag.Usage()
		os.Exit(2)
	}
	code, err := run(*root, *stack, *bump, *asJSON, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// ParseBump maps a conventional-commit type onto the semver step it causes.
// The input is what tools/ci/release-bump-type reports, so that this and
// semantic-release agree on what the candidate is proposing rather than
// deriving it twice from different places.
func ParseBump(commitType string) (Bump, error) {
	switch {
	case strings.HasSuffix(commitType, "!"):
		return Major, nil
	case commitType == "feat":
		return Minor, nil
	case commitType == "fix":
		return Patch, nil
	default:
		return NoBump, fmt.Errorf("unrecognized commit type %q: expected feat!, feat, or fix from release-bump-type", commitType)
	}
}

type reportChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Class  string `json:"class"`
}

type report struct {
	Stack      string         `json:"stack"`
	BaseTag    string         `json:"base_tag"`
	Bump       string         `json:"bump"`
	Qualifying bool           `json:"qualifying"`
	OK         bool           `json:"ok"`
	Checked    bool           `json:"checked"`
	Changes    []reportChange `json:"changes"`
}

func run(root, stackID, bumpType string, asJSON bool, out, errOut io.Writer) (int, error) {
	stack, err := LoadStack(root, stackID)
	if err != nil {
		return 1, err
	}
	bump, err := ParseBump(bumpType)
	if err != nil {
		return 1, err
	}
	if len(stack.MigrationPaths) == 0 {
		emit(out, asJSON, report{Stack: stack.ID, Bump: bump.String(), OK: true})
		return 0, nil
	}

	baseTag, err := LatestReleaseTag(root, stack.Path)
	if err != nil {
		return 1, err
	}
	ev, err := GatherEvidence(root, baseTag, stack.MigrationPaths)
	if err != nil {
		return 1, err
	}
	decision := Decide(ev, bump)

	rep := report{Stack: stack.ID, BaseTag: baseTag, Bump: bump.String(), Qualifying: decision.Qualifying, OK: decision.OK, Checked: true}
	for _, c := range ev.Changes {
		rc := reportChange{Path: c.Path, Status: c.Status.String(), Class: c.Class.String()}
		if c.Status == Deleted {
			rc.Class = ""
		}
		rep.Changes = append(rep.Changes, rc)
	}

	emit(out, asJSON, rep)
	if !decision.OK {
		fmt.Fprintln(errOut, decision.Reason)
		return 1, nil
	}
	return 0, nil
}

func emit(out io.Writer, asJSON bool, rep report) {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}
	writeText(out, rep)
}

func writeText(out io.Writer, rep report) {
	if !rep.Checked {
		fmt.Fprintf(out, "%s declares no migration paths; nothing to check.\n", rep.Stack)
		return
	}
	fmt.Fprintf(out, "%s: %d migration change(s) since %s (proposed bump: %s)\n",
		rep.Stack, len(rep.Changes), rep.BaseTag, rep.Bump)
	for _, c := range rep.Changes {
		if c.Class == "" {
			fmt.Fprintf(out, "  %-9s %s\n", c.Status, c.Path)
			continue
		}
		fmt.Fprintf(out, "  %-9s %-12s %s\n", c.Status, strings.ToLower(c.Class), c.Path)
	}
}
