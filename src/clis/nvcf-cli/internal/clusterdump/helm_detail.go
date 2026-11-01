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

package clusterdump

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// helmDetailRunner is an optional capability: an exec-backed HelmRunner also
// exposes per-release manifest and values. Defining it separately keeps the
// core HelmRunner interface (and its test fakes) unchanged.
type helmDetailRunner interface {
	GetManifest(ctx context.Context, kubeCtx, name, namespace string) (string, error)
	GetValues(ctx context.Context, kubeCtx, name, namespace string) (map[string]interface{}, error)
}

// HelmReleaseDetail is the per-release manifest and (redacted) values. The
// manifest is written to the dump tree; values are small enough to also carry
// in dump.json.
type HelmReleaseDetail struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	Manifest  string                 `json:"-"`
	Values    map[string]interface{} `json:"values,omitempty"`
}

// sensitiveKeyRe matches helm value keys that typically hold secrets. The
// unambiguous terms match as substrings; "auth" and "cred" are anchored to whole
// words so diagnostic keys like authorizationMode or credentialMode are not
// needlessly masked.
var sensitiveKeyRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|apikey|api_key|accesskey|access_key|privatekey|private_key|credential|\bcred\b|\bauth\b|\.pem)`)

// collectHelmDetails captures the rendered manifest and user-supplied values for
// every helm release on the plane. No-op if the runner cannot provide detail.
func (pc *PlaneCollector) collectHelmDetails(ctx context.Context) ([]HelmReleaseDetail, []string) {
	hr, ok := pc.Helm.(helmDetailRunner)
	if !ok {
		return nil, nil
	}
	releases, err := pc.Helm.ListReleases(ctx, pc.Context)
	if err != nil {
		return nil, []string{fmt.Sprintf("helm list: %v", err)}
	}

	var (
		out   []HelmReleaseDetail
		warns []string
	)
	for _, r := range releases {
		d := HelmReleaseDetail{Name: r.Name, Namespace: r.Namespace}
		if m, err := hr.GetManifest(ctx, pc.Context, r.Name, r.Namespace); err != nil {
			warns = append(warns, fmt.Sprintf("helm manifest %s/%s: %v", r.Namespace, r.Name, err))
		} else {
			d.Manifest = redactManifest(m, pc.Options)
		}
		if v, err := hr.GetValues(ctx, pc.Context, r.Name, r.Namespace); err != nil {
			warns = append(warns, fmt.Sprintf("helm values %s/%s: %v", r.Namespace, r.Name, err))
		} else {
			d.Values = redactValues(v, pc.Options)
		}
		out = append(out, d)
	}
	return out, warns
}

// redactValues recursively masks values whose key looks sensitive.
func redactValues(v map[string]interface{}, opts DumpOptions) map[string]interface{} {
	if v == nil || !opts.redactSecrets() {
		return v
	}
	if m, ok := redactValueTree("", v).(map[string]interface{}); ok {
		return m
	}
	return v
}

func redactValueTree(key string, val interface{}) interface{} {
	switch t := val.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{}, len(t))
		for k, v := range t {
			m[k] = redactValueTree(k, v)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(t))
		for i, v := range t {
			s[i] = redactValueTree(key, v)
		}
		return s
	case string:
		if sensitiveKeyRe.MatchString(key) {
			return maskValue(t)
		}
		return t
	default:
		return val
	}
}

// redactManifest masks Secret data inside a rendered multi-document manifest by
// reusing redactObject on each Secret document.
func redactManifest(manifest string, opts DumpOptions) string {
	if !opts.redactSecrets() {
		return manifest
	}
	// Split into documents with a real YAML document reader rather than string
	// splitting, so a "---" line embedded in a value cannot mis-split a Secret
	// document and let it fall through unredacted.
	reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	var out []string
	for {
		doc, err := reader.Read()
		if s := redactYAMLDoc(doc, opts); s != "" {
			out = append(out, s)
		}
		if err != nil { // io.EOF (clean end) or a read error: stop either way
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n---\n") + "\n"
}

// redactYAMLDoc redacts a single YAML document. A document that does not parse
// into an object is returned verbatim (it carries no Secret structure to mask).
func redactYAMLDoc(doc []byte, opts DumpOptions) string {
	trimmed := strings.TrimSpace(string(doc))
	if trimmed == "" {
		return ""
	}
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return trimmed
	}
	u := &unstructured.Unstructured{Object: obj}
	redactObject(u, opts)
	if b, err := yaml.Marshal(u.Object); err == nil {
		return strings.TrimSpace(string(b))
	}
	return trimmed
}

// --- exec implementation (execHelmRunner is declared in collector.go) ---

func (h *execHelmRunner) GetManifest(ctx context.Context, kubeCtx, name, namespace string) (string, error) {
	args := []string{"get", "manifest", name, "-n", namespace}
	if kubeCtx != "" {
		args = append([]string{"--kube-context", kubeCtx}, args...)
	}
	out, err := exec.CommandContext(ctx, "helm", args...).Output()
	if err != nil {
		return "", fmt.Errorf("helm get manifest: %w", err)
	}
	return string(out), nil
}

func (h *execHelmRunner) GetValues(ctx context.Context, kubeCtx, name, namespace string) (map[string]interface{}, error) {
	args := []string{"get", "values", name, "-n", namespace, "-o", "json"}
	if kubeCtx != "" {
		args = append([]string{"--kube-context", kubeCtx}, args...)
	}
	out, err := exec.CommandContext(ctx, "helm", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("helm get values: %w", err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("parse helm values: %w", err)
	}
	return v, nil
}
