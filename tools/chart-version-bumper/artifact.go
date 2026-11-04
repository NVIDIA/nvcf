// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	releasePlaceholder  = "${release}"
	upstreamPlaceholder = "${upstream}"
)

// ArtifactVersion describes how a service release version becomes the version
// embedded in the artifact it publishes. Most services need no declaration:
// their artifact version is the release version. Wrapper images can combine an
// upstream version from the released source tree with their release version.
type ArtifactVersion struct {
	SourceFile    string `json:"source_file"`
	SourcePattern string `json:"source_pattern"`
	Format        string `json:"format"`
}

// Release identifies a published service version and the artifact version a
// chart must consume. Version remains the release version so chart semver
// follows the released wrapper, not an upstream version embedded in its tag.
type Release struct {
	ServiceID       string
	Version         string
	ArtifactVersion string
	entry           Entry
}

// ReleaseForTag resolves a release tag and, when declared, derives its artifact
// version from the exact source tree the tag names.
func (m *Metadata) ReleaseForTag(root, tag string) (Release, error) {
	serviceID, version, err := m.ServiceForTag(tag)
	if err != nil {
		return Release{}, err
	}
	var entry Entry
	for _, candidate := range m.Services {
		if candidate.ID == serviceID {
			entry = candidate
			break
		}
	}
	release := Release{ServiceID: serviceID, Version: version, ArtifactVersion: version, entry: entry}
	if entry.ArtifactVersion == nil {
		return release, nil
	}
	artifact, err := entry.ArtifactVersion.render(root, tag, entry.Path, version)
	if err != nil {
		return Release{}, fmt.Errorf("resolve artifact version for %s: %w", serviceID, err)
	}
	release.ArtifactVersion = artifact
	return release, nil
}

func (a ArtifactVersion) render(root, tag, servicePath, version string) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	upstream := ""
	if strings.Contains(a.Format, upstreamPlaceholder) {
		cleanSource := filepath.Clean(a.SourceFile)
		if filepath.IsAbs(a.SourceFile) || cleanSource == "." || cleanSource == ".." || strings.HasPrefix(cleanSource, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("source_file %q must name a relative file inside the service path", a.SourceFile)
		}
		path := filepath.Join(servicePath, cleanSource)
		content, err := exec.Command("git", "-C", root, "show", tag+":"+filepath.ToSlash(path)).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("read %s from tag %s: %w: %s", path, tag, err, strings.TrimSpace(string(content)))
		}
		re := regexp.MustCompile(a.SourcePattern)
		matches := re.FindAllSubmatch(content, -1)
		index := re.SubexpIndex("upstream")
		if len(matches) != 1 || index < 0 || index >= len(matches[0]) || len(matches[0][index]) == 0 {
			return "", fmt.Errorf("source_pattern must match %s exactly once with a non-empty upstream capture", path)
		}
		upstream = string(matches[0][index])
	}
	artifact := strings.Replace(a.Format, releasePlaceholder, version, 1)
	artifact = strings.Replace(artifact, upstreamPlaceholder, upstream, 1)
	return artifact, nil
}

func (a ArtifactVersion) validate() error {
	if strings.Count(a.Format, releasePlaceholder) != 1 {
		return fmt.Errorf("format must contain %s exactly once", releasePlaceholder)
	}
	usesUpstream := strings.Contains(a.Format, upstreamPlaceholder)
	if strings.Count(a.Format, upstreamPlaceholder) > 1 {
		return fmt.Errorf("format may contain %s at most once", upstreamPlaceholder)
	}
	if usesUpstream != (a.SourceFile != "" && a.SourcePattern != "") {
		return fmt.Errorf("source_file and source_pattern are required exactly when format uses %s", upstreamPlaceholder)
	}
	if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(a.Format, releasePlaceholder, ""), upstreamPlaceholder, ""), "${") {
		return fmt.Errorf("format contains an unknown placeholder")
	}
	if a.SourcePattern != "" {
		re, err := regexp.Compile(a.SourcePattern)
		if err != nil {
			return fmt.Errorf("compile source_pattern: %w", err)
		}
		if re.SubexpIndex("upstream") < 0 {
			return fmt.Errorf("source_pattern must define a named upstream capture")
		}
	}
	return nil
}

// releaseVersionFromArtifact recovers the service release version from a
// chart's current artifact tag. This keeps the workflow's major/minor/patch
// decision based on the wrapper release even when the tag also embeds an
// upstream version.
func (a ArtifactVersion) releaseVersionFromArtifact(artifact string) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	pattern := regexp.QuoteMeta(a.Format)
	pattern = strings.Replace(pattern, regexp.QuoteMeta(upstreamPlaceholder), `.+?`, 1)
	pattern = strings.Replace(pattern, regexp.QuoteMeta(releasePlaceholder), `(?P<release>.+?)`, 1)
	re := regexp.MustCompile("^" + pattern + "$")
	match := re.FindStringSubmatch(artifact)
	index := re.SubexpIndex("release")
	if match == nil || index < 0 || match[index] == "" {
		return "", fmt.Errorf("artifact version %q does not match format %q", artifact, a.Format)
	}
	return match[index], nil
}

func (r Release) currentReleaseVersion(artifact string) (string, error) {
	if r.entry.ArtifactVersion == nil {
		return artifact, nil
	}
	return r.entry.ArtifactVersion.releaseVersionFromArtifact(artifact)
}
