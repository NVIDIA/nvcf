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

package steps

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

// registerFileSteps hooks the file-operation handlers. Every step that
// mutates a path under the repo working tree snapshots the destination
// to the Ledger before its first write so suite teardown can restore.
func registerFileSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^I copy the file "([^"]*)" to "([^"]*)"$`, sc.iCopyFile)
	ctx.Step(`^I update yaml file "([^"]*)" with keys:$`, sc.iUpdateYAMLFile)
	ctx.Step(`^I substitute "([^"]*)" in file "([^"]*)" with base64 of "([^"]*)"$`, sc.iSubstituteBase64)
	ctx.Step(`^I substitute a block in file "([^"]*)":$`, sc.iSubstituteBlock)
	ctx.Step(`^environment variable "([^"]*)" is set$`, sc.environmentVariableIsSet)
	ctx.Step(`^file "([^"]*)" exists$`, sc.fileShouldExist)
}

// iCopyFile copies src to dest, recording dest with the Ledger before
// the write so suite teardown can restore.
func (sc *ScenarioContext) iCopyFile(src, dest string) error {
	resolvedSrc := sc.resolvePath(dsl.Interpolate(src))
	resolvedDest := sc.resolvePath(dsl.Interpolate(dest))
	if err := sc.Suite.Ledger.Snapshot(resolvedDest); err != nil {
		return err
	}
	return copyFile(resolvedSrc, resolvedDest)
}

// iUpdateYAMLFile applies the supplied table of dotted-path/value rows
// to path. Path keys do not interpolate; value cells do.
func (sc *ScenarioContext) iUpdateYAMLFile(path string, table *godog.Table) error {
	resolved := sc.resolvePath(dsl.Interpolate(path))
	if err := sc.Suite.Ledger.Snapshot(resolved); err != nil {
		return err
	}
	keys, err := tableToKeyValuePairs(table)
	if err != nil {
		return err
	}
	return dsl.UpdateYAMLKeys(resolved, keys)
}

// iSubstituteBase64 expands ${VAR} inside source, base64-encodes the
// result, and replaces every occurrence of placeholder in path. The
// handler never returns the substituted value to its caller so the
// secret material does not leak into logs. Go's base64.StdEncoding
// emits the encoded string on a single line with no line wrapping --
// equivalent to `base64 -w0` -- so the resulting value can be sed-
// substituted into the secrets template without the sed-substitution
// breaking on embedded newlines.
func (sc *ScenarioContext) iSubstituteBase64(placeholder, path, source string) error {
	resolvedPath := sc.resolvePath(dsl.Interpolate(path))
	if err := sc.Suite.Ledger.Snapshot(resolvedPath); err != nil {
		return err
	}
	resolvedSource := dsl.Interpolate(source)
	encoded := base64.StdEncoding.EncodeToString([]byte(resolvedSource))
	return dsl.SubstituteFile(resolvedPath, placeholder, encoded)
}

// iSubstituteBlock snapshots path before delegating the exact multi-line
// replacement to the pure DSL helper.
func (sc *ScenarioContext) iSubstituteBlock(path string, doc *godog.DocString) error {
	resolvedPath := sc.resolvePath(dsl.Interpolate(path))
	if err := sc.Suite.Ledger.Snapshot(resolvedPath); err != nil {
		return err
	}
	return dsl.SubstituteFileBlock(resolvedPath, dsl.Interpolate(doc.Content))
}

// environmentVariableIsSet asserts the named env var is non-empty.
// The scenario writer uses this to surface missing prerequisites
// before any later step interpolates a blank value.
func (sc *ScenarioContext) environmentVariableIsSet(name string) error {
	if os.Getenv(name) == "" {
		return fmt.Errorf("environment variable %q is not set", name)
	}
	return nil
}

// fileShouldExist is shared by the bare "file ... exists" Given/Then
// and the two domain aliases that read the same predicate.
func (sc *ScenarioContext) fileShouldExist(path string) error {
	resolved := sc.resolvePath(dsl.Interpolate(path))
	if _, err := os.Stat(resolved); err != nil {
		return fmt.Errorf("file %s: %w", resolved, err)
	}
	return nil
}

// resolvePath joins a repo-relative path to Config.RepoRoot. Absolute
// paths pass through unchanged.
func (sc *ScenarioContext) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(sc.Suite.Config.RepoRoot, path)
}

// copyFile preserves the source file's permission bits and checks the
// destination Close error so a flush failure surfaces instead of being
// silently dropped.
func copyFile(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	return nil
}

// tableToKeyValuePairs converts a two-column Godog table into the
// (path, value) slice that dsl.UpdateYAMLKeys consumes.
func tableToKeyValuePairs(table *godog.Table) ([][2]string, error) {
	if table == nil {
		return nil, fmt.Errorf("missing table")
	}
	out := make([][2]string, 0, len(table.Rows))
	for _, row := range table.Rows {
		if len(row.Cells) != 2 {
			return nil, fmt.Errorf("expected two cells per row, got %d", len(row.Cells))
		}
		out = append(out, [2]string{row.Cells[0].Value, row.Cells[1].Value})
	}
	return out, nil
}
