// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The distinction this whole tool rests on is absent versus empty. A chart that
// has not declared its edge is outstanding work; one that declares an empty
// list has stated it ships no first-party image. Every test below exists to
// keep those two apart, or to keep a dangling id from passing as declared.

func decode(t *testing.T, body string) *Metadata {
	t.Helper()
	var m Metadata
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return &m
}

func audit(t *testing.T, body string, strict bool) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Audit(decode(t, body), strict, &out, &errOut)
	return code, out.String(), errOut.String()
}

const fixture = `{"services":[
 {"id":"svc",       "path":"src/svc"},
 {"id":"other",     "path":"src/other"},
 {"id":"declared",  "path":"deploy/helm/declared",  "deploys":["svc"]},
 {"id":"noimage",   "path":"deploy/helm/noimage",   "deploys":[]},
 {"id":"gap",       "path":"deploy/helm/gap"}
]}`

func TestUndeclaredIsNotCountedAsDeclared(t *testing.T) {
	code, out, _ := audit(t, fixture, false)
	if code != 0 {
		t.Fatalf("report mode must not fail, got %d", code)
	}
	// Asserted on the counts, not on the word "undeclared" appearing somewhere:
	// the report uses that word itself, so a substring check would pass with the
	// behaviour removed entirely.
	if !strings.Contains(out, "3 charts: 2 declared, 1 undeclared, 0 naming an unknown service") {
		t.Fatalf("counts wrong:\n%s", out)
	}
	if !strings.Contains(out, "gap                        -> undeclared") {
		t.Fatalf("the undeclared chart must appear as undeclared:\n%s", out)
	}
}

func TestEmptyDeploysIsADeclarationNotAGap(t *testing.T) {
	// The case a len()-only test would get wrong: an empty list is a decision,
	// so strict mode must not fail on it.
	code, out, _ := audit(t, fixture, true)
	if !strings.Contains(out, "noimage") || !strings.Contains(out, "(no first-party image)") {
		t.Fatalf("empty deploys should report as a deliberate declaration:\n%s", out)
	}
	// strict still fails, but for the undeclared chart, not this one.
	if code != 1 {
		t.Fatalf("strict must fail while a chart is undeclared, got %d", code)
	}
}

func TestStrictFailsOnUndeclaredAndNamesIt(t *testing.T) {
	code, _, errOut := audit(t, fixture, true)
	if code != 1 {
		t.Fatalf("strict must fail on an undeclared chart, got %d", code)
	}
	if !strings.Contains(errOut, "gap (deploy/helm/gap)") {
		t.Fatalf("strict failure must name the chart and its path:\n%s", errOut)
	}
}

func TestStrictPassesOnceEveryChartDeclares(t *testing.T) {
	body := `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"a","path":"deploy/helm/a","deploys":["svc"]},
	 {"id":"b","path":"deploy/helm/b","deploys":[]}
	]}`
	if code, _, _ := audit(t, body, true); code != 0 {
		t.Fatalf("strict must pass when nothing is undeclared, got %d", code)
	}
}

func TestUnknownServiceIDFailsEvenInReportMode(t *testing.T) {
	// A dangling id means a service was renamed and the edge left pointing at
	// nothing. That is an error whether or not strict is on, because unlike an
	// undeclared chart it is not outstanding work anyone planned.
	body := `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"stale","path":"deploy/helm/stale","deploys":["renamed-away"]}
	]}`
	code, out, errOut := audit(t, body, false)
	if code != 1 {
		t.Fatalf("an unknown service id must fail even in report mode, got %d", code)
	}
	if !strings.Contains(out, "UNKNOWN SERVICE ID") {
		t.Fatalf("report should flag the unknown id:\n%s", out)
	}
	if !strings.Contains(errOut, "renamed-away") {
		t.Fatalf("stderr should name the missing id:\n%s", errOut)
	}
}

func TestAChartMayNotPointAtAnotherChart(t *testing.T) {
	// Chart entries are excluded from the service id set, so naming one is a
	// dangling edge. Without that exclusion a chart could satisfy the audit by
	// pointing at a sibling chart, which moves no image.
	body := `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"target","path":"deploy/helm/target","deploys":[]},
	 {"id":"pointer","path":"deploy/helm/pointer","deploys":["target"]}
	]}`
	if code, _, _ := audit(t, body, false); code != 1 {
		t.Fatalf("pointing at a chart must not count as a declared edge, got %d", code)
	}
}

func TestNullDeploysReadsAsUndeclared(t *testing.T) {
	// An explicit null is not a declaration of "no image": nobody writing null
	// means that, and treating it as one would silently exempt the chart from
	// strict mode forever.
	body := `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"nulled","path":"deploy/helm/nulled","deploys":null}
	]}`
	code, out, _ := audit(t, body, true)
	if code != 1 {
		t.Fatalf("null deploys must be treated as undeclared, got %d", code)
	}
	if !strings.Contains(out, "-> undeclared") {
		t.Fatalf("null deploys should report as undeclared:\n%s", out)
	}
}

func TestServicesAreNotAudited(t *testing.T) {
	// Only chart entries carry the edge. A service with no deploys key is not a
	// gap, and counting it as one would make strict mode unreachable.
	body := `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"a","path":"deploy/helm/a","deploys":["svc"]}
	]}`
	code, out, _ := audit(t, body, true)
	if code != 0 {
		t.Fatalf("services must not be audited as charts, got %d", code)
	}
	if !strings.Contains(out, "1 charts") {
		t.Fatalf("only the chart should be counted:\n%s", out)
	}
}

func TestReportListsEveryChartExactlyOnce(t *testing.T) {
	// The report is how someone finds the work; a chart silently missing from
	// it reads as "nothing to do".
	_, out, _ := audit(t, fixture, false)
	for _, id := range []string{"declared", "noimage", "gap"} {
		if n := strings.Count(out, "  "+id+" "); n != 1 {
			t.Fatalf("chart %s appears %d times in the report, want 1:\n%s", id, n, out)
		}
	}
}

func TestRealMetadataParsesAndAudits(t *testing.T) {
	// The checked-in metadata must stay loadable and free of dangling ids. This
	// is the case that catches a service renamed without updating its edges.
	root := repoRoot(t)
	meta, err := LoadMetadata(filepath.Join(root, MetadataPath))
	if err != nil {
		t.Fatalf("checked-in release metadata does not load: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := Audit(meta, false, &out, &errOut); code != 0 {
		t.Fatalf("checked-in metadata has a dangling chart to service edge:\n%s", errOut.String())
	}
	if len(meta.Charts()) == 0 {
		t.Fatal("no charts found in the checked-in metadata; the path or prefix is wrong")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, MetadataPath)); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not find %s above the test directory", MetadataPath)
	return ""
}
