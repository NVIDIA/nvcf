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

	got := stackReleaseNamespaces(dir)
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

	got := stackReleaseNamespaces(dir)
	assert.Equal(t, []string{"nvcf"}, got, "a templated namespace must not be probed")
}

func TestResolveStackNamespaces_FallsBackWhenNoStack(t *testing.T) {
	fallback := []string{"nvcf", "sis"}

	assert.Equal(t, fallback, resolveStackNamespaces("", fallback),
		"no stack path must use the static list")
	assert.Equal(t, fallback, resolveStackNamespaces(t.TempDir(), fallback),
		"a directory with no helmfile.d must use the static list")
}

func TestResolveStackNamespaces_PrefersStack(t *testing.T) {
	dir := writeStack(t, map[string]string{
		"01.yaml.gotmpl": "releases:\n  - name: a\n    namespace: from-stack\n",
	})
	assert.Equal(t, []string{"from-stack"}, resolveStackNamespaces(dir, []string{"static"}),
		"a readable stack must win over the static list")
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

	got := stackReleaseNamespaces(dir)
	assert.Equal(t, []string{"nvcf"}, got,
		"only release-level namespaces may be probed; nested values keys must be ignored")
}
