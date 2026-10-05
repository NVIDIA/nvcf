/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package selfhosted

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeStack lays out a minimal helmfile.d matching the real stack's shape.
func writeStack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	hd := filepath.Join(dir, "helmfile.d")
	require.NoError(t, os.MkdirAll(hd, 0o755))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(hd, name), []byte(body), 0o600))
	}
	return dir
}

func TestStackReleaseNamespaces_ReadsLiteralNamespaces(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01-dependencies.yaml.gotmpl": `
releases:
  - name: nats
    namespace: nats-system
  - name: cert-manager
    namespace: cert-manager
`,
		"02-core.yaml.gotmpl": `
releases:
  - name: api
    namespace: nvcf
  - name: ui
    namespace: nvcf-ui
`,
	})

	got := namespaceNames(stackReleaseNamespaces(dir))
	assert.Equal(t, []string{"cert-manager", "nats-system", "nvcf", "nvcf-ui"}, got,
		"namespaces must come from the stack, de-duplicated and sorted")
}

// The gateway controller namespace is templated from a values key, so its
// rendered value is not knowable from the source. Including the raw template
// text would produce a garbage namespace to probe and, on a hit, to delete.
func TestStackReleaseNamespaces_SkipsTemplatedNamespaces(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"02-core.yaml.gotmpl": `
releases:
  - name: api
    namespace: nvcf
  - name: ingress
    namespace: {{ .Values.ingress.gatewayApi.controllerNamespace }}
`,
	})

	got := namespaceNames(stackReleaseNamespaces(dir))
	assert.Equal(t, []string{"nvcf"}, got, "a templated namespace must not be probed")
}

func TestResolveStackNamespaces_FallsBackWhenNoStack(t *testing.T) {
	fallback := []stackNamespace{{name: "nvcf"}, {name: "sis"}}
	want := []string{"nvcf", "sis"}

	assert.Equal(t, want, resolveStackNamespaces("", "", fallback),
		"no stack path must use the static list")
	assert.Equal(t, want, resolveStackNamespaces(t.TempDir(), "local", fallback),
		"a directory with no helmfile.d must use the static list")
}

// The derived list is additive, not a replacement: it cannot see namespaces
// behind a helmfiles: include, so on its own it can still be short. The static
// list stays as a floor.
func TestResolveStackNamespaces_UnionsStackWithStatic(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": "releases:\n  - name: a\n    namespace: from-stack\n",
	})
	got := resolveStackNamespaces(dir, "", []stackNamespace{{name: "static"}})
	assert.Contains(t, got, "from-stack", "the stack's namespaces must be picked up")
	assert.Contains(t, got, "static", "the static list must remain a floor")
}

// An unreadable fragment must not yield a truncated list that then overrides
// the complete static one, which would report an affirmative all-clear over a
// scan missing a third of its namespaces.
func TestStackReleaseNamespaces_UnreadableFragmentYieldsNothing(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": "releases:\n  - name: a\n    namespace: one\n",
		"02.yaml.gotmpl": "releases:\n  - name: b\n    namespace: two\n",
	})
	// A directory, not a chmod 000 file: root bypasses the permission bits, so
	// a mode-based fixture passes locally and fails in CI, where tests run as
	// root. os.ReadFile returns EISDIR for every UID, so the test means the
	// same thing everywhere rather than being skipped where it matters most.
	unreadable := filepath.Join(dir, "helmfile.d", "02.yaml.gotmpl")
	require.NoError(t, os.Remove(unreadable))
	require.NoError(t, os.Mkdir(unreadable, 0o755))

	assert.Nil(t, stackReleaseNamespaces(dir),
		"a partial parse must not masquerade as the stack's full namespace set")
}

// helmfile accepts plain .yaml fragments too.
func TestStackReleaseNamespaces_ReadsPlainYAMLFragments(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml": "releases:\n  - name: a\n    namespace: plain-yaml\n",
	})
	assert.Contains(t, namespaceNames(stackReleaseNamespaces(dir)), "plain-yaml")
}

// A `namespace:` key nested inside a release's values block is chart
// configuration, not a namespace the stack owns. Reading it as one would add a
// probe target whose remediation is deletion.
func TestStackReleaseNamespaces_SkipsNestedValuesKeys(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"02-core.yaml.gotmpl": `
releases:
  - name: api
    namespace: nvcf
    values:
      - serviceMonitor:
          namespace: monitoring
      - someChart:
          namespace: kube-system
`,
	})

	got := namespaceNames(stackReleaseNamespaces(dir))
	assert.Equal(t, []string{"nvcf"}, got,
		"only release-level namespaces may be probed; nested values keys must be ignored")
}

// writeEnv adds environments/<name> to a stack laid out by writeStack.
func writeEnv(t *testing.T, dir, name, body string) {
	t.Helper()
	envDir := filepath.Join(dir, "environments")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, name), []byte(body), 0o600))
}

// Keys from 5 spaces in sit inside a release's values: and are chart
// configuration; 4 is the release level.
func TestStackReleaseNamespaces_IndentCap(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": "releases:\n  - name: a\n    namespace: four\n" +
			"  - name: b\n     namespace: five\n  - name: c\n      namespace: six\n",
	})
	assert.Equal(t, []string{"four"}, namespaceNames(stackReleaseNamespaces(dir)))
}

// A release behind a condition: deploys its namespace only when the stack's
// values turn it on. Without that, a default-off addon namespace the stack
// never installs, such as a platform team's own kai-scheduler, fails the run.
func TestResolveStackNamespaces_FollowsReleaseConditions(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": `
releases:
  - name: kai-scheduler
    condition: addons.kaiScheduler.enabled
    namespace: kai-scheduler
  - name: nvca-operator
    namespace: nvca-operator
  - name: grove-operator
    condition: addons.groveOperator.enabled # comment
    namespace: grove-system
`,
	})
	writeEnv(t, dir, "base.yaml",
		"addons:\n  kaiScheduler:\n    enabled: false\n  groveOperator:\n    enabled: false\n")
	assert.Equal(t, []string{"nvca-operator"}, resolveStackNamespaces(dir, "prod", nil),
		"base.yaml alone keeps the addons off")

	writeEnv(t, dir, "prod.yaml", "addons:\n  kaiScheduler:\n    enabled: true\n")
	assert.Equal(t, []string{"kai-scheduler", "nvca-operator"}, resolveStackNamespaces(dir, "prod", nil),
		"the environment file layered over base.yaml turns kai-scheduler on")
	assert.Equal(t, []string{"nvca-operator"}, resolveStackNamespaces(dir, "staging", nil),
		"another environment's file does not count")
}

// The static lists follow the same gates: the default when the stack is not
// readable, the stack's values when it is.
func TestResolveStackNamespaces_GatesTheStaticList(t *testing.T) {
	assert.Equal(t, []string{"nvca-operator"}, ComputePlaneStaleNamespaces("", ""),
		"the scheduling addons are off by default")
	cp := ControlPlaneStaleNamespaces("", "")
	assert.Contains(t, cp, "cassandra-system", "default-on gates keep their namespace")
	assert.NotContains(t, cp, "nvcf-ui", "the UI addon is off by default")

	dir := t.TempDir()
	writeEnv(t, dir, "base.yaml", "cassandra:\n  enabled: true\naddons:\n  kaiScheduler:\n    enabled: false\n")
	writeEnv(t, dir, "prod.yaml", "cassandra:\n  enabled: false\naddons:\n  kaiScheduler:\n    enabled: true\n")
	assert.NotContains(t, ControlPlaneStaleNamespaces(dir, "prod"), "cassandra-system",
		"an external Cassandra's namespace is not the stack's")
	assert.Contains(t, ComputePlaneStaleNamespaces(dir, "prod"), "kai-scheduler")
	assert.NotContains(t, ComputePlaneStaleNamespaces(dir, "prod"), "grove-system")
}

// A release whose gate cannot be read from the source is not proof the stack
// installs into its namespace; installed: false is proof it does not.
func TestStackReleaseNamespaces_SkipsUnreadableGates(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": `
releases:
  - name: always
    namespace: always
{{- if $managedIssuer }}
  - name: in-template
    namespace: in-template
    needs:
{{- if dig "x" "enabled" true .Values }}
      - a/b
{{- end }}
{{- end }}
  - name: after-template
    namespace: after-template
  - name: templated-installed
    installed: {{ and $a $b }}
    namespace: templated-installed
  - name: not-installed
    installed: false
    namespace: not-installed
  - name: installed
    installed: true
    namespace: installed
`,
	})
	assert.Equal(t, []string{"after-template", "always", "installed"},
		namespaceNames(stackReleaseNamespaces(dir)))
}

// No helmfile declaration can put a namespace NVCA creates at runtime on the
// list: it holds live work.
func TestResolveStackNamespaces_NeverProbesRuntimeOwnedNamespaces(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": "releases:\n  - name: a\n    namespace: nvcf-backend\n" +
			"  - name: b\n    namespace: nvca-system\n",
	})
	assert.Empty(t, resolveStackNamespaces(dir, "", nil))
}

// The static lists are what a released binary probes with no stack checkout,
// so they must agree with the stacks: the same namespaces, gated by the same
// conditions, with base.yaml's defaults.
func TestStaticNamespaces_MatchTheStacks(t *testing.T) {
	root := repoStacksDir(t)
	for stack, static := range map[string][]stackNamespace{
		"self-managed":       nvcfControlPlaneNamespaces,
		"nvcf-compute-plane": nvcfComputePlaneNamespaces,
	} {
		dir := filepath.Join(root, stack)
		derived := stackReleaseNamespaces(dir)
		require.NotEmpty(t, derived, stack)
		base, found := loadValuesFiles([]string{filepath.Join(dir, "environments", "base.yaml")})
		require.True(t, found, stack)

		want := make(map[string]stackNamespace, len(derived))
		for _, ns := range derived {
			want[ns.name] = ns
		}
		assert.ElementsMatch(t, namespaceNames(derived), namespaceNames(static), stack)
		for _, ns := range static {
			assert.Equal(t, want[ns.name].gates, ns.gates, "%s: %s", stack, ns.name)
			for _, gate := range ns.gates {
				on, _ := digAny(base, strings.Split(gate, ".")...).(bool)
				assert.Equal(t, on, ns.on, "%s: %s default", stack, ns.name)
			}
		}
	}
}

// repoStacksDir returns the repository's deploy/stacks, found at or above the
// package directory. Bazel stages it as the test's data, so a run under Bazel
// fails without it rather than skipping the tests that pin the CLI to the
// stacks; elsewhere, as in a copy of the module alone, the test skips.
func repoStacksDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		stacks := filepath.Join(dir, "deploy", "stacks")
		if _, err := os.Stat(filepath.Join(stacks, "self-managed", "helmfile.d")); err == nil {
			return stacks
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Fatal("deploy/stacks is not in the test's runfiles; add //:nvcf-cli-stacks to its data")
	}
	t.Skip("deploy/stacks is not reachable from the test's working directory")
	return ""
}
