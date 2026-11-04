// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// imageMirrorMapping binds an effective source identity to its published mirror.
type imageMirrorMapping struct {
	Source      string
	Destination string
}

var imagePlatformDigest = regexp.MustCompile("^sha256:[0-9a-f]{64}$")

func decodeImageMirrorMappings(raw []byte) (map[string]imageMirrorMapping, error) {
	var document struct {
		Images map[string]imageMirrorMapping
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode image mirror mapping: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("mirror mapping must contain exactly one YAML document")
	}
	if len(document.Images) == 0 {
		return nil, fmt.Errorf("mirror mapping requires a non-empty images map")
	}
	return document.Images, nil
}

func generateImageMirrorOverrides(images []catalogImage, raw []byte, verify func(string, string) error) (map[string]any, error) {
	mappings, err := decodeImageMirrorMappings(raw)
	if err != nil {
		return nil, err
	}
	if len(mappings) != len(images) {
		return nil, fmt.Errorf("mapping must cover exactly the %d catalog identities", len(images))
	}
	output := make(map[string]any)
	verified := make(map[string]bool)
	for _, image := range images {
		mapping, ok := mappings[image.ID]
		if !ok || mapping.Source != image.Reference {
			return nil, fmt.Errorf("image %s (%s): mapping source must match %s", image.ID, image.Path, image.Reference)
		}
		destination, err := parseResolvedImageReference(mapping.Destination)
		if err != nil {
			return nil, fmt.Errorf("image %s destination: %w", image.ID, err)
		}
		host, repository, hasHost := strings.Cut(destination.Repository, "/")
		if !hasHost || !(strings.ContainsAny(host, ".:") || host == "localhost") {
			return nil, fmt.Errorf("image %s requires a fully qualified mirror destination", image.ID)
		}
		key := mapping.Source + "\x00" + mapping.Destination
		if !verified[key] {
			if err := verify(mapping.Source, mapping.Destination); err != nil {
				return nil, fmt.Errorf("image %s: %w", image.ID, err)
			}
			verified[key] = true
		}
		fields := map[string]any{"repository": destination.Repository, "tag": destination.Version}
		if image.split {
			fields["registry"], fields["repository"] = host, repository
		}
		if image.digest {
			fields["digest"] = destination.Digest
			// Clear cert-manager's legacy prefix so a full repository does not
			// become legacy.example/mirror.example/repository.
			fields["registry"] = ""
		} else if destination.Digest != "" {
			if destination.Version == "" {
				return nil, fmt.Errorf("image %s requires tag@digest for its tag-only chart input", image.ID)
			}
			fields["tag"] = destination.Version + "@" + destination.Digest
		}
		if err := setImageCatalogOverride(output, image.Path, fields); err != nil {
			return nil, err
		}
	}
	return output, nil
}

func setImageCatalogOverride(values map[string]any, path string, fields map[string]any) error {
	parts := strings.Split(path, ".")
	for _, key := range parts[:len(parts)-1] {
		if values[key] == nil {
			values[key] = make(map[string]any)
		}
		child, ok := values[key].(map[string]any)
		if !ok {
			return fmt.Errorf("overlapping image catalog path %s", path)
		}
		values = child
	}
	key := parts[len(parts)-1]
	if values[key] != nil {
		return fmt.Errorf("overlapping image catalog path %s", path)
	}
	values[key] = fields
	return nil
}

func checkImageMirrorDestinations(source, mirrored []catalogImage, raw []byte) error {
	mappings, err := decodeImageMirrorMappings(raw)
	if err != nil {
		return err
	}
	if len(source) != len(mirrored) {
		return fmt.Errorf("mirror overrides changed catalog coverage")
	}
	for _, image := range mirrored {
		if image.Reference != mappings[image.ID].Destination {
			return fmt.Errorf("image %s (%s) resolved to %s instead of mapped destination %s", image.ID, image.Path, image.Reference, mappings[image.ID].Destination)
		}
	}
	return nil
}

func verifyImageMirror(source, destination, certDirectory string) error {
	sourcePlatforms, err := imageMirrorPlatforms(source, certDirectory)
	if err != nil {
		return err
	}
	destinationPlatforms, err := imageMirrorPlatforms(destination, certDirectory)
	if err != nil {
		return err
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if sourcePlatforms[platform] == "" || destinationPlatforms[platform] != sourcePlatforms[platform] {
			return fmt.Errorf("%s source/mirror platform digests differ or are missing", platform)
		}
	}
	return nil
}

func imageMirrorPlatforms(reference, certDirectory string) (map[string]string, error) {
	args := []string{"inspect", "--raw"}
	if certDirectory != "" {
		args = append(args, "--cert-dir", certDirectory)
	}
	raw, err := imageCatalogCommand("skopeo", append(args, "docker://"+reference)...)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", reference, err)
	}
	return decodeImageMirrorPlatforms(raw)
}

func decodeImageMirrorPlatforms(raw []byte) (map[string]string, error) {
	var index struct {
		Manifests []struct {
			Digest   string
			Platform struct {
				OS           string
				Architecture string
			}
		}
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("decode image platform index: %w", err)
	}
	result := make(map[string]string)
	for _, manifest := range index.Manifests {
		platform := manifest.Platform.OS + "/" + manifest.Platform.Architecture
		if platform != "linux/amd64" && platform != "linux/arm64" {
			continue
		}
		if !imagePlatformDigest.MatchString(manifest.Digest) || result[platform] != "" {
			return nil, fmt.Errorf("invalid or ambiguous platform digest for %s", platform)
		}
		result[platform] = manifest.Digest
	}
	if len(result) != 2 {
		return nil, fmt.Errorf("image index must include linux/amd64 and linux/arm64")
	}
	return result, nil
}
