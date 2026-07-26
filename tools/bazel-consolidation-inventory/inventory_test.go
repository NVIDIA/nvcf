// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTree lets the measurement functions be tested without a git repository.
type fakeTree struct{ files map[string]string }

func (f *fakeTree) tree() *Tree {
	t := &Tree{Root: "/fake", contents: map[string]string{}}
	for path, text := range f.files {
		t.paths = append(t.paths, path)
		t.contents[path] = text
	}
	return t
}

func newTree(files map[string]string) *Tree { return (&fakeTree{files: files}).tree() }

func TestBazelVersionsAbsentReportsNone(t *testing.T) {
	// Zero subtree files is the intended post-consolidation state.
	got, err := BazelVersions(newTree(map[string]string{"MODULE.bazel": "module()\n"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "none") {
		t.Errorf("got %q, want a none result", got)
	}
}

func TestBazelVersionsDistributionOrderedNumerically(t *testing.T) {
	got, err := BazelVersions(newTree(map[string]string{
		"src/a/.bazelversion": "9.1.1\n",
		"src/b/.bazelversion": "8.6.0\n",
		"src/c/.bazelversion": "8.6.0\n",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "2 on 8.6.0  1 on 9.1.1  "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBazelVersionsRejectsMalformed(t *testing.T) {
	// Reporting these as "none" would announce the plan's success condition on
	// the strength of a parse failure.
	for name, content := range map[string]string{
		"not a version":     "not-a-version\n",
		"digit prefix only": "9-not-a-version\n",
		"empty":             "\n",
		"two versions":      "8.6.0\n9.1.1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BazelVersions(newTree(map[string]string{"src/a/.bazelversion": content}))
			if err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestBazelVersionsExcludesVendor(t *testing.T) {
	got, err := BazelVersions(newTree(map[string]string{
		"src/a/.bazelversion":          "8.6.0\n",
		"src/a/vendor/x/.bazelversion": "7.0.0\n",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "1 on 8.6.0  "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGoSDKVersions(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		want    string
		wantErr string
	}{
		{
			name:   "absent reports none",
			source: "module(name = 'root')\n",
			want:   "none",
		},
		{
			name:   "single line",
			source: "go_sdk.download(version = \"1.25.11\")\n",
			want:   "1.25.11 ",
		},
		{
			name:   "multiline",
			source: "go_sdk.download(\n    version = \"1.25.0\",\n)\n",
			want:   "1.25.0 ",
		},
		{
			name:   "single quoted",
			source: "go_sdk.download(\n    version = '1.25.0',\n)\n",
			want:   "1.25.0 ",
		},
		{
			name:   "sorted numerically not lexically",
			source: "go_sdk.download(version = \"1.25.10\")\ngo_sdk.download(version = \"1.25.6\")\n",
			want:   "1.25.6 1.25.10 ",
		},
		{
			// The version is a variable. An earlier implementation scanned the
			// body for any version-like token and reported the number in the
			// comment.
			name:    "non literal version with semver in comment",
			source:  "go_sdk.download(\n    version = GO_VERSION,  # used 1.25.0 before\n)\n",
			wantErr: "not a string literal",
		},
		{
			name:    "missing version attribute",
			source:  "go_sdk.download(\n    name = \"go\",\n)\n",
			wantErr: "no version attribute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GoSDKVersions(
				newTree(map[string]string{"src/a/MODULE.bazel": tc.source}), false, "first-party")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got result %q", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoSDKVendorSeparation(t *testing.T) {
	files := map[string]string{
		"src/a/MODULE.bazel":          "go_sdk.download(version = \"1.25.11\")\n",
		"src/a/vendor/x/MODULE.bazel": "go_sdk.download(version = \"1.23.0\")\n",
	}
	first, err := GoSDKVersions(newTree(files), false, "first-party")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "1.25.11 "; first != want {
		t.Errorf("first-party: got %q, want %q", first, want)
	}
	all, err := GoSDKVersions(newTree(files), true, "vendored-inclusive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "1.23.0 1.25.11 "; all != want {
		t.Errorf("vendored-inclusive: got %q, want %q", all, want)
	}
}

func TestOCIPulls(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		check   func(*testing.T, Pulls)
		wantErr string
	}{
		{
			name: "indented closing parenthesis",
			source: "oci.pull(\n    image = \"nvcr.io/x\",\n" +
				"    digest = \"sha256:aa\",\n    )\n",
			check: func(t *testing.T, p Pulls) {
				if p.Declarations != 1 || p.FromNVCR != 1 || p.Digests != 1 {
					t.Errorf("got %+v, want 1 declaration from nvcr.io with 1 digest", p)
				}
			},
		},
		{
			name: "single quoted attributes",
			source: "oci.pull(\n    image = 'nvcr.io/x',\n" +
				"    digest = 'sha256:aa',\n)\n",
			check: func(t *testing.T, p Pulls) {
				if p.Declarations != 1 || p.Digests != 1 {
					t.Errorf("got %+v, want 1 declaration with 1 digest", p)
				}
			},
		},
		{
			name: "tag only pull is named",
			source: "oci.pull(\n    image = \"public.ecr.aws/x\",\n" +
				"    tag = \"21-jre\",\n)\n",
			check: func(t *testing.T, p Pulls) {
				if len(p.TagOnly) != 1 || p.TagOnly[0] != "public.ecr.aws/x" {
					t.Errorf("got %+v, want the tag-only image named", p)
				}
			},
		},
		{
			name:   "parenthesis inside a string does not end the call",
			source: "oci.pull(\n    image = \"nvcr.io/x\",  # note )\n    digest = \"sha256:aa\",\n)\n",
			check: func(t *testing.T, p Pulls) {
				if p.Declarations != 1 {
					t.Errorf("got %+v, want 1 declaration", p)
				}
			},
		},
		{
			// Skipping this call would have silently lowered the count, turning
			// an unparseable declaration into an absent one.
			name:    "non literal image",
			source:  "oci.pull(\n    image = BASE_IMAGE,\n)\n",
			wantErr: "not a string literal",
		},
		{
			name:    "missing image attribute",
			source:  "oci.pull(\n    name = \"x\",\n)\n",
			wantErr: "no image attribute",
		},
		{
			name:    "malformed digest",
			source:  "oci.pull(\n    image = \"nvcr.io/x\",\n    digest = \"notadigest\",\n)\n",
			wantErr: "is not a sha256 digest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OCIPulls(newTree(map[string]string{"src/a/MODULE.bazel": tc.source}))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got %+v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, got)
		})
	}
}

func TestOCIPullsExcludesVendor(t *testing.T) {
	got, err := OCIPulls(newTree(map[string]string{
		"src/a/vendor/x/MODULE.bazel": "oci.pull(\n    image = \"docker.io/v\",\n    tag = \"latest\",\n)\n",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Declarations != 0 {
		t.Errorf("got %+v, want no declarations from vendored files", got)
	}
}

func TestFindCallsIgnoresCommentsAndStrings(t *testing.T) {
	src := "# oci.pull(fake)\nSOME = \"oci.pull(also fake)\"\noci.pull(\n    image = \"x\",\n)\n"
	calls, err := FindCalls(src, "oci.pull")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
}

func TestFindCallsRejectsUnterminated(t *testing.T) {
	if _, err := FindCalls("oci.pull(\n    image = \"x\",\n", "oci.pull"); err == nil {
		t.Fatal("expected an error for an unterminated call")
	}
}

// --- repository-level behavior -------------------------------------------

func gitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "fixture@example.com"},
		{"config", "user.name", "Fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func gitCommit(t *testing.T, root, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTreeRejectsDirtyByDefault(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, root, "MODULE.bazel", "module()\n")
	gitCommit(t, root, "initial")
	write(t, root, "MODULE.bazel", "module(name='changed')\n")

	if _, err := NewTree(root, false); err == nil {
		t.Fatal("expected a dirty tree to be rejected")
	}
	tree, err := NewTree(root, true)
	if err != nil {
		t.Fatalf("--allow-dirty should proceed: %v", err)
	}
	if !tree.Dirty {
		t.Error("tree should be marked dirty")
	}
}

func TestTreeRejectsTrackedSymlink(t *testing.T) {
	// A symlink can point outside the repository, where changing the target
	// alters the measurements while leaving git status clean, so the report
	// would no longer correspond to the commit it stamps.
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.bazelversion")
	if err := os.WriteFile(outside, []byte("8.6.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)
	write(t, root, "MODULE.bazel", "module()\n")
	if err := os.MkdirAll(filepath.Join(root, "src/svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "src/svc/.bazelversion")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitCommit(t, root, "symlinked version file")

	tree, err := NewTree(root, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := BazelVersions(tree); err == nil {
		t.Fatal("expected a tracked symlink to be rejected")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q should mention the symlink", err)
	}
}

func TestTreeRejectsMissingTrackedFile(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, root, "MODULE.bazel", "module()\n")
	gitCommit(t, root, "initial")
	if err := os.Remove(filepath.Join(root, "MODULE.bazel")); err != nil {
		t.Fatal(err)
	}
	tree, err := NewTree(root, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := tree.Read("MODULE.bazel"); err == nil {
		t.Fatal("expected a missing tracked file to be an error")
	}
}

func TestGitFailureIsAnError(t *testing.T) {
	// An empty result from a failed git call is indistinguishable from a
	// repository with no tracked files.
	if _, err := NewTree(t.TempDir(), false); err == nil {
		t.Fatal("expected an error outside a git repository")
	}
}

func TestConsolidatedRepositoryReportsSuccessfully(t *testing.T) {
	// The goal state must be reportable: zero nested modules, zero subtree
	// versions. An earlier implementation aborted silently here.
	root := t.TempDir()
	gitInit(t, root)
	write(t, root, "MODULE.bazel", "module()\n")
	write(t, root, "MODULE.bazel.lock", "{}\n")
	write(t, root, ".bazelversion", "8.6.0\n")
	write(t, root, "src/svc/BUILD.bazel", "go_library(name = \"x\")\n")
	write(t, root, "rules/oci/defs.bzl", "root oci defs\n")
	gitCommit(t, root, "consolidated")

	tree, err := NewTree(root, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	report, err := BuildReport(tree)
	if err != nil {
		t.Fatalf("consolidated repository failed to report: %v", err)
	}
	for _, want := range []string{
		"service modules (src, excluding vendor) : 0",
		"subtree .bazelversion values            : none",
		"go_sdk.download (first-party)           : none",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}
