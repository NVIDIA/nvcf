// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type effectiveStackPin struct {
	artifact             string
	artifactType         ArtifactType
	path                 string
	blockPattern         string
	blockBoundaryPattern string
	pattern              string
	// derivedTag resolves the version when path formats the tag from a
	// template variable instead of a literal that pattern matches.
	derivedTag *derivedStackPinTag
}

// derivedStackPinTag locates a tag that a template formats from a variable
// bound to a values key, for example `printf "%s/pylon:%s" $host $routerImageTag`.
type derivedStackPinTag struct {
	// reference matches the formatted image reference and captures the
	// template variable that supplies the tag.
	reference string
	// valuesPath and valuesKey locate the default value that the variable
	// reads with `dig`.
	valuesPath string
	valuesKey  []string
}

// resolvedStackPin is one pin version and the source files that define it.
type resolvedStackPin struct {
	version string
	paths   []string
}

var effectiveStackPins = []effectiveStackPin{
	{
		artifact:             "helm-nvcf-llm-request-router",
		artifactType:         ArtifactTypeChart,
		path:                 "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: llm-request-router[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact:             "nvcf-gateway-routes",
		artifactType:         ArtifactTypeChart,
		path:                 "deploy/stacks/self-managed/helmfile.d/02-core.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: ingress[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact:     "pylon",
		artifactType: ArtifactTypeImage,
		path:         "deploy/stacks/self-managed/global.yaml.gotmpl",
		pattern:      `pylon:([0-9][^"\s]*)`,
		derivedTag: &derivedStackPinTag{
			reference:  `pylon:%s"[^\n]*\$(\w+)[ \t]*}}`,
			valuesPath: "deploy/stacks/self-managed/environments/base.yaml",
			valuesKey:  []string{"addons", "llm", "requestRouter", "image", "tag"},
		},
	},
}

func extractEffectiveStackPins(sources map[string][]byte, pins []effectiveStackPin) (map[string]string, error) {
	resolved, err := resolveEffectiveStackPins(sources, pins)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string, len(resolved))
	for artifact, pin := range resolved {
		versions[artifact] = pin.version
	}
	return versions, nil
}

// resolveEffectiveStackPins resolves every pin and requires each declared
// source to define at least one of them.
func resolveEffectiveStackPins(sources map[string][]byte, pins []effectiveStackPin) (map[string]resolvedStackPin, error) {
	resolved := make(map[string]resolvedStackPin, len(pins))
	matchedSources := make(map[string]struct{}, len(sources))
	for _, pin := range pins {
		resolvedPin, err := resolveEffectiveStackPin(sources, pin)
		if err != nil {
			return nil, err
		}
		if _, exists := resolved[pin.artifact]; exists {
			return nil, fmt.Errorf("duplicate pin matcher for artifact %s", pin.artifact)
		}
		resolved[pin.artifact] = resolvedPin
		for _, sourcePath := range resolvedPin.paths {
			matchedSources[sourcePath] = struct{}{}
		}
	}

	sourcePaths := make([]string, 0, len(sources))
	for sourcePath := range sources {
		sourcePaths = append(sourcePaths, sourcePath)
	}
	sort.Strings(sourcePaths)
	for _, sourcePath := range sourcePaths {
		if _, matched := matchedSources[sourcePath]; !matched {
			return nil, fmt.Errorf("declared pin source %s has no matcher", sourcePath)
		}
	}
	return resolved, nil
}

// resolveEffectiveStackPin reads one pin from its literal tag or, when the
// template formats the tag from a variable, from the values key it reads.
func resolveEffectiveStackPin(sources map[string][]byte, pin effectiveStackPin) (resolvedStackPin, error) {
	body, ok := sources[pin.path]
	if !ok {
		return resolvedStackPin{}, fmt.Errorf("pin source %s for %s is not declared", pin.path, pin.artifact)
	}
	matchBody, err := pin.targetBlock(body)
	if err != nil {
		return resolvedStackPin{}, err
	}
	matches := regexp.MustCompile(pin.pattern).FindAllSubmatch(matchBody, -1)
	var references [][][]byte
	if pin.derivedTag != nil {
		references = regexp.MustCompile(pin.derivedTag.reference).FindAllSubmatch(matchBody, -1)
	}
	switch {
	case len(matches) == 1 && len(references) == 0:
		if len(matches[0]) != 2 {
			return resolvedStackPin{}, fmt.Errorf("pin matcher for %s from %s must capture exactly one version", pin.artifact, pin.path)
		}
		return resolvedStackPin{version: string(matches[0][1]), paths: []string{pin.path}}, nil
	case len(matches) == 0 && len(references) == 1:
		return pin.resolveDerivedTag(sources, body, references[0])
	case pin.derivedTag != nil:
		return resolvedStackPin{}, fmt.Errorf("resolved %d literal and %d derived definitions for %s from %s; want exactly one literal tag or one tag derived from %s in %s",
			len(matches), len(references), pin.artifact, pin.path, strings.Join(pin.derivedTag.valuesKey, "."), pin.derivedTag.valuesPath)
	case pin.blockPattern != "":
		return resolvedStackPin{}, fmt.Errorf("resolved %d versions for %s within target block from %s; want exactly one", len(matches), pin.artifact, pin.path)
	default:
		return resolvedStackPin{}, fmt.Errorf("resolved %d definitions for %s from %s; want exactly one", len(matches), pin.artifact, pin.path)
	}
}

// targetBlock returns the part of body that the pin pattern searches. With a
// block pattern, it is the single matching block, ending at the next boundary
// match or the end of body. Without one, it is the whole body.
func (pin effectiveStackPin) targetBlock(body []byte) ([]byte, error) {
	if pin.blockPattern == "" {
		return body, nil
	}
	blocks := regexp.MustCompile(pin.blockPattern).FindAllIndex(body, -1)
	if len(blocks) != 1 {
		return nil, fmt.Errorf("resolved %d target blocks for %s from %s; want exactly one", len(blocks), pin.artifact, pin.path)
	}
	blockStart, blockEnd := blocks[0][0], len(body)
	if pin.blockBoundaryPattern != "" {
		remainderStart := blocks[0][1]
		if next := regexp.MustCompile(pin.blockBoundaryPattern).FindIndex(body[remainderStart:]); next != nil {
			blockEnd = remainderStart + next[0]
		}
	}
	return body[blockStart:blockEnd], nil
}

// resolveDerivedTag verifies that the captured template variable reads the
// configured values key exactly once, then reads that key's default value.
func (pin effectiveStackPin) resolveDerivedTag(sources map[string][]byte, templateBody []byte, reference [][]byte) (resolvedStackPin, error) {
	derived := pin.derivedTag
	valuesKey := strings.Join(derived.valuesKey, ".")
	if len(reference) != 2 {
		return resolvedStackPin{}, fmt.Errorf("derived tag matcher for %s from %s must capture exactly one template variable", pin.artifact, pin.path)
	}
	variable := string(reference[1])
	bindingPattern := `\$` + regexp.QuoteMeta(variable) + `[ \t]*:?=[ \t]*dig`
	for _, key := range derived.valuesKey {
		bindingPattern += `[ \t]+"` + regexp.QuoteMeta(key) + `"`
	}
	bindingPattern += `[ \t]+"[^"]*"[ \t]+\.Values\b`
	bindings := regexp.MustCompile(bindingPattern).FindAll(templateBody, -1)
	if len(bindings) != 1 {
		return resolvedStackPin{}, fmt.Errorf("resolved %d bindings of $%s to %s for %s from %s; want exactly one", len(bindings), variable, valuesKey, pin.artifact, pin.path)
	}
	valuesBody, ok := sources[derived.valuesPath]
	if !ok {
		return resolvedStackPin{}, fmt.Errorf("pin source %s for %s is not declared; %s derives the tag from %s", derived.valuesPath, pin.artifact, pin.path, valuesKey)
	}
	version, err := yamlScalarAtPath(valuesBody, derived.valuesKey)
	if err != nil {
		return resolvedStackPin{}, fmt.Errorf("resolve %s tag from %s in %s: %w", pin.artifact, valuesKey, derived.valuesPath, err)
	}
	return resolvedStackPin{version: version, paths: []string{pin.path, derived.valuesPath}}, nil
}

// yamlScalarAtPath returns the single non-empty scalar at keyPath.
func yamlScalarAtPath(body []byte, keyPath []string) (string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(body, &document); err != nil {
		return "", err
	}
	if len(document.Content) == 0 {
		return "", fmt.Errorf("%s is not set", strings.Join(keyPath, "."))
	}
	node := document.Content[0]
	for depth, key := range keyPath {
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("%s is not set", strings.Join(keyPath, "."))
		}
		var values []*yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				values = append(values, node.Content[i+1])
			}
		}
		if len(values) == 0 {
			return "", fmt.Errorf("%s is not set", strings.Join(keyPath, "."))
		}
		if len(values) > 1 {
			return "", fmt.Errorf("resolved %d definitions of %s; want exactly one", len(values), strings.Join(keyPath[:depth+1], "."))
		}
		node = values[0]
	}
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node.Kind != yaml.ScalarNode || node.Tag == "!!null" || node.Value == "" || strings.ContainsAny(node.Value, " \t\r\n") {
		return "", fmt.Errorf("%s must be a non-empty tag", strings.Join(keyPath, "."))
	}
	return node.Value, nil
}

// effectiveStackPinSourcesUsed lists the source files that define the pins.
func effectiveStackPinSourcesUsed(sources map[string][]byte, pins []effectiveStackPin) ([]string, error) {
	resolved, err := resolveEffectiveStackPins(sources, pins)
	if err != nil {
		return nil, err
	}
	unique := make(map[string]struct{}, len(sources))
	for _, pin := range resolved {
		for _, sourcePath := range pin.paths {
			unique[sourcePath] = struct{}{}
		}
	}
	paths := make([]string, 0, len(unique))
	for sourcePath := range unique {
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)
	return paths, nil
}

// effectiveStackPinRequiredSourcePaths adds the values file of each derived
// tag whose template reference appears in the primary pin sources.
func effectiveStackPinRequiredSourcePaths(primary map[string][]byte, pins []effectiveStackPin) []string {
	unique := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		unique[pin.path] = struct{}{}
		if pin.derivedTag == nil {
			continue
		}
		body, err := pin.targetBlock(primary[pin.path])
		if err != nil {
			continue
		}
		if regexp.MustCompile(pin.derivedTag.reference).Match(body) {
			unique[pin.derivedTag.valuesPath] = struct{}{}
		}
	}
	paths := make([]string, 0, len(unique))
	for sourcePath := range unique {
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)
	return paths
}

// loadEffectiveStackPinSnapshot reads the primary pin sources from a stack
// release, plus any values file that a derived tag reads at that release.
func loadEffectiveStackPinSnapshot(repoRoot string, release stackSourceRelease, pins []effectiveStackPin) (stackSourceSnapshot, error) {
	primaryPaths := effectiveStackPinSourcePaths(pins)
	snapshot, err := loadStackSourceSnapshot(repoRoot, release, primaryPaths)
	if err != nil {
		return stackSourceSnapshot{}, err
	}
	sourcePaths := effectiveStackPinRequiredSourcePaths(snapshot.Files, pins)
	if len(sourcePaths) == len(primaryPaths) {
		return snapshot, nil
	}
	return loadStackSourceSnapshot(repoRoot, release, sourcePaths)
}

func pinSourceDigest(sources map[string][]byte, pins []effectiveStackPin) (string, error) {
	resolved, err := resolveEffectiveStackPins(sources, pins)
	if err != nil {
		return "", err
	}
	pins = append([]effectiveStackPin(nil), pins...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].artifact < pins[j].artifact })

	digest := sha256.New()
	for _, pin := range pins {
		resolvedPin, ok := resolved[pin.artifact]
		if !ok {
			continue
		}
		digest.Write([]byte(pin.artifact))
		digest.Write([]byte{0})
		for _, sourcePath := range resolvedPin.paths {
			digest.Write([]byte(sourcePath))
			digest.Write([]byte{0})
		}
		digest.Write([]byte(resolvedPin.version))
		digest.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// validateStackSourceSnapshot verifies the catalog against its immutable stack release inputs.
func validateStackSourceSnapshot(repoRoot string, catalog *Catalog) error {
	pins, err := effectiveStackPinsForSources(catalog.Stack.PinSources)
	if err != nil {
		return err
	}
	return validateStackSourceSnapshotWithPins(repoRoot, catalog, pins)
}

func effectiveStackPinsForSources(sourcePaths []string) ([]effectiveStackPin, error) {
	known := append([]effectiveStackPin(nil), effectiveStackPins...)
	knownPaths := make(map[string]struct{}, len(known))
	for _, pin := range known {
		knownPaths[pin.path] = struct{}{}
		if pin.derivedTag != nil {
			knownPaths[pin.derivedTag.valuesPath] = struct{}{}
		}
	}
	selectedPaths := make(map[string]struct{}, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		_, ok := knownPaths[sourcePath]
		if !ok {
			return nil, fmt.Errorf("stack source snapshot declares unsupported pin source %s", sourcePath)
		}
		selectedPaths[sourcePath] = struct{}{}
	}
	pins := make([]effectiveStackPin, 0, len(known))
	for _, pin := range known {
		if _, selected := selectedPaths[pin.path]; selected {
			pins = append(pins, pin)
		}
	}
	return pins, nil
}

// validateStackSourceSnapshotWithPins verifies catalog artifacts with a supplied pin definition set.
func validateStackSourceSnapshotWithPins(repoRoot string, catalog *Catalog, pins []effectiveStackPin) error {
	if catalog.Stack.SourceCommit == "" {
		return nil
	}

	snapshot, err := loadStackSourceSnapshot(repoRoot, stackSourceRelease{
		Version: catalog.Stack.Version,
		Tag:     catalog.Stack.SourceTag,
		Commit:  catalog.Stack.SourceCommit,
	}, catalog.Stack.PinSources)
	if err != nil {
		return err
	}

	versions, err := extractEffectiveStackPins(snapshot.Files, pins)
	if err != nil {
		return err
	}
	digest, err := pinSourceDigest(snapshot.Files, pins)
	if err != nil {
		return err
	}
	if digest != catalog.Stack.PinSourceDigest {
		return fmt.Errorf("effective stack pins have digest %s, want release snapshot %s for stack version %s at %s", digest, catalog.Stack.PinSourceDigest, catalog.Stack.Version, catalog.Stack.SourceCommit)
	}
	for _, pin := range pins {
		want := versions[pin.artifact]
		artifact, ok := catalog.findArtifactByNameAndType(pin.artifact, pin.artifactType)
		if !ok {
			return fmt.Errorf("catalog is missing stack-pinned artifact %s", pin.artifact)
		}
		if artifact.Version != want {
			return fmt.Errorf("catalog %s version is %s, want effective stack pin %s from %s", pin.artifact, artifact.Version, want, pin.path)
		}
	}
	return nil
}

func gitOutput(repoRoot string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return nil, fmt.Errorf("git %v: %s", args, trimOutput(exitErr.Stderr))
	}
	return nil, err
}

func trimOutput(output []byte) string {
	return strings.TrimSpace(string(output))
}
