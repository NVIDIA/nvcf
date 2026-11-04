// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// umbrellaPreparation contains explicit reviewed inputs; it never selects latest releases.
type umbrellaPreparation struct {
	Source        string
	Version       string
	Change        string
	Qualification string
	Toolchain     umbrellaToolchain
	StackVersions map[string]string
}

func runUmbrella(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: docs-version-sync umbrella <prepare|check|bundle|verify-bundle|manifest|publish|docs-prepare|docs-revise|docs-mode|docs-stage> [flags]")
	}
	command := args[0]
	if command == "docs-mode" {
		return runDocsMode(args[1:])
	}
	if command == "docs-stage" {
		return runDocsStage(args[1:])
	}
	if strings.HasPrefix(command, "docs-") {
		return runUmbrellaDocs(command, args[1:])
	}
	if command == "publish" {
		return runUmbrellaPublish(args[1:])
	}
	allowed := map[string]string{
		"prepare":       "repo source version change qualification helm-version helmfile-version self-managed-version compute-plane-version observability-version previous inventories out",
		"check":         "repo manifest previous inventories",
		"bundle":        "repo manifest previous inventories out",
		"verify-bundle": "repo manifest bundle sha256",
		"manifest":      "manifest",
	}
	fields, ok := allowed[command]
	if !ok {
		return fmt.Errorf("unknown umbrella command %q", command)
	}
	flags := flag.NewFlagSet("umbrella "+command, flag.ContinueOnError)
	rootFlag := flags.String("repo", "", "repository containing reviewed commits and stack tags")
	manifestPath := flags.String("manifest", "", "umbrella manifest to validate")
	previousPath := flags.String("previous", "", "preceding qualified umbrella manifest")
	inventories := flags.String("inventories", "", "directory containing the three released inventory JSON files")
	out := flags.String("out", "", "manifest file or new bundle directory; existing output must match exactly")
	bundle := flags.String("bundle", "", "umbrella archive to verify")
	digest := flags.String("sha256", "", "expected archive digest from the trusted release asset index")
	plan := umbrellaPreparation{StackVersions: map[string]string{}}
	flags.StringVar(&plan.Source, "source", "", "full reviewed source commit (also supplies the catalog in legacy mode)")
	flags.StringVar(&plan.Version, "version", "", "exact umbrella version X.Y.Z")
	flags.StringVar(&plan.Change, "change", "", "initial, major, minor, or patch")
	flags.StringVar(&plan.Qualification, "qualification", "", "public review URL approving the complete combination; omit for a candidate")
	flags.StringVar(&plan.Toolchain.Helm, "helm-version", "", "exact supported Helm 3 version")
	flags.StringVar(&plan.Toolchain.Helmfile, "helmfile-version", "", "exact supported Helmfile 1.1 version")
	for _, name := range releaseSetStackNames {
		key, _ := stackKeyForPlane(name)
		flags.Func(key+"-version", "exact released stack version X.Y.Z; supply all three to select release-local pins", func(version string) error {
			if _, exists := plan.StackVersions[name]; exists {
				return fmt.Errorf("%s version was specified more than once", key)
			}
			plan.StackVersions[name] = version
			return nil
		})
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected umbrella arguments")
	}
	var invalid string
	flags.Visit(func(f *flag.Flag) {
		if !strings.Contains(" "+fields+" ", " "+f.Name+" ") {
			invalid = f.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("--%s is not valid for umbrella %s", invalid, command)
	}
	if command == "manifest" {
		if *manifestPath == "" {
			return fmt.Errorf("umbrella manifest requires --manifest")
		}
		_, _, err := loadUmbrellaManifest(*manifestPath)
		return err
	}
	if command == "verify-bundle" {
		if *bundle == "" || *manifestPath == "" || *digest == "" {
			return fmt.Errorf("umbrella verify-bundle requires --bundle, --manifest, and --sha256")
		}
		return verifyUmbrellaBundleFile(resolveRepoPath(*rootFlag, *bundle), resolveRepoPath(*rootFlag, *manifestPath), *digest)
	}
	root := *rootFlag
	var err error
	if root == "" {
		root, err = findRepoRoot()
		if err != nil {
			return err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if *inventories == "" {
		return fmt.Errorf("umbrella %s requires --inventories with all three exact released inventories", command)
	}
	inventoryDir := resolveRepoPath(root, *inventories)
	previous, err := loadPreviousUmbrella(root, *previousPath)
	if err != nil {
		return err
	}
	if command == "prepare" {
		if *out == "" {
			return fmt.Errorf("umbrella prepare requires --out")
		}
		return prepareUmbrellaManifest(root, inventoryDir, resolveRepoPath(root, *out), previous, plan)
	}
	if *manifestPath == "" {
		return fmt.Errorf("umbrella %s requires --manifest", command)
	}
	manifest, data, err := loadUmbrellaManifest(resolveRepoPath(root, *manifestPath))
	if err != nil {
		return err
	}
	if command == "bundle" {
		if *out == "" {
			return fmt.Errorf("umbrella bundle requires --out")
		}
		return assembleUmbrellaBundle(root, inventoryDir, resolveRepoPath(root, *out), manifest, data, previous)
	}
	if err := checkUmbrellaInputs(root, inventoryDir, manifest, data, previous); err != nil {
		return err
	}
	fmt.Printf("Validated umbrella %s (%s), manifest %s\n", manifest.Version, manifest.Status, umbrellaDigest(data))
	return nil
}

func loadPreviousUmbrella(root, path string) (*umbrellaManifest, error) {
	if path == "" {
		return nil, nil
	}
	previous, data, err := loadUmbrellaManifest(resolveRepoPath(root, path))
	if err != nil {
		return nil, err
	}
	if err := validatePublishedUmbrella(root, previous, data); err != nil {
		return nil, err
	}
	return previous, nil
}

func prepareUmbrellaManifest(root, inventoryDir, out string, previous *umbrellaManifest, plan umbrellaPreparation) error {
	if !fullLowercaseCommitSHARe.MatchString(plan.Source) {
		return fmt.Errorf("umbrella prepare requires a full reviewed --source commit SHA")
	}
	var catalog *Catalog
	if len(plan.StackVersions) == 0 {
		var err error
		catalog, err = catalogAtCommit(root, plan.Source)
		if err != nil {
			return err
		}
		if err := validateEditionSources(root, catalog); err != nil {
			return err
		}
	} else if err := validateUmbrellaStackSelections(plan.StackVersions); err != nil {
		return err
	}
	manifest := &umbrellaManifest{
		SchemaVersion: 1, Version: plan.Version, Change: plan.Change,
		Status: ReleaseSetDevelopment, Qualification: plan.Qualification,
		SourceCommit: plan.Source, Toolchain: plan.Toolchain, Stacks: map[string]umbrellaStack{},
	}
	if catalog == nil {
		manifest.SchemaVersion = 2
	}
	if previous != nil {
		manifest.PreviousVersion = previous.Version
	}
	if plan.Qualification != "" {
		manifest.Status = ReleaseSetQualified
	}
	for _, name := range releaseSetStackNames {
		key, _ := stackKeyForPlane(name)
		spec, _ := stackInventorySpecByKey(key)
		stack := umbrellaStack{InventoryAsset: spec.AssetName}
		if catalog != nil {
			metadata, _ := catalog.ReleaseSet.Stacks.byName(name)
			if metadata.InventoryAsset != spec.AssetName {
				return fmt.Errorf("reviewed %s catalog must select inventory %s", name, spec.AssetName)
			}
			stack.Version, stack.SourceTag, stack.SourceCommit = metadata.Version, metadata.SourceTag, metadata.SourceCommit
			for _, compatibility := range catalog.Compatibility {
				if compatibility.Stack == name && compatibility.Version == metadata.Version {
					stack.CompatibleWith = compatibility.CompatibleWith
				}
			}
		} else {
			stack.Version = plan.StackVersions[name]
			stack.SourceTag = spec.TagPrefix + stack.Version
			commit, err := gitOutput(root, "rev-parse", "--verify", "refs/tags/"+stack.SourceTag+"^{commit}")
			if err != nil {
				return fmt.Errorf("resolve released %s tag: %w", name, err)
			}
			stack.SourceCommit = trimOutput(commit)
			stack.CompatibleWith = map[string]string{}
			for _, peer := range releaseSetStackNames {
				if peer != name {
					stack.CompatibleWith[peer] = plan.StackVersions[peer]
				}
			}
		}
		data, err := os.ReadFile(filepath.Join(inventoryDir, spec.AssetName))
		if err != nil {
			return err
		}
		stack.InventorySHA256 = umbrellaDigest(data)
		manifest.Stacks[name] = stack
	}
	data, err := marshalUmbrellaManifest(manifest)
	if err != nil {
		return err
	}
	if err := checkUmbrellaInputs(root, inventoryDir, manifest, data, previous); err != nil {
		return err
	}
	if err := writeImmutableUmbrellaFile(out, data); err != nil {
		return err
	}
	fmt.Printf("Prepared umbrella %s (%s) at %s. Tag: %s. No docs configuration or release was changed.\n", manifest.Version, manifest.Status, out, umbrellaTag(manifest.Version))
	return nil
}

func validateUmbrellaStackSelections(versions map[string]string) error {
	if len(versions) != len(releaseSetStackNames) {
		return fmt.Errorf("release-local preparation requires --self-managed-version, --compute-plane-version, and --observability-version together")
	}
	for _, name := range releaseSetStackNames {
		if !validReleaseLocalVersion(versions[name]) {
			return fmt.Errorf("release-local %s version must be an exact stable X.Y.Z", name)
		}
	}
	return nil
}

func checkUmbrellaInputs(root, inventoryDir string, manifest *umbrellaManifest, data []byte, previous *umbrellaManifest) error {
	if err := validateUmbrellaTransition(previous, manifest); err != nil {
		return err
	}
	if _, err := validateUmbrellaSources(root, manifest, inventoryDir); err != nil {
		return err
	}
	return validatePublishedUmbrella(root, manifest, data)
}
