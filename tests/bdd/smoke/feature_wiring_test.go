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

package smoke

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

var gherkinStepLine = regexp.MustCompile(`^\s*(?:Given|When|Then|And|But)\s+(.+?)\s*$`)

func TestSmokeFeaturesUseUnambiguousSharedSteps(t *testing.T) {
	definitions := &definitionCatalog{}
	steps.RegisterSmokeSteps(definitions, steps.NewScenarioContext(&harness.Suite{
		Config: harness.Config{OutDir: t.TempDir()},
	}))
	paths, err := smokeFeaturePaths("features")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no smoke features")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			for line, text := range featureSteps(t, path) {
				matches := definitions.matches(text)
				if matches != 1 {
					t.Errorf("line %d step %q matched %d definitions", line, text, matches)
				}
			}
		})
	}
}

func TestSmokeCatalogExcludesInstallBootstrap(t *testing.T) {
	definitions := &definitionCatalog{}
	steps.RegisterSmokeSteps(definitions, steps.NewScenarioContext(&harness.Suite{
		Config: harness.Config{OutDir: t.TempDir()},
	}))
	if matches := definitions.matches("a single-cluster ncp-local cluster is running"); matches != 0 {
		t.Fatalf("install bootstrap matched %d smoke step definitions", matches)
	}
}

func smokeFeaturePaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() && filepath.Ext(path) == ".feature" {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

type definitionCatalog struct {
	patterns []*regexp.Regexp
}

func (c *definitionCatalog) Step(expr, _ interface{}) {
	var pattern string
	switch value := expr.(type) {
	case string:
		pattern = value
	case []byte:
		pattern = string(value)
	case *regexp.Regexp:
		c.patterns = append(c.patterns, value)
		return
	default:
		panic(fmt.Sprintf("unsupported step expression %T", expr))
	}
	c.patterns = append(c.patterns, regexp.MustCompile(pattern))
}

func (c *definitionCatalog) matches(text string) int {
	matches := 0
	for _, pattern := range c.patterns {
		if pattern.MatchString(text) {
			matches++
		}
	}
	return matches
}

func featureSteps(t *testing.T, path string) map[int]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := make(map[int]string)
	scanner := bufio.NewScanner(file)
	inDocString := false
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if strings.TrimSpace(text) == `"""` {
			inDocString = !inDocString
			continue
		}
		if inDocString {
			continue
		}
		match := gherkinStepLine.FindStringSubmatch(text)
		if len(match) == 2 {
			result[line] = match[1]
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
