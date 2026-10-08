// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvcaconfig

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const yamlMergeTag = "!!merge"

// KeyConflictError reports keys in one mapping that the decoder treats as the
// same key. Viper folds keys to lower case while iterating a Go map, so keys
// that differ only by case would otherwise resolve to an arbitrary value on
// each decode.
//
// +k8s:deepcopy-gen=false
type KeyConflictError struct {
	// Path is the dotted path of the mapping that holds the keys. It is empty
	// for the top level.
	Path string
	// Keys lists every spelling in document order.
	Keys []string
	// Lines lists the line of each key in Keys.
	Lines []int
}

func (e *KeyConflictError) Error() string {
	location := "at the top level"
	if e.Path != "" {
		location = fmt.Sprintf("under %q", e.Path)
	}
	lines := make([]string, len(e.Lines))
	for i, line := range e.Lines {
		lines[i] = strconv.Itoa(line)
	}

	distinct := map[string]struct{}{}
	for _, key := range e.Keys {
		distinct[key] = struct{}{}
	}
	if len(distinct) == 1 {
		return fmt.Sprintf("config key %q %s is defined more than once (lines %s)",
			e.Keys[0], location, strings.Join(lines, ", "))
	}

	quoted := make([]string, len(e.Keys))
	for i, key := range e.Keys {
		quoted[i] = strconv.Quote(key)
	}
	return fmt.Sprintf("config keys %s %s differ only by case (lines %s); keep one spelling",
		strings.Join(quoted, ", "), location, strings.Join(lines, ", "))
}

// validateYAMLKeys rejects YAML input whose mappings contain keys that are
// equal after Viper's case folding, including exact duplicates. It inspects
// the YAML node tree because decoding into a map has already discarded or
// collapsed the conflicting keys. Syntax errors are left to the decoder.
func validateYAMLKeys(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var v keyValidator
	v.walk(&doc, "")
	return errors.Join(v.errs...)
}

type keyValidator struct {
	errs []error
}

type yamlKey struct {
	name string
	line int
}

// walk does not follow aliases. Every anchor is defined in the same document
// and is checked where it is defined.
func (v *keyValidator) walk(node *yaml.Node, path string) {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			v.walk(child, path)
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			v.walk(child, fmt.Sprintf("%s[%d]", path, i))
		}
	case yaml.MappingNode:
		v.checkMapping(node, path)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if isMergeKey(key) || key.Kind != yaml.ScalarNode {
				continue
			}
			v.walk(value, joinKeyPath(path, key.Value))
		}
	}
}

// checkMapping groups the keys of one mapping by their folded spelling.
// Keys pulled in through "<<" merge keys count as part of the mapping, but an
// explicit key with the same exact spelling legitimately overrides them.
func (v *keyValidator) checkMapping(node *yaml.Node, path string) {
	var explicit []yamlKey
	var merged []yamlKey
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch {
		case isMergeKey(key):
			merged = append(merged, mergedKeys(value, key.Line, map[*yaml.Node]bool{})...)
		case key.Kind == yaml.ScalarNode:
			explicit = append(explicit, yamlKey{name: key.Value, line: key.Line})
		}
	}

	groups := map[string][]yamlKey{}
	var order []string
	add := func(k yamlKey) {
		folded := strings.ToLower(k.name)
		if _, ok := groups[folded]; !ok {
			order = append(order, folded)
		}
		groups[folded] = append(groups[folded], k)
	}
	exact := map[string]bool{}
	for _, k := range explicit {
		exact[k.name] = true
		add(k)
	}
	for _, k := range merged {
		if exact[k.name] {
			continue
		}
		exact[k.name] = true
		add(k)
	}

	for _, folded := range order {
		keys := groups[folded]
		if len(keys) < 2 {
			continue
		}
		conflict := &KeyConflictError{Path: path}
		for _, k := range keys {
			conflict.Keys = append(conflict.Keys, k.name)
			conflict.Lines = append(conflict.Lines, k.line)
		}
		v.errs = append(v.errs, conflict)
	}
}

// mergedKeys returns the keys contributed by the value of a "<<" merge key,
// reported at the line of the merge key that pulls them in.
func mergedKeys(node *yaml.Node, line int, visiting map[*yaml.Node]bool) []yamlKey {
	if visiting[node] {
		return nil
	}
	visiting[node] = true
	defer delete(visiting, node)

	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil
		}
		return mergedKeys(node.Alias, line, visiting)
	case yaml.SequenceNode:
		var keys []yamlKey
		for _, child := range node.Content {
			keys = append(keys, mergedKeys(child, line, visiting)...)
		}
		return keys
	case yaml.MappingNode:
		var keys []yamlKey
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			switch {
			case isMergeKey(key):
				keys = append(keys, mergedKeys(value, line, visiting)...)
			case key.Kind == yaml.ScalarNode:
				keys = append(keys, yamlKey{name: key.Value, line: line})
			}
		}
		return keys
	}
	return nil
}

func isMergeKey(key *yaml.Node) bool {
	return key.Kind == yaml.ScalarNode && key.Tag == yamlMergeTag
}

func joinKeyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
