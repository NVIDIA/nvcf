// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"strings"
)

// Class is what a migration does to data that already exists.
type Class int

const (
	// Additive migrations only add schema. A cluster that skipped every stack
	// version between two points still arrives at the right schema, because
	// golang-migrate applies the ordered set regardless of how far behind the
	// cluster was.
	Additive Class = iota
	// Destructive migrations remove a table, type, or column, or delete rows.
	// Skipping versions is still safe for the schema itself, but the drop is
	// unrecoverable without a restore, and it constrains deployment order:
	// the migration must land before the services that read what it removes.
	Destructive
)

func (c Class) String() string {
	if c == Destructive {
		return "Destructive"
	}
	return "Additive"
}

var destructive = regexp.MustCompile(`(?i)\b(DROP|TRUNCATE|DELETE)\b`)

// Classify reports what a CQL migration does. Comments are stripped first:
// migrations/cassandra/keyspaces/nvcf_api/03_init_tables.up.sql documents a
// "high-churn write/delete workload" above a CREATE TABLE, and a classifier
// that reads raw text calls that destructive.
func Classify(sql string) Class {
	if destructive.MatchString(stripComments(sql)) {
		return Destructive
	}
	return Additive
}

func stripComments(sql string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
