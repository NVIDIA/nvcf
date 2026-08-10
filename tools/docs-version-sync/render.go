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
	case "manifest-deployment-resources":
		return renderManifestDeploymentResources(catalog)
	case "image-mirroring-resource-examples":
		return renderImageMirroringResourceExamples(catalog)
	case "image-mirroring-stack-snippet":
		return renderImageMirroringStackSnippet(catalog)
	case "image-mirroring-compute-stack-snippet":
		return renderImageMirroringComputeStackSnippet(catalog)
	case "image-mirroring-cli-snippet":
		return renderImageMirroringCLISnippet(catalog)
	default:
		return "", fmt.Errorf("unknown renderer %q", renderer)
	}
}

func renderManifestDeploymentResources(catalog *Catalog) (string, error) {
	resources := catalog.resourceArtifacts()
	var b strings.Builder
	b.WriteString("| Type | Component Name | Full Path |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, artifact := range resources {
		path, err := catalog.artifactPath(artifact)
		if err != nil {
			return "", err
		}
		b.WriteString(fmt.Sprintf("| Resource | %s | `%s` |\n", artifact.Name, path))
	}
	return b.String(), nil
}

func renderImageMirroringResourceExamples(catalog *Catalog) (string, error) {
	stack := catalog.stackArtifact()
	ref, err := catalog.resourceRef(stack)
	if err != nil {
		return "", err
	}

	compute, hasComputeStack := catalog.findArtifact(computeStackResourceName)
	computeRef := ""
	if hasComputeStack {
		if compute.Type != ArtifactTypeResource {
			return "", fmt.Errorf("%s must be a resource artifact", computeStackResourceName)
		}
		computeRef, err = catalog.resourceRef(compute)
		if err != nil {
			return "", err
		}
	}

	refWithVersion := strings.Replace(ref, stack.Version, "${STACK_VERSION}", 1)

	var b strings.Builder
	b.WriteString("```bash\n")
	b.WriteString("# Set stack versions\n")
	b.WriteString(fmt.Sprintf("export STACK_VERSION=%q\n", stack.Version))
	if hasComputeStack {
		b.WriteString(fmt.Sprintf("export COMPUTE_STACK_VERSION=%q\n", compute.Version))
	}
	b.WriteString("\n")
	b.WriteString("# Download a specific control-plane stack version\n")
	b.WriteString("ngc registry resource download-version \\\n")
	b.WriteString(fmt.Sprintf("  %q\n", refWithVersion))

	if hasComputeStack {
		computeRefWithVersion := strings.Replace(computeRef, compute.Version, "${COMPUTE_STACK_VERSION}", 1)
		b.WriteString("\n# Download a specific compute-plane stack version\n")
		b.WriteString("ngc registry resource download-version \\\n")
		b.WriteString(fmt.Sprintf("  %q\n", computeRefWithVersion))
	}

	b.WriteString("```\n")
	return b.String(), nil
}

func renderImageMirroringStackSnippet(catalog *Catalog) (string, error) {
	stack := catalog.stackArtifact()
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
	compute, ok := catalog.findArtifact(computeStackResourceName)
	if !ok {
		return "", fmt.Errorf("supplemental artifact %s is required", computeStackResourceName)
	}
	if compute.Type != ArtifactTypeResource {
		return "", fmt.Errorf("%s must be a resource artifact", computeStackResourceName)
	}
	ref, err := catalog.resourceRef(compute)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("```bash\n# Set the version\nexport COMPUTE_VERSION=%q\n\nngc registry resource download-version %q && \\\n   mkdir -p %s && \\\n   tar -xzf %s_v${COMPUTE_VERSION}/%s-${COMPUTE_VERSION}.tar.gz -C %s && \\\n   rm -rf %s_v${COMPUTE_VERSION}\n```\n",
		compute.Version,
		strings.Replace(ref, compute.Version, "${COMPUTE_VERSION}", 1),
		compute.Name,
		compute.Name,
		compute.Name,
		compute.Name,
		compute.Name,
	), nil
}

func renderImageMirroringCLISnippet(catalog *Catalog) (string, error) {
	cli, ok := catalog.findArtifact("nvcf-cli")
	if !ok {
		return "", fmt.Errorf("supplemental artifact nvcf-cli is required")
	}
	ref, err := catalog.resourceRef(cli)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("```bash\n# Set the version\nexport VERSION=%q\n\n# Set your platform (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64)\nexport PLATFORM=\"linux-amd64\"\n\nngc registry resource download-version %q\n\ntar -xzf nvcf-cli_v${VERSION}/${PLATFORM}/nvcf-cli-${PLATFORM}-${VERSION}.tar.gz\nmv nvcf-cli-${PLATFORM}-${VERSION} nvcf-cli\nchmod +x nvcf-cli/nvcf-cli\n```\n",
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

func (catalog *Catalog) artifactTypeLabel(artifact Artifact) string {
	switch artifact.Type {
	case ArtifactTypeChart:
		if publication, ok := catalog.publicationFor(artifact); ok && publication.ChartFormat == ChartFormatHTTP {
			return "Chart (HTTP)"
		}
		return "Chart (OCI)"
	case ArtifactTypeResource:
		return "Resource"
	default:
		return "Image"
	}
}

func (catalog *Catalog) resourceArtifacts() []Artifact {
	denylist := catalog.DenylistMap()
	var resources []Artifact
	if _, denied := denylist[catalog.Stack.Name]; !denied {
		resources = append(resources, catalog.stackArtifact())
	}
	for _, artifact := range catalog.Artifacts {
		if artifact.Type != ArtifactTypeResource {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if artifact.Name == catalog.Stack.Name {
			continue
		}
		resources = append(resources, artifact)
	}
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Type != ArtifactTypeResource {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		resources = append(resources, artifact)
	}
	return resources
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

func (catalog *Catalog) uncategorizedArtifacts(used map[string]struct{}) []Artifact {
	denylist := catalog.DenylistMap()
	var artifacts []Artifact
	for _, artifact := range catalog.Artifacts {
		if artifact.Type == ArtifactTypeResource {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if _, ok := used[artifact.catalogKey()]; ok {
			continue
		}
		artifacts = append(artifacts, artifact)
	}
	for _, artifact := range catalog.SupplementalArtifacts {
		if artifact.Type == ArtifactTypeResource {
			continue
		}
		if _, denied := denylist[artifact.Name]; denied {
			continue
		}
		if _, ok := used[artifact.catalogKey()]; ok {
			continue
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func (catalog *Catalog) isDenied(name string) bool {
	_, denied := catalog.DenylistMap()[name]
	return denied
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
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
			Begin:  fmt.Sprintf("<!-- docs-version-sync:BEGIN %s -->", marker),
			End:    fmt.Sprintf("<!-- docs-version-sync:END %s -->", marker),
			Legacy: true,
		},
	}
}
