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

package bdd_tmp

import (
	"os"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNVCFCLINonlocalFixtureMatchesCLITemplate asserts every top-level
// key in tests/bdd/fixtures/nvcf-cli-nonlocal.yaml.template is also
// declared (active or commented documentation) in the canonical CLI
// template at src/clis/nvcf-cli/.nvcf-cli.yaml.template.
//
// The BDD fixture is intentionally a trimmed subset of the CLI
// template (chart-level constants only, no production URLs, no
// inline docs). If the CLI renames or removes a key the BDD fixture
// references, the runtime CLI config the feature builds at suite
// runtime would silently lose that key. This test catches the
// rename/remove at unit-test time so the wiring is forced to update
// in lockstep.
//
// The CLI template's commented-out documentation blocks (e.g.
// `# api_keys_host: api-keys.nvcf.example.com`) are sufficient to
// pass the assertion. The contract is "the CLI knows about this key",
// not "the CLI defaults it".
func TestNVCFCLINonlocalFixtureMatchesCLITemplate(t *testing.T) {
	const (
		fixturePath     = "fixtures/nvcf-cli-nonlocal.yaml.template"
		cliTemplatePath = "../../src/clis/nvcf-cli/.nvcf-cli.yaml.template"
	)

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	var fixture map[string]any
	if err := yaml.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatalf("parse fixture %s: %v", fixturePath, err)
	}
	if len(fixture) == 0 {
		t.Fatalf("fixture %s has no top-level keys", fixturePath)
	}

	cliTemplateBytes, err := os.ReadFile(cliTemplatePath)
	if err != nil {
		t.Fatalf("read CLI template %s: %v", cliTemplatePath, err)
	}
	cliBody := string(cliTemplateBytes)

	for key := range fixture {
		// Match either an active key (`<key>:`) or a documentation
		// line that mentions the key explicitly (`# api_keys_host:`,
		// `# Config key: api_keys_host`). Anchor on word boundaries
		// so a substring of another key cannot satisfy the match.
		pattern := regexp.MustCompile(`(?m)(^|[^\w])` + regexp.QuoteMeta(key) + `(\s*:|\b)`)
		if !pattern.MatchString(cliBody) {
			t.Errorf("BDD fixture key %q not referenced in CLI template at %s; the CLI may have renamed or removed it", key, cliTemplatePath)
		}
	}
}
