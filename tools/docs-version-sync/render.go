// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrCheckFailed = errors.New("docs version catalog is out of sync")

func Render(renderer string, catalog *Catalog) (string, error) {
	if err := ValidateCatalog(catalog); err != nil {
		return "", err
	}
	switch renderer {
	case "manifest-artifact-registry-paths":
		return renderManifestArtifactRegistryPaths(catalog)
	case "image-mirroring-resource-examples":
		return renderImageMirroringResourceExamples(catalog)
	case "image-mirroring-stack-snippet":
		return renderImageMirroringStackSnippet(catalog)
	case "image-mirroring-compute-stack-snippet":
		return renderImageMirroringComputeStackSnippet(catalog)
	case "image-mirroring-observability-stack-snippet":
		return renderImageMirroringObservabilityStackSnippet(catalog)
	case "image-mirroring-cli-snippet":
		return renderImageMirroringCLISnippet(catalog)
	default:
		return "", fmt.Errorf("unknown renderer %q", renderer)
	}
}

func renderImageMirroringResourceExamples(catalog *Catalog) (string, error) {
	stack := catalog.stackArtifact()
	stackPending := catalog.publicationIsPending(stack)
	ref := ""
	var err error
	if !stackPending {
		ref, err = catalog.resourceRef(stack)
		if err != nil {
			return "", err
		}
	}

	type supplementalStack struct {
		artifact   Artifact
		versionEnv string
		label      string
		pending    bool
		ref        string
	}
	var supplementalStacks []supplementalStack
	for _, config := range []struct {
		name       string
		versionEnv string
		label      string
	}{
		{name: computeStackResourceName, versionEnv: "COMPUTE_STACK_VERSION", label: "compute-plane"},
		{name: observabilityStackResourceName, versionEnv: "OBSERVABILITY_STACK_VERSION", label: "observability"},
	} {
		artifact, found := catalog.findArtifactByNameAndType(config.name, ArtifactTypeResource)
		if !found {
			continue
		}
		item := supplementalStack{
			artifact:   artifact,
			versionEnv: config.versionEnv,
			label:      config.label,
			pending:    catalog.publicationIsPending(artifact),
		}
		if !item.pending {
			item.ref, err = catalog.resourceRef(artifact)
			if err != nil {
				return "", err
			}
		}
		supplementalStacks = append(supplementalStacks, item)
	}

	var b strings.Builder
	b.WriteString("```bash\n")
	b.WriteString("# Set stack versions\n")
	b.WriteString(fmt.Sprintf("export STACK_VERSION=%q\n", stack.Version))
	for _, supplemental := range supplementalStacks {
		b.WriteString(fmt.Sprintf("export %s=%q\n", supplemental.versionEnv, supplemental.artifact.Version))
	}
	b.WriteString("\n")
	b.WriteString("# Download a specific control-plane stack version\n")
	if stackPending {
		b.WriteString(fmt.Sprintf("# Publication pending: %s %s is not yet available for download.\n", stack.Name, stack.Version))
	} else {
		refWithVersion := strings.Replace(ref, stack.Version, "${STACK_VERSION}", 1)
		b.WriteString("ngc registry resource download-version \\\n")
		b.WriteString(fmt.Sprintf("  %q\n", refWithVersion))
	}

	for _, supplemental := range supplementalStacks {
		b.WriteString(fmt.Sprintf("\n# Download a specific %s stack version\n", supplemental.label))
		if supplemental.pending {
			b.WriteString(fmt.Sprintf("# Publication pending: %s %s is not yet available for download.\n", supplemental.artifact.Name, supplemental.artifact.Version))
		} else {
			refWithVersion := strings.Replace(supplemental.ref, supplemental.artifact.Version, "${"+supplemental.versionEnv+"}", 1)
			b.WriteString("ngc registry resource download-version \\\n")
			b.WriteString(fmt.Sprintf("  %q\n", refWithVersion))
		}
	}

	b.WriteString("```\n")
	return b.String(), nil
}

func renderImageMirroringStackSnippet(catalog *Catalog) (string, error) {
	stack := catalog.stackArtifact()
	if catalog.publicationIsPending(stack) {
		return fmt.Sprintf("```bash\n# Publication pending: %s %s is not yet available for download.\n```\n", stack.Name, stack.Version), nil
	}
	ref, err := catalog.resourceRef(stack)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("```bash\n# Set the version\nexport VERSION=%q\n\nngc registry resource download-version %q && \\\n   mkdir -p %s && \\\n   tar -xzf %s_v${VERSION}/%s-${VERSION}.tar.gz -C %s && \\\n   rm -rf %s_v${VERSION}\n```\n",
		stack.Version,
		strings.Replace(ref, stack.Version, "${VERSION}", 1),
		stack.Name,
		stack.Name,
		stack.Name,
		stack.Name,
		stack.Name,
	), nil
}

func renderImageMirroringComputeStackSnippet(catalog *Catalog) (string, error) {
	return renderImageMirroringSupplementalStackSnippet(catalog, computeStackResourceName, "COMPUTE_VERSION")
}

func renderImageMirroringObservabilityStackSnippet(catalog *Catalog) (string, error) {
	return renderImageMirroringSupplementalStackSnippet(catalog, observabilityStackResourceName, "OBSERVABILITY_VERSION")
}

func renderImageMirroringSupplementalStackSnippet(catalog *Catalog, name, versionEnv string) (string, error) {
	artifact, ok := catalog.findArtifactByNameAndType(name, ArtifactTypeResource)
	if !ok {
		return "", fmt.Errorf("supplemental artifact %s is required", name)
	}
	if catalog.publicationIsPending(artifact) {
		return fmt.Sprintf("```bash\n# Publication pending: %s %s is not yet available for download.\n```\n", artifact.Name, artifact.Version), nil
	}
	ref, err := catalog.resourceRef(artifact)
	if err != nil {
		return "", err
	}
	versionRef := "${" + versionEnv + "}"
	return fmt.Sprintf("```bash\n# Set the version\nexport %s=%q\n\nngc registry resource download-version %q && \\\n   mkdir -p %s && \\\n   tar -xzf %s_v%s/%s-%s.tar.gz -C %s && \\\n   rm -rf %s_v%s\n```\n",
		versionEnv,
		artifact.Version,
		strings.Replace(ref, artifact.Version, versionRef, 1),
		artifact.Name,
		artifact.Name,
		versionRef,
		artifact.Name,
		versionRef,
		artifact.Name,
		artifact.Name,
		versionRef,
	), nil
}

func renderImageMirroringCLISnippet(catalog *Catalog) (string, error) {
	cli, ok := catalog.findArtifact("nvcf-cli")
	if !ok {
		return "", fmt.Errorf("supplemental artifact nvcf-cli is required")
	}
	if catalog.publicationIsPending(cli) {
		return fmt.Sprintf("```bash\n# Publication pending: %s %s is not yet available for download.\n```\n\nPackage contents and extraction instructions will be available after publication or mirroring.\n", cli.Name, cli.Version), nil
	}
	ref, err := catalog.resourceRef(cli)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("```bash\n# Set the version\nexport VERSION=%q\n\n# Set your platform (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64)\nexport PLATFORM=\"linux-amd64\"\n\nngc registry resource download-version %q\n\ntar -xzf nvcf-cli_v${VERSION}/${PLATFORM}/nvcf-cli-${PLATFORM}-${VERSION}.tar.gz\nmv nvcf-cli-${PLATFORM}-${VERSION} nvcf-cli\nchmod +x nvcf-cli/nvcf-cli\n```\n\nThe extracted directory contains:\n\n- `nvcf-cli` - The CLI binary\n- `.nvcf-cli.yaml.template` - Configuration template\n- `examples/` - Sample configuration files for different environments\n- `USAGE-GUIDE.md` - Detailed usage documentation\n",
		cli.Version,
		strings.Replace(ref, cli.Version, "${VERSION}", 1),
	), nil
}

func (catalog *Catalog) stackArtifact() Artifact {
	return Artifact{
		Name:     catalog.Stack.Name,
		Type:     ArtifactTypeResource,
		Registry: catalog.Stack.Registry,
		Version:  catalog.Stack.Version,
	}
}

func (catalog *Catalog) findArtifact(name string) (Artifact, bool) {
	if catalog.Stack.Name == name {
		return catalog.stackArtifact(), true
	}
	for _, artifact := range catalog.Artifacts {
		if artifact.Name == name {
			return artifact, true
		}
	}
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Name == name {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func (catalog *Catalog) findArtifactByNameAndType(name string, artifactType ArtifactType) (Artifact, bool) {
	for _, artifact := range catalog.findArtifacts(name) {
		if artifact.Type == artifactType {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func (catalog *Catalog) findArtifacts(name string) []Artifact {
	var artifacts []Artifact
	if catalog.Stack.Name == name {
		artifacts = append(artifacts, catalog.stackArtifact())
	}
	for _, artifact := range catalog.Artifacts {
		if artifact.Name == name {
			artifacts = append(artifacts, artifact)
		}
	}
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Name == name {
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts
}

func SyncDocs(repoRoot string, catalog *Catalog, check bool) error {
	if err := ValidateCatalog(catalog); err != nil {
		return err
	}
	var drifted []string
	for _, output := range catalog.Outputs {
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(output.Path))
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", output.Path, err)
		}
		updated := string(data)
		updated, changed, err := SyncInlineVersions(output.Path, updated, catalog)
		if err != nil {
			return fmt.Errorf("%s: %w", output.Path, err)
		}
		for _, block := range output.Blocks {
			rendered, err := Render(block.Renderer, catalog)
			if err != nil {
				return err
			}
			next, blockChanged, err := ReplaceMarkedBlock(updated, block.Marker, rendered)
			if err != nil {
				return fmt.Errorf("%s: %w", output.Path, err)
			}
			updated = next
			changed = changed || blockChanged
		}
		if !changed {
			continue
		}
		if check {
			drifted = append(drifted, output.Path)
			continue
		}
		if err := os.WriteFile(fullPath, []byte(updated), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", output.Path, err)
		}
	}
	if len(drifted) > 0 {
		return fmt.Errorf("%w: %s", ErrCheckFailed, strings.Join(drifted, ", "))
	}
	return nil
}

func ReplaceMarkedBlock(content, marker, rendered string) (string, bool, error) {
	syntax, beginIndex, searchStart, endIndex, err := findMarkedBlock(content, marker)
	if err != nil {
		return "", false, err
	}
	replacement := "\n\n" + strings.TrimRight(rendered, "\n") + "\n\n"
	if syntax.Legacy {
		current := markerSyntaxes(marker)[0]
		updated := content[:beginIndex] + current.Begin + replacement + current.End + content[endIndex+len(syntax.End):]
		return updated, updated != content, nil
	}
	updated := content[:searchStart] + replacement + content[endIndex:]
	return updated, updated != content, nil
}

type markerSyntax struct {
	Begin  string
	End    string
	Legacy bool
}

func findMarkedBlock(content, marker string) (markerSyntax, int, int, int, error) {
	var selected markerSyntax
	beginIndex := -1
	for _, syntax := range markerSyntaxes(marker) {
		index := strings.Index(content, syntax.Begin)
		if index < 0 {
			continue
		}
		if beginIndex >= 0 {
			return markerSyntax{}, 0, 0, 0, fmt.Errorf("duplicate marker %q", marker)
		}
		if nextBegin := strings.Index(content[index+len(syntax.Begin):], syntax.Begin); nextBegin >= 0 {
			return markerSyntax{}, 0, 0, 0, fmt.Errorf("duplicate marker %q", marker)
		}
		selected = syntax
		beginIndex = index
	}
	if beginIndex < 0 {
		return markerSyntax{}, 0, 0, 0, fmt.Errorf("missing begin marker for %q", marker)
	}
	searchStart := beginIndex + len(selected.Begin)
	relativeEnd := strings.Index(content[searchStart:], selected.End)
	if relativeEnd < 0 {
		return markerSyntax{}, 0, 0, 0, fmt.Errorf("missing end marker for %q", marker)
	}
	endIndex := searchStart + relativeEnd
	return selected, beginIndex, searchStart, endIndex, nil
}

func markerSyntaxes(marker string) []markerSyntax {
	return []markerSyntax{
		{
			Begin: fmt.Sprintf("{/* docs-version-sync:BEGIN %s */}", marker),
			End:   fmt.Sprintf("{/* docs-version-sync:END %s */}", marker),
		},
		{
			Begin: fmt.Sprintf("{/*docs-version-sync:BEGIN %s*/}", marker),
			End:   fmt.Sprintf("{/*docs-version-sync:END %s*/}", marker),
		},
		{
			Begin:  fmt.Sprintf("<!-- docs-version-sync:BEGIN %s -->", marker),
			End:    fmt.Sprintf("<!-- docs-version-sync:END %s -->", marker),
			Legacy: true,
		},
	}
}
