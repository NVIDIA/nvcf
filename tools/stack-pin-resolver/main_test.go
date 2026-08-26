// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this tool exists to prevent is silence: a chart releases, nothing
// in the stack resolves to it, no pin moves, and the run goes green. So the
// tests below care most about unresolved releases being loud, and about a
// rewrite landing on exactly one line.

type stackFixture struct{ root string }

func newStack(t *testing.T, metadata string, files map[string]string) *stackFixture {
	t.Helper()
	f := &stackFixture{root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(f.root, "tools", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, MetadataPath), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.root, HelmfileDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *stackFixture) audit(t *testing.T) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err := Audit(f.root, &out, &errOut)
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return code, out.String(), errOut.String()
}

func (f *stackFixture) bump(t *testing.T, tag string, write bool) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, err := Bump(f.root, tag, write, &out, &errOut)
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return code, out.String(), errOut.String()
}

func (f *stackFixture) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, HelmfileDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const stackMeta = `{"services":[
 {"id":"alpha","path":"deploy/helm/alpha","service_name":"helm-nvcf-alpha"},
 {"id":"beta","path":"deploy/helm/beta","service_name":"helm-nvcf-beta"},
 {"id":"reval","path":"deploy/helm/reval","service_name":"helm-reval"},
 {"id":"router","path":"deploy/helm/router","service_name":"helm-nvcf-llm-request-router"},
 {"id":"orphan","path":"deploy/helm/orphan","service_name":"helm-nvcf-orphan"},
 {"id":"nameless","path":"deploy/helm/nameless"}
]}`

// alpha and beta are both pinned at 1.0.0 on purpose: a rewrite that is merely
// "close enough" moves both, and only a fixture where the two share a version
// exposes it.
const stackFile = `repositories:
  - name: nvcf
    url: oci://example.invalid/nvcf

releases:
  - name: alpha
    namespace: nvcf
    version: 1.0.0
  - name: beta
    namespace: nvcf
    version: 1.0.0
  - name: reval
    chart: nvcf/helm-reval
    version: 2.4.0
  - name: templated
    chart: nvcf/helm-nvcf-{{ .Release.Name }}
    version: 3.1.0
  - name: router
    chart: {{ $chartOverride | default "nvcf/helm-nvcf-llm-request-router" | quote }}
    version: 0.9.0
`

func TestRepositoriesBlockIsNotAPin(t *testing.T) {
	// The repositories entry matches the release shape but carries no version.
	// Counting it would put a bogus release in the audit and, worse, make the
	// stack look resolvable when it is not.
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	_, out, _ := f.audit(t)
	if strings.Contains(out, "nvcf ") && strings.Contains(out, "oci://") {
		t.Fatalf("the repositories block should not appear as a release:\n%s", out)
	}
	if !strings.Contains(out, "5 releases, 0 unresolved") {
		t.Fatalf("want exactly the five pinned releases:\n%s", out)
	}
}

func TestExplicitChartLineWins(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	_, out, _ := f.audit(t)
	if !strings.Contains(out, "-> helm-reval") {
		t.Fatalf("an explicit chart line should resolve to that chart:\n%s", out)
	}
}

func TestConventionAppliesWhenThereIsNoChartLine(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	_, out, _ := f.audit(t)
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "-> helm-nvcf-alpha") {
		t.Fatalf("a release with no chart line inherits helm-nvcf-<name>:\n%s", out)
	}
}

func TestReleaseNameTemplateResolvesByConvention(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	_, out, _ := f.audit(t)
	if !strings.Contains(out, "-> helm-nvcf-templated") {
		t.Fatalf("a .Release.Name template resolves to the release's own chart:\n%s", out)
	}
}

func TestOverrideWithDefaultResolvesToTheDefault(t *testing.T) {
	// The default is what ships unless an operator overrides it, so it is the
	// chart an automated bump should follow.
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	_, out, _ := f.audit(t)
	if !strings.Contains(out, "-> helm-nvcf-llm-request-router") {
		t.Fatalf("an override-with-default should resolve to the default:\n%s", out)
	}
}

func TestUnknownTemplateFormIsUnresolvedNotGuessed(t *testing.T) {
	body := `releases:
  - name: mystery
    chart: {{ include "something.else" . }}
    version: 1.0.0
`
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": body})
	code, out, errOut := f.audit(t)
	if code != 1 {
		t.Fatalf("an unreadable chart line must fail the audit, got %d", code)
	}
	if !strings.Contains(out, "1 releases, 1 unresolved") {
		t.Fatalf("it must be counted as unresolved:\n%s", out)
	}
	if !strings.Contains(errOut, "mystery") {
		t.Fatalf("the failure must name the release:\n%s", errOut)
	}
}

func TestBumpRefusesWhileAnythingIsUnresolved(t *testing.T) {
	// The release nobody can read might be the one pinning this chart. Bumping
	// the rest and reporting success is the silent failure this guards against.
	body := stackFile + `  - name: mystery
    chart: {{ include "something.else" . }}
    version: 1.0.0
`
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": body})
	code, _, errOut := f.bump(t, "deploy/helm/alpha/v2.0.0", true)
	if code != 1 {
		t.Fatalf("bump must refuse a partially understood stack, got %d", code)
	}
	if !strings.Contains(errOut, "refusing to bump") {
		t.Fatalf("it should say why:\n%s", errOut)
	}
	if got := f.read(t, "00-stack.yaml.gotmpl"); !strings.Contains(got, "- name: alpha\n    namespace: nvcf\n    version: 1.0.0") {
		t.Fatalf("nothing may be written when the stack is unresolved:\n%s", got)
	}
}

func TestBumpMovesOnlyTheMatchingRelease(t *testing.T) {
	// alpha and beta share the version 1.0.0. A rewrite keyed on the value
	// rather than on the release moves both.
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	code, out, errOut := f.bump(t, "deploy/helm/alpha/v2.0.0", true)
	if code != 0 {
		t.Fatalf("bump should succeed, got %d: %s", code, errOut)
	}
	if !strings.Contains(out, "alpha: 1.0.0 -> 2.0.0") {
		t.Fatalf("alpha should have moved:\n%s", out)
	}
	got := f.read(t, "00-stack.yaml.gotmpl")
	if !strings.Contains(got, "- name: alpha\n    namespace: nvcf\n    version: 2.0.0") {
		t.Fatalf("alpha's pin did not move:\n%s", got)
	}
	if !strings.Contains(got, "- name: beta\n    namespace: nvcf\n    version: 1.0.0") {
		t.Fatalf("beta shares alpha's old version and must not have moved:\n%s", got)
	}
}

func TestBumpLeavesEveryOtherLineByteIdentical(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	f.bump(t, "deploy/helm/alpha/v2.0.0", true)
	before := strings.Split(stackFile, "\n")
	after := strings.Split(f.read(t, "00-stack.yaml.gotmpl"), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed: %d -> %d", len(before), len(after))
	}
	diffs := 0
	for i := range before {
		if before[i] != after[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Fatalf("want exactly one changed line, got %d", diffs)
	}
}

func TestAlreadyPinnedIsANoOp(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	code, out, _ := f.bump(t, "deploy/helm/alpha/v1.0.0", true)
	if code != 0 {
		t.Fatalf("re-pinning the current version is not an error, got %d", code)
	}
	if !strings.Contains(out, "already 1.0.0") {
		t.Fatalf("it should say so:\n%s", out)
	}
	if f.read(t, "00-stack.yaml.gotmpl") != stackFile {
		t.Fatal("a no-op bump rewrote the file")
	}
}

func TestChartNobodyPinsIsAnError(t *testing.T) {
	// A chart released but pinned nowhere is exactly the nvcf-unbound shape:
	// the release happens, the stack never picks it up, and nothing says so.
	// orphan is declared in the metadata and appears in no helmfile release.
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	code, _, errOut := f.bump(t, "deploy/helm/orphan/v2.0.0", false)
	if code != 1 {
		t.Fatalf("a chart no stack release pins must fail, got %d", code)
	}
	if !strings.Contains(errOut, "no stack release pins helm-nvcf-orphan") {
		t.Fatalf("the error must name the chart that went unpinned:\n%s", errOut)
	}
}

func TestUnpinnedChartIsAnError(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	// nameless is in the metadata but has no service_name, so it cannot resolve.
	code, _, errOut := f.bump(t, "deploy/helm/nameless/v1.0.0", false)
	if code != 1 {
		t.Fatalf("a chart with no service_name must fail, got %d", code)
	}
	if !strings.Contains(errOut, "no service_name") {
		t.Fatalf("the error should say what is missing:\n%s", errOut)
	}
}

func TestUnknownChartPathIsAnError(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	code, _, errOut := f.bump(t, "deploy/helm/nosuch/v1.0.0", false)
	if code != 1 {
		t.Fatalf("an unknown chart path must fail, got %d", code)
	}
	if !strings.Contains(errOut, "no release-metadata entry") {
		t.Fatalf("the error should say what is missing:\n%s", errOut)
	}
}

func TestNonChartTagIsRejected(t *testing.T) {
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	code, _, errOut := f.bump(t, "src/control-plane-services/notary/v1.9.0", false)
	if code != 1 {
		t.Fatalf("a service release tag is not this tool's business, got %d", code)
	}
	if !strings.Contains(errOut, "not a chart release tag") {
		t.Fatalf("it should say so:\n%s", errOut)
	}
}

func TestRealStackResolvesCompletely(t *testing.T) {
	// The whole point: every release in the shipped stack must resolve. A new
	// release added in a form this cannot read fails here rather than silently
	// missing its bump later.
	root := repoRoot(t)
	var out, errOut bytes.Buffer
	code, err := Audit(root, &out, &errOut)
	if err != nil {
		t.Fatalf("auditing the checked-in stack failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("the checked-in stack has unresolved releases:\n%s", errOut.String())
	}
	releases, err := LoadStack(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) == 0 {
		t.Fatal("no releases found; the helmfile path or glob is wrong")
	}
	t.Logf("%d releases resolved", len(releases))
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

func TestTagVersionIsValidatedBeforeItIsWritten(t *testing.T) {
	// The version out of a tag is written verbatim into a shipped helmfile, and
	// anyone who can push a tag chooses it. A value carrying a space, a quote or
	// a newline would corrupt the file or smuggle in an adjacent key.
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": stackFile})
	for _, bad := range []string{
		"deploy/helm/alpha/v2.0.0 extra",
		`deploy/helm/alpha/v"quoted"`,
		"deploy/helm/alpha/vlatest",
		"deploy/helm/alpha/v../../etc/passwd",
	} {
		code, _, errOut := f.bump(t, bad, true)
		if code != 1 {
			t.Errorf("tag %q must be rejected, got exit %d", bad, code)
		}
		if !strings.Contains(errOut, "not a plain version string") {
			t.Errorf("tag %q: want a version-format error, got %q", bad, errOut)
		}
	}
	// A newline is rejected one step earlier, by the tag pattern itself, since
	// Go's . does not match one. Asserted separately so the message difference
	// is deliberate rather than a gap.
	if code, _, errOut := f.bump(t, "deploy/helm/alpha/v2.0.0\nversion: 9.9.9", true); code != 1 ||
		!strings.Contains(errOut, "not a chart release tag") {
		t.Errorf("a tag carrying a newline must be rejected: exit %d, %q", code, errOut)
	}
	if f.read(t, "00-stack.yaml.gotmpl") != stackFile {
		t.Fatal("a rejected tag still wrote to the helmfile")
	}
}

func TestNestedVersionKeyIsNotMistakenForThePin(t *testing.T) {
	// A version: inside a values block sits deeper than the release's own
	// fields. Matching any indent rewrites an unrelated key and leaves the real
	// pin untouched.
	// The nested key comes FIRST, before the release's own pin. Ordered the
	// other way the loop finds the real pin and stops, so the test passes even
	// with the indent check removed. Mutation testing caught exactly that.
	body := `releases:
  - name: alpha
    namespace: nvcf
    values:
      - image:
          version: 7.7.7
    version: 1.0.0
`
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": body})
	if code, _, errOut := f.bump(t, "deploy/helm/alpha/v2.0.0", true); code != 0 {
		t.Fatalf("bump should succeed, got %d: %s", code, errOut)
	}
	got := f.read(t, "00-stack.yaml.gotmpl")
	if !strings.Contains(got, "    version: 2.0.0") {
		t.Fatalf("the release pin did not move:\n%s", got)
	}
	if !strings.Contains(got, "          version: 7.7.7") {
		t.Fatalf("the nested value must not be touched:\n%s", got)
	}
}

func TestUnreadableReleaseVersionIsReportedNotSkipped(t *testing.T) {
	// Silently skipping a release-level version that cannot be parsed drops a
	// real pin, which is the failure this tool exists to prevent. A block with
	// no version at all is still not a pin, and must stay that way.
	body := `repositories:
  - name: nvcf
    url: oci://example.invalid/nvcf

releases:
  - name: alpha
    version: {{ .Values.someVersion }}
`
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": body})
	code, out, errOut := f.audit(t)
	if code != 1 {
		t.Fatalf("an unreadable pin must fail the audit, got %d", code)
	}
	if !strings.Contains(out, "1 releases, 1 unresolved") {
		t.Fatalf("it must be counted, not dropped:\n%s", out)
	}
	if !strings.Contains(errOut, "not a recognisable pin") {
		t.Fatalf("the reason should say the version is unreadable:\n%s", errOut)
	}
}

func TestQuotedVersionIsStillAPin(t *testing.T) {
	// Nothing in the stack is quoted today. If a value gains quotes it must not
	// silently stop being recognised, which would drop it from every bump.
	body := `releases:
  - name: alpha
    version: "1.0.0"
`
	f := newStack(t, stackMeta, map[string]string{"00-stack.yaml.gotmpl": body})
	code, out, errOut := f.audit(t)
	if code != 0 {
		t.Fatalf("a quoted version is still a pin, got %d: %s", code, errOut)
	}
	if !strings.Contains(out, "1 releases, 0 unresolved") {
		t.Fatalf("want it counted as a resolved pin:\n%s", out)
	}
}
