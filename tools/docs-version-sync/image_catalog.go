// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// catalogImage connects a stable image identity to its effective chart values.
type catalogImage struct {
	ID        string
	Path      string
	Ownership string
	Reference string
	split     bool
	digest    bool
}

// imageChartSnapshot contains Helm-coalesced values and dependency versions.
type imageChartSnapshot struct {
	Values      map[string]any
	AppVersions map[string]string
	Manifest    []byte
}

const imageCatalogProbeName = "nvcf-image-catalog-probe.yaml"

const imageCatalogProbe = "{{- $versions := dict \"\" .Chart.AppVersion -}}\n" +
	"{{- range $name, $chart := .Subcharts -}}\n" +
	"{{- $_ := set $versions $name $chart.Chart.AppVersion -}}\n{{- end }}\n" +
	"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nvcf-image-catalog-probe\n" +
	"data:\n  values: {{ toJson .Values | quote }}\n" +
	"  appVersions: {{ toJson $versions | quote }}\n"

func runImageCatalog(args []string) error {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "mirror") {
		return fmt.Errorf("usage: images inspect|mirror --chart DIR [-f VALUES] [--mapping FILE]")
	}
	flags := flag.NewFlagSet("images "+args[0], flag.ContinueOnError)
	chart := flags.String("chart", "", "unpacked chart, with dependencies built")
	mapping := flags.String("mapping", "", "source/destination image mapping for mirror")
	certDirectory := flags.String("registry-cert-dir", "", "skopeo registry CA/client certificate directory")
	var values []string
	flags.Func("f", "values file; repeat in Helm precedence order", func(value string) error {
		absolute, err := filepath.Abs(value)
		values = append(values, absolute)
		return err
	})
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *chart == "" || flags.NArg() != 0 || (args[0] == "inspect" && (*mapping != "" || *certDirectory != "")) ||
		(args[0] == "mirror" && *mapping == "") {
		return fmt.Errorf("specify --chart and valid images %s flags", args[0])
	}
	snapshot, err := renderImageChart(*chart, values)
	if err != nil {
		return err
	}
	images, err := resolveChartImageCatalog(snapshot)
	if err != nil {
		return err
	}
	if err := checkChartImageCoverage(images, snapshot.Manifest); err != nil {
		return err
	}
	if args[0] == "inspect" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(images)
	}
	raw, err := os.ReadFile(*mapping)
	if err != nil {
		return fmt.Errorf("read --mapping: %w", err)
	}
	overrides, err := generateImageMirrorOverrides(images, raw, func(source, destination string) error {
		return verifyImageMirror(source, destination, *certDirectory)
	})
	if err != nil {
		return err
	}
	// Verify that the actual templates honor every generated override before
	// writing any output. A chart-specific fallback must never escape the mirror.
	temporary, err := os.CreateTemp("", "nvcf-image-overrides-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	data, err := yaml.Marshal(overrides)
	if err != nil {
		temporary.Close()
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	mirrored, err := renderImageChart(*chart, append(values, temporary.Name()))
	if err != nil {
		return fmt.Errorf("render mirrored chart: %w", err)
	}
	expected, err := resolveChartImageCatalog(mirrored)
	if err != nil {
		return err
	}
	if err := checkChartImageCoverage(expected, mirrored.Manifest); err != nil {
		return fmt.Errorf("mirrored chart: %w", err)
	}
	if err := checkImageMirrorDestinations(images, expected, raw); err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func renderImageChart(chart string, values []string) (imageChartSnapshot, error) {
	directory, err := os.MkdirTemp("", "nvcf-image-catalog-")
	if err != nil {
		return imageChartSnapshot{}, err
	}
	defer os.RemoveAll(directory)
	copyPath := filepath.Join(directory, "chart")
	if err := copyResolvedInventoryStack(chart, copyPath); err != nil {
		return imageChartSnapshot{}, fmt.Errorf("copy image chart: %w", err)
	}
	probePath := filepath.Join(copyPath, "templates", imageCatalogProbeName)
	if _, err := os.Stat(probePath); !os.IsNotExist(err) {
		return imageChartSnapshot{}, fmt.Errorf("chart reserves %s", probePath)
	}
	if err := os.MkdirAll(filepath.Dir(probePath), 0o700); err != nil {
		return imageChartSnapshot{}, err
	}
	if err := os.WriteFile(probePath, []byte(imageCatalogProbe), 0o600); err != nil {
		return imageChartSnapshot{}, err
	}
	args := []string{"template", "image-catalog", copyPath, "--namespace", "image-catalog"}
	for _, value := range values {
		args = append(args, "-f", value)
	}
	raw, err := imageCatalogCommand("helm", args...)
	if err != nil {
		return imageChartSnapshot{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err != nil {
			return imageChartSnapshot{}, fmt.Errorf("image catalog probe missing from Helm output: %w", err)
		}
		metadata, _ := document["metadata"].(map[string]any)
		if document["kind"] != "ConfigMap" || metadata["name"] != "nvcf-image-catalog-probe" {
			continue
		}
		data, _ := document["data"].(map[string]any)
		snapshot := imageChartSnapshot{Manifest: raw}
		if err := json.Unmarshal([]byte(fmt.Sprint(data["values"])), &snapshot.Values); err != nil {
			return imageChartSnapshot{}, fmt.Errorf("decode computed image values: %w", err)
		}
		if err := json.Unmarshal([]byte(fmt.Sprint(data["appVersions"])), &snapshot.AppVersions); err != nil {
			return imageChartSnapshot{}, fmt.Errorf("decode image app versions: %w", err)
		}
		return snapshot, nil
	}
}

func imageCatalogCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func resolveChartImageCatalog(snapshot imageChartSnapshot) ([]catalogImage, error) {
	var images []catalogImage
	identities := make(map[string]bool)
	for _, category := range []string{"thirdPartyImages", "ownedImages"} {
		entries, ok := snapshot.Values[category].(map[string]any)
		if snapshot.Values[category] != nil && !ok {
			return nil, fmt.Errorf("%s must be a map of image value paths", category)
		}
		for id, entry := range entries {
			path, ok := entry.(string)
			if !ok || path == "" || identities[id] {
				return nil, fmt.Errorf("%s.%s must be a unique identity referencing existing image values", category, id)
			}
			identities[id] = true
			node, err := imageCatalogValue(snapshot.Values, path)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", category, id, err)
			}
			image, err := resolveCatalogImage(snapshot, path, node)
			if err != nil {
				return nil, fmt.Errorf("%s.%s (%s): %w", category, id, path, err)
			}
			image.ID, image.Path, image.Ownership = id, path, category
			images = append(images, image)
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("chart must declare thirdPartyImages or ownedImages")
	}
	sort.Slice(images, func(i, j int) bool { return images[i].ID < images[j].ID })
	return images, nil
}

func imageCatalogValue(values map[string]any, path string) (map[string]any, error) {
	var node any = values
	for _, key := range strings.Split(path, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid image values path %q", path)
		}
		node, ok = object[key]
		if !ok || key == "" {
			return nil, fmt.Errorf("missing image values path %q", path)
		}
	}
	object, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("image values path %q must identify an object", path)
	}
	return object, nil
}

func imageCatalogString(node map[string]any, key string) string {
	value, _ := node[key].(string)
	return value
}

func resolveCatalogImage(snapshot imageChartSnapshot, path string, node map[string]any) (catalogImage, error) {
	registry := imageCatalogString(node, "registry")
	repository := imageCatalogString(node, "repository")
	tag := imageCatalogString(node, "tag")
	digest := imageCatalogString(node, "digest")
	_, split := node["registry"]
	fullRepository := false
	// cert-manager composes missing repository/tag fields from subchart values
	// and appVersion. Keep those defaults owned by the vendored dependency.
	root := strings.Split(path, ".")[0]
	parent, _ := snapshot.Values[root].(map[string]any)
	if _, hasImageRegistry := parent["imageRegistry"]; hasImageRegistry {
		fullRepository = true
		if repository == "" {
			repository = strings.Trim(imageCatalogString(parent, "imageRegistry")+"/"+imageCatalogString(parent, "imageNamespace"), "/") + "/" + imageCatalogString(node, "name")
		}
		if tag == "" && digest == "" {
			tag = snapshot.AppVersions[root]
		}
	}
	if registry != "" {
		repository = registry + "/" + repository
	}
	reference := repository
	if tag != "" {
		reference += ":" + tag
	}
	if digest != "" {
		reference += "@" + digest
	}
	if _, err := parseResolvedImageReference(reference); err != nil {
		return catalogImage{}, err
	}
	if repository == "" || strings.HasSuffix(repository, "/") {
		return catalogImage{}, fmt.Errorf("repository must be supplied in effective chart values")
	}
	return catalogImage{Reference: reference, split: split && !fullRepository, digest: fullRepository}, nil
}

func checkChartImageCoverage(catalog []catalogImage, manifest []byte) error {
	declared := make(map[string]bool)
	for _, image := range catalog {
		declared[image.Reference] = true
	}
	actual, err := extractResolvedImages(manifest)
	if err != nil {
		return err
	}
	for _, image := range actual {
		if !declared[image.Reference] {
			return fmt.Errorf("rendered image %s is absent from the chart image catalog", image.Reference)
		}
	}
	return nil
}
