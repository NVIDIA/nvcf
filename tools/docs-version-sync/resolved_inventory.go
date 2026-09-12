// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const resolvedStackInventorySchemaVersion = 1

var (
	resolvedStackPlanes = []string{"compute-plane", "control-plane", "observability"}
	imageArgumentRe     = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9_.-]*-image=(.*)$`)
	imageDigestRe       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+.-]*:[A-Za-z0-9=_+.-]+$`)
)

type resolvedStackInventory struct {
	SchemaVersion int                         `json:"schema_version"`
	Source        stackSourceRelease          `json:"source"`
	Releases      []resolvedInventoryRelease  `json:"releases"`
	Artifacts     []resolvedInventoryArtifact `json:"artifacts"`
}

type resolvedInventoryRelease struct {
	Plane     string `json:"plane"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Required  bool   `json:"required"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
}

type resolvedInventoryArtifact struct {
	Type       string                   `json:"type"`
	Name       string                   `json:"name"`
	Repository string                   `json:"repository"`
	Version    string                   `json:"version,omitempty"`
	Digest     string                   `json:"digest,omitempty"`
	Reference  string                   `json:"reference"`
	Sources    []resolvedArtifactSource `json:"sources"`
}

type resolvedArtifactSource struct {
	Plane   string `json:"plane"`
	Release string `json:"release"`
}

type resolvedInventoryPlaneInput struct {
	Name              string
	ReleaseList       []byte
	ManifestByRelease map[string][]byte
}

type helmfileRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Enabled   bool   `json:"enabled"`
	Installed bool   `json:"installed"`
	Labels    string `json:"labels"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
}

type parsedImageReference struct {
	Name       string
	Repository string
	Version    string
	Digest     string
	Reference  string
}

// generateResolvedStackInventory builds an inventory from Helmfile list output and manifests
// rendered with the same resolved values. Callers must supply a manifest entry for every release,
// including disabled optional and resource-only releases.
func generateResolvedStackInventory(source stackSourceRelease, planes []resolvedInventoryPlaneInput) (resolvedStackInventory, error) {
	if err := validateStackSourceRelease(source); err != nil {
		return resolvedStackInventory{}, err
	}
	orderedPlanes, err := normalizeResolvedInventoryPlanes(planes)
	if err != nil {
		return resolvedStackInventory{}, err
	}

	inventory := resolvedStackInventory{
		SchemaVersion: resolvedStackInventorySchemaVersion,
		Source:        source,
	}
	artifacts := make(map[string]*resolvedInventoryArtifact)
	for _, plane := range orderedPlanes {
		releases, err := parseHelmfileReleaseList(plane.ReleaseList)
		if err != nil {
			return resolvedStackInventory{}, fmt.Errorf("parse %s Helmfile releases: %w", plane.Name, err)
		}
		if err := validateRenderedReleaseSet(plane.Name, releases, plane.ManifestByRelease); err != nil {
			return resolvedStackInventory{}, err
		}

		for _, release := range releases {
			sourceRef := resolvedArtifactSource{Plane: plane.Name, Release: release.Name}
			chartArtifact, err := resolvedChartArtifact(release, sourceRef)
			if err != nil {
				return resolvedStackInventory{}, fmt.Errorf("resolve chart for %s release %s: %w", plane.Name, release.Name, err)
			}
			mergeResolvedArtifact(artifacts, chartArtifact)

			images, err := extractResolvedImages(plane.ManifestByRelease[release.Name])
			if err != nil {
				return resolvedStackInventory{}, fmt.Errorf("resolve images for %s release %s: %w", plane.Name, release.Name, err)
			}
			for _, image := range images {
				mergeResolvedArtifact(artifacts, resolvedInventoryArtifact{
					Type:       "container-image",
					Name:       image.Name,
					Repository: image.Repository,
					Version:    image.Version,
					Digest:     image.Digest,
					Reference:  image.Reference,
					Sources:    []resolvedArtifactSource{sourceRef},
				})
			}

			inventory.Releases = append(inventory.Releases, resolvedInventoryRelease{
				Plane:     plane.Name,
				Name:      release.Name,
				Namespace: release.Namespace,
				Required:  release.Enabled && release.Installed,
				Chart:     release.Chart,
				Version:   release.Version,
			})
		}
	}

	for _, artifact := range artifacts {
		sort.Slice(artifact.Sources, func(i, j int) bool {
			return compareResolvedArtifactSources(artifact.Sources[i], artifact.Sources[j]) < 0
		})
		inventory.Artifacts = append(inventory.Artifacts, *artifact)
	}
	sort.Slice(inventory.Releases, func(i, j int) bool {
		return compareResolvedInventoryReleases(inventory.Releases[i], inventory.Releases[j]) < 0
	})
	sort.Slice(inventory.Artifacts, func(i, j int) bool {
		return compareResolvedInventoryArtifacts(inventory.Artifacts[i], inventory.Artifacts[j]) < 0
	})

	if err := validateResolvedStackInventory(inventory); err != nil {
		return resolvedStackInventory{}, err
	}
	return inventory, nil
}

func normalizeResolvedInventoryPlanes(planes []resolvedInventoryPlaneInput) ([]resolvedInventoryPlaneInput, error) {
	if len(planes) == 0 {
		return nil, fmt.Errorf("resolved inventory requires at least one plane")
	}
	planes = append([]resolvedInventoryPlaneInput(nil), planes...)
	sort.Slice(planes, func(i, j int) bool { return planes[i].Name < planes[j].Name })
	for i, plane := range planes {
		if !isResolvedStackPlane(plane.Name) {
			return nil, fmt.Errorf("resolved inventory plane %d is unknown: %q", i, plane.Name)
		}
		if i > 0 && planes[i-1].Name == plane.Name {
			return nil, fmt.Errorf("resolved inventory plane %q is duplicated", plane.Name)
		}
	}
	return planes, nil
}

func parseHelmfileReleaseList(raw []byte) ([]helmfileRelease, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var releases []helmfileRelease
	if err := decoder.Decode(&releases); err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("release list cannot be empty")
	}

	seen := make(map[string]struct{}, len(releases))
	for _, release := range releases {
		if strings.TrimSpace(release.Name) == "" || release.Name != strings.TrimSpace(release.Name) {
			return nil, fmt.Errorf("release name %q must be non-empty and trimmed", release.Name)
		}
		if _, ok := seen[release.Name]; ok {
			return nil, fmt.Errorf("duplicate Helmfile release %s", release.Name)
		}
		seen[release.Name] = struct{}{}
		if !release.Installed {
			return nil, fmt.Errorf("release %s is not installed", release.Name)
		}
		if _, _, _, err := resolvedChartReference(release.Chart, release.Version); err != nil {
			return nil, fmt.Errorf("release %s: %w", release.Name, err)
		}
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].Name < releases[j].Name })
	return releases, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected data after JSON value")
}

func validateRenderedReleaseSet(plane string, releases []helmfileRelease, manifests map[string][]byte) error {
	if manifests == nil {
		return fmt.Errorf("%s rendered manifests cannot be nil", plane)
	}
	want := make(map[string]struct{}, len(releases))
	for _, release := range releases {
		want[release.Name] = struct{}{}
		manifest, ok := manifests[release.Name]
		if !ok {
			return fmt.Errorf("%s release %s has no rendered manifest entry", plane, release.Name)
		}
		if len(bytes.TrimSpace(manifest)) == 0 {
			return fmt.Errorf("%s release %s has empty rendered manifests", plane, release.Name)
		}
	}
	for release := range manifests {
		if _, ok := want[release]; !ok {
			return fmt.Errorf("%s rendered manifest has unknown release %s", plane, release)
		}
	}
	return nil
}

func resolvedChartArtifact(release helmfileRelease, source resolvedArtifactSource) (resolvedInventoryArtifact, error) {
	repository, name, reference, err := resolvedChartReference(release.Chart, release.Version)
	if err != nil {
		return resolvedInventoryArtifact{}, err
	}
	return resolvedInventoryArtifact{
		Type:       "helm-chart",
		Name:       name,
		Repository: repository,
		Version:    release.Version,
		Reference:  reference,
		Sources:    []resolvedArtifactSource{source},
	}, nil
}

func resolvedChartReference(chart, version string) (string, string, string, error) {
	if strings.TrimSpace(chart) == "" || chart != strings.TrimSpace(chart) || strings.ContainsAny(chart, "{}$") {
		return "", "", "", fmt.Errorf("chart %q is unresolved", chart)
	}
	if strings.TrimSpace(version) == "" || version != strings.TrimSpace(version) || strings.ContainsAny(version, "{}$") || version == "latest" {
		return "", "", "", fmt.Errorf("chart %s has unresolved version %q", chart, version)
	}
	clean := strings.TrimSuffix(chart, "/")
	name := path.Base(clean)
	repository := strings.TrimSuffix(clean, "/"+name)
	if name == "." || name == "/" || name == "" || repository == "" {
		return "", "", "", fmt.Errorf("chart %q must include a repository and name", chart)
	}
	return repository, name, chart + "@" + version, nil
}

func extractResolvedImages(manifest []byte) ([]parsedImageReference, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	images := make(map[string]parsedImageReference)
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := collectResolvedImages(&document, images); err != nil {
			return nil, err
		}
	}

	resolved := make([]parsedImageReference, 0, len(images))
	for _, image := range images {
		resolved = append(resolved, image)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Reference < resolved[j].Reference })
	return resolved, nil
}

func collectResolvedImages(node *yaml.Node, images map[string]parsedImageReference) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if isResolvedImageField(key.Value) && value.Kind == yaml.ScalarNode {
				if value.Tag == "!!null" {
					return fmt.Errorf("field %s has no resolved image reference", key.Value)
				}
				image, err := parseResolvedImageReference(value.Value)
				if err != nil {
					return fmt.Errorf("field %s: %w", key.Value, err)
				}
				images[image.Reference] = image
			}
			if key.Value == "args" || key.Value == "command" {
				if err := collectResolvedImageArguments(value, images); err != nil {
					return err
				}
			}
			if err := collectResolvedImages(value, images); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := collectResolvedImages(child, images); err != nil {
			return err
		}
	}
	return nil
}

func collectResolvedImageArguments(node *yaml.Node, images map[string]parsedImageReference) error {
	if node.Kind == yaml.ScalarNode {
		if reference, ok := resolvedImageArgument(node.Value); ok {
			image, err := parseResolvedImageReference(reference)
			if err != nil {
				return fmt.Errorf("image argument %q: %w", node.Value, err)
			}
			images[image.Reference] = image
		}
		return nil
	}
	for _, child := range node.Content {
		if err := collectResolvedImageArguments(child, images); err != nil {
			return err
		}
	}
	return nil
}

func resolvedImageArgument(value string) (string, bool) {
	matches := imageArgumentRe.FindStringSubmatch(value)
	if matches == nil {
		return "", false
	}
	return matches[1], true
}

func isResolvedImageField(key string) bool {
	return key == "image" || strings.HasPrefix(key, "release-artifact-") && strings.HasSuffix(key, "-image")
}

func parseResolvedImageReference(reference string) (parsedImageReference, error) {
	if reference == "" || reference != strings.TrimSpace(reference) || strings.ContainsAny(reference, "{}$") || strings.Contains(reference, "://") {
		return parsedImageReference{}, fmt.Errorf("image reference %q is unresolved", reference)
	}

	nameAndTag, digest, hasDigest := strings.Cut(reference, "@")
	if hasDigest {
		if strings.Contains(digest, "@") || !imageDigestRe.MatchString(digest) {
			return parsedImageReference{}, fmt.Errorf("image reference %q has invalid digest", reference)
		}
	}
	lastSlash := strings.LastIndex(nameAndTag, "/")
	lastColon := strings.LastIndex(nameAndTag, ":")
	repository := nameAndTag
	version := ""
	if lastColon > lastSlash {
		repository = nameAndTag[:lastColon]
		version = nameAndTag[lastColon+1:]
	}
	if repository == "" || strings.HasSuffix(repository, "/") {
		return parsedImageReference{}, fmt.Errorf("image reference %q has no repository", reference)
	}
	if version == "" && !hasDigest {
		return parsedImageReference{}, fmt.Errorf("image reference %q has no tag or digest", reference)
	}
	if version == "latest" && !hasDigest {
		return parsedImageReference{}, fmt.Errorf("image reference %q uses unresolved latest tag", reference)
	}
	name := path.Base(repository)
	if name == "." || name == "/" || name == "" {
		return parsedImageReference{}, fmt.Errorf("image reference %q has no repository name", reference)
	}
	return parsedImageReference{
		Name:       name,
		Repository: repository,
		Version:    version,
		Digest:     digest,
		Reference:  reference,
	}, nil
}

func mergeResolvedArtifact(artifacts map[string]*resolvedInventoryArtifact, candidate resolvedInventoryArtifact) {
	key := candidate.Type + "\x00" + candidate.Reference
	existing, ok := artifacts[key]
	if !ok {
		copy := candidate
		artifacts[key] = &copy
		return
	}
	for _, source := range existing.Sources {
		if source == candidate.Sources[0] {
			return
		}
	}
	existing.Sources = append(existing.Sources, candidate.Sources[0])
}

func validateResolvedStackInventory(inventory resolvedStackInventory) error {
	if inventory.SchemaVersion != resolvedStackInventorySchemaVersion {
		return fmt.Errorf("resolved inventory schema version is %d, want %d", inventory.SchemaVersion, resolvedStackInventorySchemaVersion)
	}
	if err := validateStackSourceRelease(inventory.Source); err != nil {
		return err
	}
	if len(inventory.Releases) == 0 {
		return fmt.Errorf("resolved inventory has no releases")
	}
	if len(inventory.Artifacts) == 0 {
		return fmt.Errorf("resolved inventory has no artifacts")
	}

	releases := make(map[string]resolvedInventoryRelease, len(inventory.Releases))
	for i, release := range inventory.Releases {
		if i > 0 && compareResolvedInventoryReleases(inventory.Releases[i-1], release) >= 0 {
			return fmt.Errorf("resolved inventory releases are not uniquely sorted")
		}
		if !isResolvedStackPlane(release.Plane) {
			return fmt.Errorf("release %s has unknown plane %s", release.Name, release.Plane)
		}
		key := resolvedReleaseKey(release.Plane, release.Name)
		releases[key] = release
	}

	chartSources := make(map[string]struct{}, len(inventory.Releases))
	seenArtifacts := make(map[string]struct{}, len(inventory.Artifacts))
	for i, artifact := range inventory.Artifacts {
		if i > 0 && compareResolvedInventoryArtifacts(inventory.Artifacts[i-1], artifact) >= 0 {
			return fmt.Errorf("resolved inventory artifacts are not uniquely sorted")
		}
		key := artifact.Type + "\x00" + artifact.Reference
		if _, ok := seenArtifacts[key]; ok {
			return fmt.Errorf("duplicate resolved artifact %s", artifact.Reference)
		}
		seenArtifacts[key] = struct{}{}
		if len(artifact.Sources) == 0 {
			return fmt.Errorf("artifact %s has no release sources", artifact.Reference)
		}
		for sourceIndex, source := range artifact.Sources {
			if sourceIndex > 0 && compareResolvedArtifactSources(artifact.Sources[sourceIndex-1], source) >= 0 {
				return fmt.Errorf("artifact %s sources are not uniquely sorted", artifact.Reference)
			}
			release, ok := releases[resolvedReleaseKey(source.Plane, source.Release)]
			if !ok {
				return fmt.Errorf("artifact %s references unknown release %s/%s", artifact.Reference, source.Plane, source.Release)
			}
			if artifact.Type == "helm-chart" && artifact.Reference == release.Chart+"@"+release.Version {
				chartSources[resolvedReleaseKey(source.Plane, source.Release)] = struct{}{}
			}
		}
		if err := validateResolvedInventoryArtifact(artifact); err != nil {
			return err
		}
	}
	for key := range releases {
		if _, ok := chartSources[key]; !ok {
			return fmt.Errorf("release %s has no matching chart artifact", key)
		}
	}
	return nil
}

func validateResolvedInventoryArtifact(artifact resolvedInventoryArtifact) error {
	switch artifact.Type {
	case "helm-chart":
		repository, name, reference, err := resolvedChartReference(artifact.Repository+"/"+artifact.Name, artifact.Version)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", artifact.Reference, err)
		}
		if artifact.Repository != repository || artifact.Name != name || artifact.Reference != reference || artifact.Digest != "" {
			return fmt.Errorf("chart artifact %s has inconsistent resolved fields", artifact.Reference)
		}
	case "container-image":
		image, err := parseResolvedImageReference(artifact.Reference)
		if err != nil {
			return err
		}
		if artifact.Name != image.Name {
			return fmt.Errorf("image artifact %s has stale repository name %q, want %q", artifact.Reference, artifact.Name, image.Name)
		}
		if artifact.Repository != image.Repository || artifact.Version != image.Version || artifact.Digest != image.Digest {
			return fmt.Errorf("image artifact %s has inconsistent resolved fields", artifact.Reference)
		}
	default:
		return fmt.Errorf("artifact %s has unknown type %q", artifact.Reference, artifact.Type)
	}
	return nil
}

func marshalResolvedStackInventory(inventory resolvedStackInventory) ([]byte, error) {
	if err := validateResolvedStackInventory(inventory); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func parseResolvedStackInventory(raw []byte) (resolvedStackInventory, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var inventory resolvedStackInventory
	if err := decoder.Decode(&inventory); err != nil {
		return resolvedStackInventory{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return resolvedStackInventory{}, err
	}
	if err := validateResolvedStackInventory(inventory); err != nil {
		return resolvedStackInventory{}, err
	}
	return inventory, nil
}

func isResolvedStackPlane(plane string) bool {
	index := sort.SearchStrings(resolvedStackPlanes, plane)
	return index < len(resolvedStackPlanes) && resolvedStackPlanes[index] == plane
}

func resolvedReleaseKey(plane, release string) string {
	return plane + "/" + release
}

func compareResolvedInventoryReleases(left, right resolvedInventoryRelease) int {
	if left.Plane != right.Plane {
		return strings.Compare(left.Plane, right.Plane)
	}
	return strings.Compare(left.Name, right.Name)
}

func compareResolvedInventoryArtifacts(left, right resolvedInventoryArtifact) int {
	if left.Type != right.Type {
		return strings.Compare(left.Type, right.Type)
	}
	return strings.Compare(left.Reference, right.Reference)
}

func compareResolvedArtifactSources(left, right resolvedArtifactSource) int {
	if left.Plane != right.Plane {
		return strings.Compare(left.Plane, right.Plane)
	}
	return strings.Compare(left.Release, right.Release)
}
