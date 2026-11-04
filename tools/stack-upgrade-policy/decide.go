// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
)

// Status is what happened to a migration file between two points in history.
type Status int

const (
	Added Status = iota
	Modified
	Deleted
)

func (s Status) String() string {
	switch s {
	case Added:
		return "added"
	case Modified:
		return "modified"
	default:
		return "deleted"
	}
}

// Change is one migration file and what happened to it.
type Change struct {
	Path   string
	Status Status
	Class  Class
}

// Evidence is every migration change a release candidate introduces since the
// stack's last published release.
type Evidence struct {
	Changes []Change
}

// Bump is the semver step a release candidate is proposing.
type Bump int

const (
	NoBump Bump = iota
	Patch
	Minor
	Major
)

func (b Bump) String() string {
	switch b {
	case Major:
		return "major"
	case Minor:
		return "minor"
	case Patch:
		return "patch"
	default:
		return "none"
	}
}

// Decision is the outcome for one release candidate.
type Decision struct {
	// Qualifying reports whether the candidate contains a change a customer
	// cannot safely skip past.
	Qualifying bool
	OK         bool
	Reason     string
}

// Decide compares migration evidence against the proposed version bump.
//
// Additive migrations never qualify. golang-migrate applies the ordered set
// per keyspace, so a cluster many versions behind still arrives at the right
// schema; nothing about skipping versions breaks it.
//
// A destructive migration or a deleted migration file does qualify. A drop
// cannot be undone without a restore, and a deleted migration is how a
// compatibility bridge stops shipping — once it is gone, a cluster arriving
// later has no way to run it. Both need the major boundary that tells a
// customer not to skip this release.
func Decide(ev Evidence, bump Bump) Decision {
	var qualifying []Change
	for _, c := range ev.Changes {
		if c.Status == Deleted || c.Class == Destructive {
			qualifying = append(qualifying, c)
		}
	}
	if len(qualifying) == 0 {
		return Decision{OK: true}
	}
	if bump == Major {
		return Decision{Qualifying: true, OK: true}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "proposed bump is %s, but this candidate contains %d change(s) a customer cannot skip past:\n", bump, len(qualifying))
	for _, c := range qualifying {
		if c.Status == Deleted {
			fmt.Fprintf(&b, "  %s (deleted: a cluster arriving later cannot run it)\n", c.Path)
			continue
		}
		fmt.Fprintf(&b, "  %s (%s, destructive)\n", c.Path, c.Status)
	}
	b.WriteString("\nEither cut this as a major release, or keep the removed migration in place until the next major boundary.")
	return Decision{Qualifying: true, Reason: b.String()}
}
