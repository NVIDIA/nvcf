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
