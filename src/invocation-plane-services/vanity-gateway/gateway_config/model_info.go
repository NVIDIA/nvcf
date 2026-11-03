/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package config

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	sigsyaml "sigs.k8s.io/yaml"
)

var modelInfoSectionOrder = []string{
	"openai.chatCompletions", "openai.completions", "openai.embeddings", "openai.responses",
	"openai.imageGenerations", "openai.imageEdits", "openai.imageVariations", anthropicMessagesSection,
}

func validateModelInfoFields(model string, fields map[string]json.RawMessage) error {
	for key, value := range fields {
		switch strings.ToLower(key) {
		case "id", "object", "created", "owned_by", "supported_endpoints", "visibility", "routes":
			return fmt.Errorf("model %q: modelInfoFields field %q is reserved", model, key)
		}
		if !json.Valid(value) {
			return fmt.Errorf("model %q: modelInfoFields field %q must be JSON-compatible", model, key)
		}
	}
	return nil
}

// ModelInfoFieldsByName builds an owned snapshot. First value wins per key;
// disjoint keys merge in section order, then source entry order.
func (c *GatewayConfig) ModelInfoFieldsByName() map[string]map[string]json.RawMessage {
	result := make(map[string]map[string]json.RawMessage)
	sections := c.modelSections()
	for _, section := range modelInfoSectionOrder {
		entries := sections[section]
		keys := c.modelEntryOrder[section]
		if keys == nil {
			for key := range entries {
				keys = append(keys, key)
			}
			slices.Sort(keys)
		}
		for _, key := range keys {
			entry := entries[key]
			for field, value := range entry.ModelInfoFields {
				if result[entry.ModelName] == nil {
					result[entry.ModelName] = make(map[string]json.RawMessage)
				}
				if _, exists := result[entry.ModelName][field]; !exists {
					result[entry.ModelName][field] = slices.Clone(value)
				}
			}
		}
	}
	return result
}

// Preserve YAML entry order and metadata scalar types before the existing
// YAML-to-JSON loader normalizes keys and numbers.
func unmarshalGatewayConfig(data []byte, c *GatewayConfig) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	normalized, err := normalizeYAML(&root, make(map[*yaml.Node]bool))
	if err != nil {
		return err
	}
	if len(normalized.Content) == 0 {
		return sigsyaml.Unmarshal(data, c)
	}
	v2 := yamlField(normalized.Content[0], "v2config")
	order := make(map[string][]string)
	metadata := make(map[string]map[string]map[string]json.RawMessage)
	for _, section := range modelInfoSectionOrder {
		node := v2
		for _, part := range strings.Split(section, ".") {
			node = yamlField(node, part)
		}
		if node == nil || node.Kind != yaml.MappingNode {
			continue
		}
		metadata[section] = make(map[string]map[string]json.RawMessage)
		for i := 0; i < len(node.Content); i += 2 {
			key, err := yamlConfigKey(node.Content[i])
			if err != nil {
				return err
			}
			order[section] = append(order[section], key)
			entry := node.Content[i+1]
			if entry.Kind != yaml.MappingNode {
				continue
			}
			modelName := ""
			if name := yamlField(entry, "modelName"); name != nil {
				modelName = name.Value
			}
			metadataPresent := false
			for j := 0; j < len(entry.Content); j += 2 {
				if !strings.EqualFold(entry.Content[j].Value, "modelInfoFields") {
					continue
				}
				if metadataPresent {
					return fmt.Errorf("model %q: duplicate modelInfoFields field", modelName)
				}
				metadataPresent = true
				value, err := modelInfoJSON(entry.Content[j+1])
				if err != nil {
					return fmt.Errorf("model %q: modelInfoFields: %w", modelName, err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(value, &fields); err != nil {
					return fmt.Errorf("model %q: modelInfoFields must be a mapping: %w", modelName, err)
				}
				metadata[section][key] = fields
				// Decode routing with the existing loader, without reinterpreting
				// metadata numbers or YAML 1.1 scalar types a second time.
				entry.Content[j+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
			}
		}
	}
	if v2 == nil {
		return sigsyaml.Unmarshal(data, c)
	}
	core, err := yaml.Marshal(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "v2config"}, v2,
	}})
	if err != nil {
		return err
	}
	if err := sigsyaml.Unmarshal(core, c); err != nil {
		return err
	}
	c.modelEntryOrder = order
	sections := c.modelSections()
	for section, entries := range metadata {
		for key, fields := range entries {
			entry := sections[section][key]
			entry.ModelInfoFields = fields
			sections[section][key] = entry
		}
	}
	return nil
}

func yamlField(node *yaml.Node, name string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if strings.EqualFold(node.Content[i].Value, name) {
			return node.Content[i+1]
		}
	}
	return nil
}

func modelInfoJSON(node *yaml.Node) (json.RawMessage, error) {
	switch node.Kind {
	case yaml.MappingNode:
		fields := make(map[string]json.RawMessage)
		for i := 0; i < len(node.Content); i += 2 {
			key := metadataScalar(node.Content[i])
			if key.Tag != "!!str" || legacyYAMLBool(key) != "" {
				return nil, fmt.Errorf("object keys must be strings")
			}
			if _, exists := fields[key.Value]; exists {
				return nil, fmt.Errorf("duplicate object key %q", key.Value)
			}
			value, err := modelInfoJSON(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			fields[key.Value] = value
		}
		return json.Marshal(fields)
	case yaml.SequenceNode:
		values := make([]json.RawMessage, len(node.Content))
		for i, child := range node.Content {
			value, err := modelInfoJSON(child)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return json.Marshal(values)
	case yaml.ScalarNode:
		node = metadataScalar(node)
		switch node.Tag {
		case "!!str":
			if value := legacyYAMLBool(node); value != "" {
				return json.RawMessage(value), nil
			}
			return json.Marshal(node.Value)
		case "!!null":
			return json.RawMessage("null"), nil
		case "!!bool":
			switch strings.ToLower(node.Value) {
			case "true", "yes", "on":
				return json.RawMessage("true"), nil
			case "false", "no", "off":
				return json.RawMessage("false"), nil
			}
			return nil, fmt.Errorf("invalid boolean")
		case "!!int":
			literal := strings.ReplaceAll(node.Value, "_", "")
			number, ok := yamlInteger(literal)
			if ok {
				return json.RawMessage(number.String()), nil
			}
		case "!!float":
			literal := strings.ReplaceAll(node.Value, "_", "")
			if strings.Contains(literal, ":") {
				whole, fraction, _ := strings.Cut(literal, ".")
				integer, ok := yamlInteger(whole)
				if !ok {
					return nil, fmt.Errorf("invalid number")
				}
				literal = integer.String() + "." + fraction
				if strings.HasPrefix(whole, "-") && integer.Sign() == 0 {
					literal = "-" + literal
				}
			}
			value, err := strconv.ParseFloat(literal, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("numbers must be finite")
			}
			if json.Valid([]byte(literal)) {
				return json.RawMessage(literal), nil
			}
			return json.Marshal(value)
		}
	}
	return nil, fmt.Errorf("value must be JSON-compatible")
}

// The existing loader uses YAML 1.1 boolean spellings. Quoted or explicitly
// tagged strings retain their string type.
func legacyYAMLBool(node *yaml.Node) string {
	if node.Tag != "!!str" || node.Style != 0 {
		return ""
	}
	switch node.Value {
	case "yes", "Yes", "YES", "on", "On", "ON":
		return "true"
	case "no", "No", "NO", "off", "Off", "OFF":
		return "false"
	}
	return ""
}

// Resolve implicit metadata scalars with YAML 1.1 forms. Quoting or explicit
// tags override implicit resolution. YAML libraries differ on exponent-only,
// octal, signed fractions, and base-60 literals; discovery must keep their types.
var (
	yamlIntegerForm = regexp.MustCompile(`^[+-]?(0b[01_]+|0[0-7_]+|0|[1-9][0-9_]*|0x[0-9a-fA-F_]+|[1-9][0-9_]*(:[0-5]?[0-9])+)$`)
	yamlFloatForm   = regexp.MustCompile(`^([+-]?[0-9][0-9_]*\.[0-9_]*([eE][+-][0-9]+)?|\.[0-9][0-9_]*([eE][+-][0-9]+)?|[+-]?[0-9][0-9_]*(:[0-5]?[0-9])+\.[0-9_]*|[+-]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)
)

func metadataScalar(node *yaml.Node) *yaml.Node {
	if node.Kind != yaml.ScalarNode || node.Style != 0 {
		return node
	}
	copy := *node
	switch node.Tag {
	case "!!str", "!!int", "!!float":
		switch {
		case yamlIntegerForm.MatchString(node.Value):
			copy.Tag = "!!int"
		case yamlFloatForm.MatchString(node.Value):
			copy.Tag = "!!float"
		default:
			copy.Tag = "!!str"
		}
	}
	return &copy
}

func yamlInteger(literal string) (*big.Int, bool) {
	if !strings.Contains(literal, ":") {
		return new(big.Int).SetString(literal, 0)
	}
	negative := strings.HasPrefix(literal, "-")
	literal = strings.TrimPrefix(strings.TrimPrefix(literal, "-"), "+")
	number := new(big.Int)
	for _, part := range strings.Split(literal, ":") {
		value, ok := new(big.Int).SetString(part, 10)
		if !ok {
			return nil, false
		}
		number.Mul(number, big.NewInt(60)).Add(number, value)
	}
	if negative {
		number.Neg(number)
	}
	return number, true
}

// normalizeYAML resolves aliases and merge defaults while retaining mapping
// insertion order. Explicit overrides keep the inherited key's position,
// matching YAML merge construction. Explicit duplicate keys and cycles fail.
func normalizeYAML(node *yaml.Node, active map[*yaml.Node]bool) (*yaml.Node, error) {
	if active[node] {
		return nil, fmt.Errorf("YAML cycles are unsupported")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		return normalizeYAML(node.Alias, active)
	}
	result := *node
	result.Anchor = ""
	result.Content = nil
	if value := legacyYAMLBool(node); value != "" {
		result.Tag, result.Value = "!!bool", value
	}
	if node.Kind != yaml.MappingNode {
		for _, child := range node.Content {
			normalized, err := normalizeYAML(child, active)
			if err != nil {
				return nil, err
			}
			result.Content = append(result.Content, normalized)
		}
		return &result, nil
	}
	positions := make(map[string]int)
	put := func(key, value *yaml.Node) {
		identity := yamlKeyIdentity(key)
		if index, exists := positions[identity]; exists {
			result.Content[index+1] = value
		} else {
			positions[identity] = len(result.Content)
			result.Content = append(result.Content, key, value)
		}
	}
	var explicit []*yaml.Node
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key, err := normalizeYAML(node.Content[i], active)
		if err != nil {
			return nil, err
		}
		value, err := normalizeYAML(node.Content[i+1], active)
		if err != nil {
			return nil, err
		}
		if key.Tag == "!!merge" {
			defaults := []*yaml.Node{value}
			if value.Kind == yaml.SequenceNode {
				defaults = slices.Clone(value.Content)
				slices.Reverse(defaults)
			}
			for _, mapping := range defaults {
				if mapping.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("YAML merge defaults must be mappings")
				}
				for j := 0; j < len(mapping.Content); j += 2 {
					put(mapping.Content[j], mapping.Content[j+1])
				}
			}
			continue
		}
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("YAML mapping keys must be scalars")
		}
		identity := yamlKeyIdentity(key)
		if seen[identity] {
			return nil, fmt.Errorf("duplicate YAML mapping key %q", key.Value)
		}
		seen[identity] = true
		explicit = append(explicit, key, value)
	}
	for i := 0; i < len(explicit); i += 2 {
		put(explicit[i], explicit[i+1])
	}
	return &result, nil
}

func yamlKeyIdentity(key *yaml.Node) string {
	key = metadataScalar(key)
	if key.Tag == "!!bool" || legacyYAMLBool(key) != "" {
		if strings.EqualFold(key.Value, "true") || strings.EqualFold(key.Value, "yes") || strings.EqualFold(key.Value, "on") || legacyYAMLBool(key) == "true" {
			return "number:1"
		}
		return "number:0"
	}
	if key.Tag == "!!int" || key.Tag == "!!float" {
		literal := strings.ReplaceAll(key.Value, "_", "")
		if key.Tag == "!!int" {
			if value, ok := new(big.Int).SetString(literal, 0); ok {
				literal = value.String()
			}
		}
		if value, ok := new(big.Rat).SetString(literal); ok {
			return "number:" + value.RatString()
		}
	}
	if key.Tag == "!!null" {
		return "null"
	}
	return key.Tag + ":" + key.Value
}

// Match key coercion in the existing YAML-to-JSON config path. Metadata object
// keys themselves remain strict strings; this only handles model entry keys.
func yamlConfigKey(key *yaml.Node) (string, error) {
	if key.Tag == "!!str" {
		return key.Value, nil
	}
	copy := *key
	if value := legacyYAMLBool(key); value != "" {
		copy.Tag, copy.Value = "!!bool", value
	}
	data, err := yaml.Marshal(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		&copy, {Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"},
	}})
	if err != nil {
		return "", err
	}
	data, err = sigsyaml.YAMLToJSON(data)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", err
	}
	for name := range fields {
		return name, nil
	}
	return "", fmt.Errorf("missing YAML mapping key")
}
