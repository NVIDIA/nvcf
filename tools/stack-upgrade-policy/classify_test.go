// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestClassifyAddColumnIsAdditive(t *testing.T) {
	sql := "ALTER TABLE nvcf_api.functions_v3 ADD llm_config frozen<llm_config_udt>;"
	if got := Classify(sql); got != Additive {
		t.Fatalf("Classify() = %v, want Additive", got)
	}
}

func TestClassifyDropTableIsDestructive(t *testing.T) {
	sql := "DROP TABLE IF EXISTS nvcf_autoscaler.recently_invoked_functions_history;"
	if got := Classify(sql); got != Destructive {
		t.Fatalf("Classify() = %v, want Destructive", got)
	}
}

func TestClassifyAlterTableDropIsDestructive(t *testing.T) {
	sql := "ALTER TABLE IF EXISTS nvcf_api.functions_deployment_v2 DROP IF EXISTS gpu_specs;"
	if got := Classify(sql); got != Destructive {
		t.Fatalf("Classify() = %v, want Destructive", got)
	}
}

func TestClassifyDropTypeIsDestructive(t *testing.T) {
	sql := "DROP TYPE IF EXISTS nvct_api.health_udt;"
	if got := Classify(sql); got != Destructive {
		t.Fatalf("Classify() = %v, want Destructive", got)
	}
}

// migrations/cassandra/keyspaces/nvcf_api/03_init_tables.up.sql carries the
// comment "Tuned for high-churn write/delete workload: UCS + short gc_grace."
// A classifier that greps the raw text calls that table creation destructive.
func TestClassifyIgnoresKeywordsInComments(t *testing.T) {
	sql := `-- Tuned for high-churn write/delete workload: UCS + short gc_grace.
-- We may DROP this table later.
CREATE TABLE IF NOT EXISTS nvcf_api.functions_v3 (id uuid PRIMARY KEY);`
	if got := Classify(sql); got != Additive {
		t.Fatalf("Classify() = %v, want Additive", got)
	}
}

// CodeRabbit on #1983: stripComments only handled `--`, so a block comment
// containing DROP made an additive migration look destructive and would have
// failed a legitimate non-major release.
func TestClassifyIgnoresKeywordsInBlockComments(t *testing.T) {
	sql := `/* DROP TABLE old_data; superseded, see NVCF-1234 */
CREATE TABLE IF NOT EXISTS nvcf_api.new_data (id uuid PRIMARY KEY);`
	if got := Classify(sql); got != Additive {
		t.Fatalf("Classify() = %v, want Additive", got)
	}
}

// CQL carries table options as string literals, and a table whose comment
// mentions dropping rows is not a destructive migration.
func TestClassifyIgnoresKeywordsInStringLiterals(t *testing.T) {
	sql := "CREATE TABLE nvcf_api.t (id uuid PRIMARY KEY) WITH comment = 'we never delete or drop here';"
	if got := Classify(sql); got != Additive {
		t.Fatalf("Classify() = %v, want Additive", got)
	}
}

// The doubled-quote escape must not leave the scanner stuck inside a string,
// or every statement after one would be skipped and a real DROP missed.
func TestClassifyHandlesEscapedQuotesAndStillSeesRealDrops(t *testing.T) {
	sql := `CREATE TABLE nvcf_api.t (id uuid PRIMARY KEY) WITH comment = 'it''s fine';
DROP TABLE IF EXISTS nvcf_api.old;`
	if got := Classify(sql); got != Destructive {
		t.Fatalf("Classify() = %v, want Destructive", got)
	}
}
