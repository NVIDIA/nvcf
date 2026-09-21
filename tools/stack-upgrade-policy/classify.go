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

// stripComments blanks out everything CQL does not execute: line comments,
// block comments, and single-quoted strings. Keyword matching runs on what is
// left, so a DROP mentioned in a comment or a table's comment option is not
// mistaken for one the database will perform.
//
// Stripped regions become a space rather than nothing, so that removing a
// comment between two tokens cannot weld them into a third.
func stripComments(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				i = len(sql)
			} else {
				i += end
			}
			b.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				i = len(sql)
			} else {
				i += 2 + end + 2
			}
			b.WriteByte(' ')
		case sql[i] == '\'':
			i++
			for i < len(sql) {
				if sql[i] != '\'' {
					i++
					continue
				}
				// '' is an escaped quote inside the string, not its end.
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i += 2
					continue
				}
				i++
				break
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}
