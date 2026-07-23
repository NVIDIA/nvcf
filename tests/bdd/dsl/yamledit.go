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

package dsl

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MatchMode selects between strict subtree equality and subset matching
// for MatchYAMLSubtree. Map comparison ignores key order in both modes.
// List comparison is order-sensitive and length-strict in both modes;
// YAML lists carry positional meaning, and a set-style subset would
// silently accept reorderings.
type MatchMode int

const (
	// MatchExact requires every key in expected and every key in actual
	// to be present in the other side, recursively.
	MatchExact MatchMode = iota
	// MatchSubset requires every key in expected to exist in actual
	// with the same value; extra keys in actual are tolerated.
	MatchSubset
)

// UpdateYAMLKeys reads the YAML file at path, applies each (dotted-path,
// value) pair as an upsert, and writes the file back. Path syntax uses
// "." between segments and "[n]" for list indices; missing intermediate
// maps and missing list slots are created. Encountering a scalar where a
// map or list is expected is a hard error. Value cells run through
// Interpolate before assignment.
func UpdateYAMLKeys(path string, keys [][2]string) error {
	root, err := readYAMLAny(path)
	if err != nil {
		return err
	}
	rootMap, ok := root.(map[string]any)
	if root == nil {
		rootMap = map[string]any{}
	} else if !ok {
		return fmt.Errorf("update yaml %s: top-level value is not a map", path)
	}
	for _, kv := range keys {
		segments, err := parsePath(kv[0])
		if err != nil {
			return fmt.Errorf("update yaml %s: %w", path, err)
		}
		if err := setNested(rootMap, segments, Interpolate(kv[1])); err != nil {
			return fmt.Errorf("update yaml %s: %w", path, err)
		}
	}
	return writeYAML(path, rootMap)
}

// ReadYAMLKey resolves a dotted path against the YAML at path.
// Returns (value, true, nil) on found, (empty, false, nil) on missing
// key, and a non-nil error only for IO or parse failures. Non-scalar
// values are rendered with fmt.Sprint.
func ReadYAMLKey(path, dottedKey string) (string, bool, error) {
	root, err := readYAMLAny(path)
	if err != nil {
		return "", false, err
	}
	segments, err := parsePath(dottedKey)
	if err != nil {
		return "", false, err
	}
	value, ok := walkPath(root, segments)
	if !ok {
		return "", false, nil
	}
	if value == nil {
		return "", true, nil
	}
	return fmt.Sprint(value), true, nil
}

// MatchYAMLSubtree compares the subtree at keyPath inside the YAML file
// at filePath to the parsed expectedYAML. Empty keyPath compares against
// the whole file. The expected docstring runs through Interpolate before
// parsing so ${VAR} cells resolve at compare time. On mismatch the
// returned error names the first differing path.
func MatchYAMLSubtree(filePath, keyPath, expectedYAML string, mode MatchMode) error {
	var expected any
	if err := yaml.Unmarshal([]byte(Interpolate(expectedYAML)), &expected); err != nil {
		return fmt.Errorf("parse expected yaml: %w", err)
	}
	root, err := readYAMLAny(filePath)
	if err != nil {
		return err
	}
	actual := root
	if keyPath != "" {
		segments, err := parsePath(keyPath)
		if err != nil {
			return err
		}
		got, ok := walkPath(root, segments)
		if !ok {
			return fmt.Errorf("%s: key path %q is not present", filePath, keyPath)
		}
		actual = got
	}
	return deepCompare(expected, actual, mode, keyPath)
}

// SubstituteFile replaces every occurrence of placeholder in the file at
// path with replacement and writes the result back. Used for credential
// rendering; the handler never logs placeholder or replacement. Caller
// is responsible for any ${VAR} expansion in replacement.
func SubstituteFile(path, placeholder, replacement string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	rendered := strings.ReplaceAll(string(body), placeholder, replacement)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return os.WriteFile(path, []byte(rendered), info.Mode().Perm())
}

// SubstituteFileBlock parses an exact old/new block pair separated by one
// YAML document-marker line and replaces the old block in path. It fails when
// the spec is malformed or the old block is absent so documentation drift
// cannot silently turn a requested edit into a no-op.
func SubstituteFileBlock(path, spec string) error {
	const separator = "\n---\n"
	if strings.Count(spec, separator) != 1 {
		return fmt.Errorf("substitution spec must contain exactly one --- separator line")
	}
	parts := strings.SplitN(spec, separator, 2)
	oldBlock, newBlock := parts[0], parts[1]
	if oldBlock == "" {
		return fmt.Errorf("substitution old block must not be empty")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !strings.Contains(string(body), oldBlock) {
		return fmt.Errorf("%s: substitution old block is not present", path)
	}
	return SubstituteFile(path, oldBlock, newBlock)
}

// readYAMLAny reads path and unmarshals into a generic any value.
// An empty document parses to nil.
func readYAMLAny(path string) (any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var v any
	if err := yaml.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}

// writeYAML serializes v back to path with default 0o644 permissions
// when the file does not already exist; an existing file keeps its
// permission bits.
func writeYAML(path string, v any) error {
	body, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, body, mode)
}

// pathSegment describes one piece of a dotted path. listIndex is only
// honored when isList is true.
type pathSegment struct {
	key       string
	listIndex int
	isList    bool
}

var listSuffix = regexp.MustCompile(`^([^\[]+)\[(\d+)\]$`)

// parsePath splits a dotted path into segments. Each segment is either
// a plain map key or a "key[n]" list reference. An empty path returns
// no segments and is valid.
func parsePath(p string) ([]pathSegment, error) {
	if p == "" {
		return nil, nil
	}
	parts := strings.Split(p, ".")
	out := make([]pathSegment, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid path %q: empty segment", p)
		}
		if m := listSuffix.FindStringSubmatch(part); m != nil {
			idx, err := strconv.Atoi(m[2])
			if err != nil {
				return nil, fmt.Errorf("invalid path %q: %w", p, err)
			}
			out = append(out, pathSegment{key: m[1], listIndex: idx, isList: true})
			continue
		}
		if strings.Contains(part, "[") || strings.Contains(part, "]") {
			return nil, fmt.Errorf("invalid path %q: malformed list segment %q", p, part)
		}
		out = append(out, pathSegment{key: part})
	}
	return out, nil
}

// DescribeMissingKey returns a one-line debug hint for "key X not
// present" errors: it walks as far down the path as it can, then lists
// the keys that ARE present at the deepest reachable level. Useful in
// assertion failure messages so the operator sees what the file
// actually contains.
func DescribeMissingKey(filePath, dottedKey string) string {
	root, err := readYAMLAny(filePath)
	if err != nil {
		return ""
	}
	segments, err := parsePath(dottedKey)
	if err != nil {
		return ""
	}
	current := root
	traversed := ""
	for i, seg := range segments {
		m, ok := current.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s is %T, not a map", displayPath(traversed), current)
		}
		next, present := m[seg.key]
		if !present {
			here := displayPath(traversed)
			return fmt.Sprintf("%s has keys %v; missing %q (path index %d)", here, sortedKeys(m), seg.key, i)
		}
		traversed = joinPath(traversed, seg.key)
		current = next
		if seg.isList {
			list, ok := current.([]any)
			if !ok {
				return fmt.Sprintf("%s is %T, not a list", traversed, current)
			}
			if seg.listIndex >= len(list) {
				return fmt.Sprintf("%s has length %d; index %d out of range", traversed, len(list), seg.listIndex)
			}
			current = list[seg.listIndex]
			traversed = fmt.Sprintf("%s[%d]", traversed, seg.listIndex)
		}
	}
	return ""
}

// walkPath returns the value at the given path or (nil, false) when any
// segment along the way is missing or has the wrong type for traversal.
func walkPath(root any, segments []pathSegment) (any, bool) {
	current := root
	for _, seg := range segments {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[seg.key]
		if !ok {
			return nil, false
		}
		if seg.isList {
			list, ok := next.([]any)
			if !ok || seg.listIndex >= len(list) {
				return nil, false
			}
			current = list[seg.listIndex]
			continue
		}
		current = next
	}
	return current, true
}

// setNested walks segments inside root, creating intermediate maps and
// list entries as needed, and assigns value at the final segment.
// Returns an error if a scalar is in the way of a required map or list.
func setNested(root map[string]any, segments []pathSegment, value any) error {
	if len(segments) == 0 {
		return fmt.Errorf("empty path")
	}
	current := root
	for i, seg := range segments {
		final := i == len(segments)-1
		if seg.isList {
			list, err := ensureList(current, seg.key, seg.listIndex)
			if err != nil {
				return err
			}
			if final {
				list[seg.listIndex] = value
				current[seg.key] = list
				return nil
			}
			entryMap, ok := list[seg.listIndex].(map[string]any)
			if list[seg.listIndex] == nil {
				entryMap = map[string]any{}
				list[seg.listIndex] = entryMap
			} else if !ok {
				return fmt.Errorf("path %s[%d]: existing scalar blocks descent", seg.key, seg.listIndex)
			}
			current[seg.key] = list
			current = entryMap
			continue
		}
		if final {
			current[seg.key] = value
			return nil
		}
		next, ok := current[seg.key].(map[string]any)
		if current[seg.key] == nil {
			next = map[string]any{}
			current[seg.key] = next
		} else if !ok {
			return fmt.Errorf("path %s: existing scalar blocks descent", seg.key)
		}
		current = next
	}
	return nil
}

// ensureList returns the list at parent[key], growing it (with nils as
// fillers) so that index is in range. A missing key creates a new list.
// A scalar at parent[key] is an error.
func ensureList(parent map[string]any, key string, index int) ([]any, error) {
	existing, present := parent[key]
	if !present || existing == nil {
		list := make([]any, index+1)
		return list, nil
	}
	list, ok := existing.([]any)
	if !ok {
		return nil, fmt.Errorf("path %s: existing scalar blocks list", key)
	}
	for len(list) <= index {
		list = append(list, nil)
	}
	return list, nil
}

// deepCompare recurses into expected/actual according to the given mode.
// path is the dotted location of the current node, used in error
// messages. The root of the comparison passes "".
func deepCompare(expected, actual any, mode MatchMode, path string) error {
	if expected == nil && actual == nil {
		return nil
	}
	if expectedMap, ok := expected.(map[string]any); ok {
		actualMap, isMap := actual.(map[string]any)
		if !isMap {
			return mismatch(path, "map", actual)
		}
		return compareMaps(expectedMap, actualMap, mode, path)
	}
	if expectedList, ok := expected.([]any); ok {
		actualList, isList := actual.([]any)
		if !isList {
			return mismatch(path, "list", actual)
		}
		return compareLists(expectedList, actualList, mode, path)
	}
	if !reflect.DeepEqual(expected, actual) {
		return fmt.Errorf("%s: expected %v, got %v", displayPath(path), expected, actual)
	}
	return nil
}

// compareMaps iterates in sorted key order so the first reported
// mismatch is deterministic across runs.
func compareMaps(expected, actual map[string]any, mode MatchMode, path string) error {
	expectedKeys := sortedKeys(expected)
	for _, k := range expectedKeys {
		childPath := joinPath(path, k)
		actualV, present := actual[k]
		if !present {
			return fmt.Errorf("%s: expected key missing in actual", childPath)
		}
		if err := deepCompare(expected[k], actualV, mode, childPath); err != nil {
			return err
		}
	}
	if mode == MatchExact {
		for _, k := range sortedKeys(actual) {
			if _, present := expected[k]; !present {
				return fmt.Errorf("%s: unexpected key in actual", joinPath(path, k))
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compareLists(expected, actual []any, mode MatchMode, path string) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("%s: expected list length %d, got %d", displayPath(path), len(expected), len(actual))
	}
	for i := range expected {
		childPath := fmt.Sprintf("%s[%d]", path, i)
		if err := deepCompare(expected[i], actual[i], mode, childPath); err != nil {
			return err
		}
	}
	return nil
}

func mismatch(path, expectedKind string, actual any) error {
	return fmt.Errorf("%s: expected %s, got %T", displayPath(path), expectedKind, actual)
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func displayPath(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}
