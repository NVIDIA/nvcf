// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dangerous direction is a bump that lands on the wrong line. A chart often
// names several images, and only the one whose tag matches appVersion belongs
// to the service that just released. The multi-image fixture below is the case
// that would expose a substitution that is merely "close enough".

type fixture struct{ root string }

func newFixture(t *testing.T, metadata string) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(f.root, "tools", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.metadata(t, metadata)
	return f
}

func (f *fixture) metadata(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, MetadataPath), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) chart(t *testing.T, id, appVersion, values string) {
	t.Helper()
	dir := filepath.Join(f.root, "deploy", "helm", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chart := fmt.Sprintf("apiVersion: v2\nname: helm-%s\nversion: 0.0.0\nappVersion: %q\n", id, appVersion)
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(chart), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(values+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) run(t *testing.T, tag string, write bool) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err := Run(f.root, tag, write, &out, &errOut)
	if err != nil {
		// A returned error is the failure path; surface it on stderr the way the
		// command does so tests can assert on the message.
		fmt.Fprintln(&errOut, err)
	}
	return code, out.String(), errOut.String()
}

func (f *fixture) read(t *testing.T, id, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, "deploy", "helm", id, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const meta = `{"services":[
 {"id":"svc","path":"src/svc"},
 {"id":"other","path":"src/other"},
 {"id":"agree","path":"deploy/helm/agree","deploys":["svc"]},
 {"id":"drift","path":"deploy/helm/drift","deploys":["svc"]},
 {"id":"notag","path":"deploy/helm/notag","deploys":["svc"]},
 {"id":"floater","path":"deploy/helm/floater","deploys":["svc"]},
 {"id":"multi","path":"deploy/helm/multi","deploys":["svc"]},
 {"id":"unrelated","path":"deploy/helm/unrelated","deploys":["other"]}
]}`

func full(t *testing.T) *fixture {
	f := newFixture(t, meta)
	f.chart(t, "agree", "1.0.0", "image:\n  tag: \"1.0.0\"")
	f.chart(t, "drift", "1.0.0", "image:\n  tag: \"9.9.9\"")
	f.chart(t, "notag", "1.0.0", "image:\n  repository: \"\"")
	f.chart(t, "floater", "1.0.0", "image:\n  tag: \"latest\"")
	// Two images. Only the one matching appVersion belongs to this service.
	f.chart(t, "multi", "1.0.0", "app:\n  image:\n    tag: \"1.0.0\"\nsidecar:\n  image:\n    tag: \"3.3.3\"")
	f.chart(t, "unrelated", "1.0.0", "image:\n  tag: \"1.0.0\"")
	return f
}

func TestDriftedChartRefusesWithTheRefusalCode(t *testing.T) {
	// Exit 3, not any non-zero: a caller has to tell a refusal (charts that
	// could move did) from a failure (nothing moved), and cannot do it by
	// looking at stderr because the failure path writes there too. The unowned
	// tag test below pins the other side of that distinction.
	code, _, errOut := full(t).run(t, "src/svc/v2.0.0", false)
	if code != RefusedExit {
		t.Fatalf("want exit %d, got %d", RefusedExit, code)
	}
	if !strings.Contains(errOut, "does not match image tag") {
		t.Fatalf("refusal must name the mismatch:\n%s", errOut)
	}
}

func TestFloatingTagRefusesForBeingFloating(t *testing.T) {
	// Asserted on the reason, not on the word "floating": name the fixture
	// chart "floating" and a substring check passes on the chart id even with
	// the floating check removed entirely. Mutation testing caught exactly that
	// in the first version of this suite, which is why the chart is "floater".
	_, _, errOut := full(t).run(t, "src/svc/v2.0.0", false)
	if !strings.Contains(errOut, "image tag is floating") {
		t.Fatalf("a floating tag must be refused for being floating:\n%s", errOut)
	}
}

func TestAgreeingChartIsPlanned(t *testing.T) {
	_, out, _ := full(t).run(t, "src/svc/v2.0.0", false)
	if !strings.Contains(out, "agree: 1.0.0 -> 2.0.0 (appVersion and image tag agree)") {
		t.Fatalf("agreeing chart should plan a bump:\n%s", out)
	}
}

func TestChartWithNoTagMovesAppVersionOnly(t *testing.T) {
	_, out, _ := full(t).run(t, "src/svc/v2.0.0", false)
	if !strings.Contains(out, "notag: 1.0.0 -> 2.0.0 (no image tag set)") {
		t.Fatalf("a chart with no tag should move appVersion alone:\n%s", out)
	}
}

func TestUnrelatedChartIsNotTouched(t *testing.T) {
	// It declares a different service. Reaching it would mean the deploys edge
	// is not actually gating anything.
	_, out, errOut := full(t).run(t, "src/svc/v2.0.0", false)
	if strings.Contains(out, "unrelated") || strings.Contains(errOut, "unrelated") {
		t.Fatalf("a chart deploying another service must not be considered:\n%s%s", out, errOut)
	}
}

func TestUnownedTagIsAFailureDistinctFromARefusal(t *testing.T) {
	code, _, errOut := full(t).run(t, "src/nosuch/v1.0.0", false)
	if code != 1 {
		t.Fatalf("an unowned tag must fail with 1, not %d", code)
	}
	if code == RefusedExit {
		t.Fatal("a failure must not share the refusal exit code")
	}
	if !strings.Contains(errOut, "no service in release metadata owns the tag") {
		t.Fatalf("failure should name the problem:\n%s", errOut)
	}
}

func TestServiceWithNoChartIsNotAnError(t *testing.T) {
	f := newFixture(t, `{"services":[{"id":"lonely","path":"src/lonely"}]}`)
	code, out, _ := f.run(t, "src/lonely/v1.0.0", false)
	if code != 0 {
		t.Fatalf("a service shipping no chart is not an error, got %d", code)
	}
	if !strings.Contains(out, "nothing to do") {
		t.Fatalf("it should say so:\n%s", out)
	}
}

func TestLongestPathWinsWhenTwoServicePathsBothMatch(t *testing.T) {
	// Both paths below genuinely match the tag, which is what makes the tie-break
	// reachable: "src/a/v1/v2.0.0" starts with "src/a" + "/v" and with
	// "src/a/v1" + "/v". Picking the shorter one attributes the release to the
	// wrong service and reads the version as "1/v2.0.0".
	//
	// The obvious nesting case, "src/a" against "src/a/b", cannot exercise this:
	// a tag of "src/a/b/v2.0.0" does not start with "src/a/v", so only one
	// candidate ever exists and the tie-break is never consulted.
	f := newFixture(t, `{"services":[
	 {"id":"outer","path":"src/a"},
	 {"id":"inner","path":"src/a/v1"},
	 {"id":"c","path":"deploy/helm/c","deploys":["inner"]}
	]}`)
	f.chart(t, "c", "1.0.0", "image:\n  tag: \"1.0.0\"")
	_, out, _ := f.run(t, "src/a/v1/v2.0.0", false)
	if !strings.Contains(out, "service inner, version 2.0.0") {
		t.Fatalf("the longest matching path must win:\n%s", out)
	}
}

func TestChartReleaseTagIsNotAServiceRelease(t *testing.T) {
	// Chart entries are excluded when resolving a tag. Without that, a chart
	// release resolves to the chart itself as though it were a service, finds no
	// chart deploying it, and reports "nothing to do" with exit 0. A chart
	// release is stack-pin-resolver's business, and silently succeeding here
	// would hide that it reached the wrong tool.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"c","path":"deploy/helm/c","deploys":["svc"]}
	]}`)
	f.chart(t, "c", "1.0.0", "image:\n  tag: \"1.0.0\"")
	code, out, errOut := f.run(t, "deploy/helm/c/v1.2.0", false)
	if code != 1 {
		t.Fatalf("a chart release tag must not resolve to a service, got exit %d:\n%s", code, out)
	}
	if !strings.Contains(errOut, "no service in release metadata owns the tag") {
		t.Fatalf("it should say the tag owns no service:\n%s", errOut)
	}
}

func TestApplyRewritesOnlyTheTagLinesMatchingAppVersion(t *testing.T) {
	// Apply is exercised directly. PlanFor refuses a multi-image chart now, but
	// the line-scoped rewrite is still the mechanism that protects a sidecar if
	// a chart ever gains a way to name its service image, so it stays covered.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"multi","path":"deploy/helm/multi","deploys":["svc"]}
	]}`)
	f.chart(t, "multi", "1.0.0", "app:\n  image:\n    tag: \"1.0.0\"\nsidecar:\n  image:\n    tag: \"3.3.3\"")

	chart := Entry{ID: "multi", Path: "deploy/helm/multi", Deploys: []string{"svc"}}
	if err := Apply(f.root, chart, "2.0.0", Plan{Action: ActionBoth, Current: "1.0.0"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := f.read(t, "multi", "Chart.yaml"); !strings.Contains(got, `appVersion: "2.0.0"`) {
		t.Fatalf("appVersion did not move:\n%s", got)
	}
	values := f.read(t, "multi", "values.yaml")
	if !strings.Contains(values, `    tag: "2.0.0"`) {
		t.Fatalf("the matching image tag did not move:\n%s", values)
	}
	if !strings.Contains(values, `    tag: "3.3.3"`) {
		t.Fatalf("the sidecar tag must be left alone, it belongs to another image:\n%s", values)
	}
}

func TestMultipleImagesRefuseBecauseOwnershipIsUnknown(t *testing.T) {
	// A tag equal to appVersion is not evidence that it is the service's image.
	// This is the case that makes it not evidence: appVersion has fallen behind
	// the service image at 2.0.0 and instead matches an unrelated sidecar at
	// 1.0.0. Treating agreement as ownership moves the sidecar to the released
	// version and leaves the service image untouched, which is a wrong edit that
	// reads as a routine bump.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"drifted","path":"deploy/helm/drifted","deploys":["svc"]}
	]}`)
	f.chart(t, "drifted", "1.0.0",
		"service:\n  image:\n    tag: \"2.0.0\"\nsidecar:\n  image:\n    tag: \"1.0.0\"\nother:\n  image:\n    tag: \"latest\"")
	before := f.read(t, "drifted", "values.yaml")

	code, _, errOut := f.run(t, "src/svc/v3.0.0", true)
	if code != RefusedExit {
		t.Fatalf("want refusal, got exit %d", code)
	}
	if !strings.Contains(errOut, "cannot be identified") {
		t.Fatalf("the refusal should say ownership is unknown:\n%s", errOut)
	}
	if after := f.read(t, "drifted", "values.yaml"); after != before {
		t.Fatalf("nothing may be written:\n%s", after)
	}
	if got := f.read(t, "drifted", "Chart.yaml"); !strings.Contains(got, `appVersion: "1.0.0"`) {
		t.Fatalf("appVersion must not move either:\n%s", got)
	}
}

func TestSingleFloatingTagStillRefuses(t *testing.T) {
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"floater","path":"deploy/helm/floater","deploys":["svc"]}
	]}`)
	f.chart(t, "floater", "1.0.0", "image:\n  tag: \"latest\"")
	code, _, errOut := f.run(t, "src/svc/v2.0.0", true)
	if code != RefusedExit {
		t.Fatalf("want refusal, got %d", code)
	}
	if !strings.Contains(errOut, "image tag is floating") {
		t.Fatalf("want the floating reason:\n%s", errOut)
	}
}

func TestWritePreservesSurroundingContent(t *testing.T) {
	// Rewriting line by line rather than round-tripping YAML is deliberate; this
	// is the test that fails if someone swaps in a marshaller.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"c","path":"deploy/helm/c","deploys":["svc"]}
	]}`)
	f.chart(t, "c", "1.0.0", "# a comment that must survive\nimage:\n  tag: \"1.0.0\" # trailing note\n  pullPolicy: IfNotPresent")

	if code, _, errOut := f.run(t, "src/svc/v2.0.0", true); code != 0 {
		t.Fatalf("write should succeed, got %d: %s", code, errOut)
	}
	values := f.read(t, "c", "values.yaml")
	for _, want := range []string{"# a comment that must survive", "pullPolicy: IfNotPresent", `tag: "2.0.0" # trailing note`} {
		if !strings.Contains(values, want) {
			t.Fatalf("lost %q from values.yaml:\n%s", want, values)
		}
	}
}

func TestRerunningAnAppliedBumpIsANoOp(t *testing.T) {
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"c","path":"deploy/helm/c","deploys":["svc"]}
	]}`)
	f.chart(t, "c", "1.0.0", "image:\n  tag: \"1.0.0\"")
	f.run(t, "src/svc/v2.0.0", true)
	before := f.read(t, "c", "values.yaml")

	code, out, _ := f.run(t, "src/svc/v2.0.0", true)
	if code != 0 {
		t.Fatalf("a repeat run should be clean, got %d", code)
	}
	if !strings.Contains(out, "already 2.0.0") {
		t.Fatalf("it should say the chart is already there:\n%s", out)
	}
	if after := f.read(t, "c", "values.yaml"); after != before {
		t.Fatalf("a repeat run rewrote the file:\n%s\n---\n%s", before, after)
	}
}

func TestRefusalStillAppliesTheChartsThatCouldMove(t *testing.T) {
	// This is what the refusal exit code buys: partial progress must survive.
	// If a refusal aborted the run, the agreeing chart would never move.
	f := full(t)
	code, _, _ := f.run(t, "src/svc/v2.0.0", true)
	if code != RefusedExit {
		t.Fatalf("want refusal exit, got %d", code)
	}
	if got := f.read(t, "agree", "Chart.yaml"); !strings.Contains(got, `appVersion: "2.0.0"`) {
		t.Fatalf("the agreeing chart should still have moved:\n%s", got)
	}
	if got := f.read(t, "drift", "Chart.yaml"); !strings.Contains(got, `appVersion: "1.0.0"`) {
		t.Fatalf("the drifted chart must not have moved:\n%s", got)
	}
	if got := f.read(t, "floater", "values.yaml"); !strings.Contains(got, `tag: "latest"`) {
		t.Fatalf("the floating tag must not have been replaced:\n%s", got)
	}
}

func TestRealChartsResolve(t *testing.T) {
	// Against the checked-in metadata and charts, so a chart directory layout
	// this tool cannot read shows up here rather than during a release.
	root := repoRoot(t)
	m, err := LoadMetadata(root)
	if err != nil {
		t.Fatalf("checked-in release metadata does not load: %v", err)
	}
	seen := 0
	for _, e := range m.Services {
		if !strings.HasPrefix(e.Path, ChartPrefix) || e.Deploys == nil {
			continue
		}
		chartYAML, _ := ChartFiles(root, e.Path)
		if chartYAML == "" {
			t.Errorf("chart %s declares an edge but no Chart.yaml was found under %s", e.ID, e.Path)
			continue
		}
		if _, err := PlanFor(root, e, "9.9.9"); err != nil {
			t.Errorf("planning %s failed: %v", e.ID, err)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no chart with a declared edge was found; the metadata path or prefix is wrong")
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

func TestFloatingStillRefusesWhenNoTagAgrees(t *testing.T) {
	// The other side of the reorder: if nothing agrees with appVersion, one of
	// these tags is this service's image, and a floating one is not rewritable.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"floater","path":"deploy/helm/floater","deploys":["svc"]}
	]}`)
	f.chart(t, "floater", "1.0.0", "image:\n  tag: \"latest\"")
	code, _, errOut := f.run(t, "src/svc/v2.0.0", true)
	if code != RefusedExit {
		t.Fatalf("want refusal, got %d", code)
	}
	if !strings.Contains(errOut, "image tag is floating") {
		t.Fatalf("want the floating reason:\n%s", errOut)
	}
	if got := f.read(t, "floater", "values.yaml"); !strings.Contains(got, `tag: "latest"`) {
		t.Fatalf("nothing may be written:\n%s", got)
	}
}

func TestAMissingValuesFileLeavesChartYamlAlone(t *testing.T) {
	// Apply reads values.yaml before writing Chart.yaml. Writing first and then
	// failing the read leaves appVersion moved with the tag behind, which is the
	// drift state the next run refuses: a partial write that poisons the chart.
	f := newFixture(t, `{"services":[
	 {"id":"svc","path":"src/svc"},
	 {"id":"c","path":"deploy/helm/c","deploys":["svc"]}
	]}`)
	f.chart(t, "c", "1.0.0", "image:\n  tag: \"1.0.0\"")
	before := f.read(t, "c", "Chart.yaml")
	if err := os.Remove(filepath.Join(f.root, "deploy", "helm", "c", "values.yaml")); err != nil {
		t.Fatal(err)
	}

	// With values.yaml gone the chart plans appversion-only, so force the
	// both-file path directly to exercise the ordering.
	chart := Entry{ID: "c", Path: "deploy/helm/c", Deploys: []string{"svc"}}
	err := Apply(f.root, chart, "2.0.0", Plan{Action: ActionBoth, Current: "1.0.0"})
	if err == nil {
		t.Fatal("Apply should fail when values.yaml cannot be read")
	}
	if after := f.read(t, "c", "Chart.yaml"); after != before {
		t.Fatalf("Chart.yaml was written despite the failure:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
