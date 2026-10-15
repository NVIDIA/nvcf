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
)

type effectiveStackPin struct {
	artifact             string
	artifactType         ArtifactType
	path                 string
	blockPattern         string
	blockBoundaryPattern string
	pattern              string
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
	},
	{
		artifact:             "helm-nvca-operator",
		artifactType:         ArtifactTypeChart,
		path:                 "deploy/stacks/nvcf-compute-plane/helmfile.d/02-nvca.yaml.gotmpl",
		blockPattern:         `(?m)^  - name: nvca-operator[ \t]*$`,
		blockBoundaryPattern: `(?m)^  -[ \t]+`,
		pattern:              `(?m)^    version:[ \t]*"?([^"\s]+)"?[ \t]*$`,
	},
	{
		artifact:             "nvca",
		artifactType:         ArtifactTypeImage,
		path:                 "deploy/stacks/nvcf-compute-plane/environments/base.yaml",
		blockPattern:         `(?m)^  nvcaOperator:[ \t]*$`,
		blockBoundaryPattern: `(?m)^  [A-Za-z0-9_-]+:[ \t]*`,
		pattern:              `(?m)^      nvcaVersion:[ \t]*"([^"]+)"[ \t]*$`,
	},
}

func extractEffectiveStackPins(sources map[string][]byte, pins []effectiveStackPin) (map[string]string, error) {
	versions := make(map[string]string, len(pins))
	matchedSources := make(map[string]struct{}, len(sources))
	for _, pin := range pins {
		body, ok := sources[pin.path]
		if !ok {
			return nil, fmt.Errorf("pin source %s for %s is not declared", pin.path, pin.artifact)
		}
		matchBody := body
		if pin.blockPattern != "" {
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
			matchBody = body[blockStart:blockEnd]
		}
		matches := regexp.MustCompile(pin.pattern).FindAllSubmatch(matchBody, -1)
		if len(matches) != 1 {
			if pin.blockPattern != "" {
				return nil, fmt.Errorf("resolved %d versions for %s within target block from %s; want exactly one", len(matches), pin.artifact, pin.path)
			}
			return nil, fmt.Errorf("resolved %d definitions for %s from %s; want exactly one", len(matches), pin.artifact, pin.path)
		}
		if len(matches[0]) != 2 {
			return nil, fmt.Errorf("pin matcher for %s from %s must capture exactly one version", pin.artifact, pin.path)
		}
		if _, exists := versions[pin.artifact]; exists {
			return nil, fmt.Errorf("duplicate pin matcher for artifact %s", pin.artifact)
		}
		versions[pin.artifact] = string(matches[0][1])
		matchedSources[pin.path] = struct{}{}
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
	return versions, nil
}

func pinSourceDigest(sources map[string][]byte, pins []effectiveStackPin) (string, error) {
	versions, err := extractEffectiveStackPins(sources, pins)
	if err != nil {
		return "", err
	}
	pins = append([]effectiveStackPin(nil), pins...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].artifact < pins[j].artifact })

	digest := sha256.New()
	for _, pin := range pins {
		version, ok := versions[pin.artifact]
		if !ok {
			continue
		}
		digest.Write([]byte(pin.artifact))
		digest.Write([]byte{0})
		digest.Write([]byte(pin.path))
		digest.Write([]byte{0})
		digest.Write([]byte(version))
		digest.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// validateStackSourceSnapshot verifies the catalog against its immutable stack release inputs.
func validateStackSourceSnapshot(repoRoot string, catalog *Catalog) error {
	return validateStackSourceSnapshotWithPins(repoRoot, catalog, effectiveStackPins)
}

// validateStackSourceSnapshotWithPins verifies catalog artifacts with a supplied pin definition set.
func validateStackSourceSnapshotWithPins(repoRoot string, catalog *Catalog, pins []effectiveStackPin) error {
	if catalog.Stack.SourceCommit == "" {
		return nil
	}

	snapshot, err := loadStackSourceSnapshot(repoRoot, stackSourceRelease{
		Version: catalog.Stack.SourceVersion,
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
		return fmt.Errorf("effective stack pins have digest %s, want release snapshot %s for source version %s at %s", digest, catalog.Stack.PinSourceDigest, catalog.Stack.SourceVersion, catalog.Stack.SourceCommit)
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
