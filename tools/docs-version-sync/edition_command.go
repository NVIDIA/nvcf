// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// editionPreparation records deterministic preparation inputs and the resulting source tree.
type editionPreparation struct {
	Source          string `json:"source"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version"`
	Change          string `json:"change"`
	SelfManaged     string `json:"self_managed"`
	ComputePlane    string `json:"compute_plane"`
	Observability   string `json:"observability"`
	Navigation      string `json:"navigation"`
	Qualification   string `json:"qualification"`
	Digest          string `json:"digest,omitempty"`
}

func runEdition(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: docs-version-sync edition <prepare|check|register|manifest|links> [flags]")
	}
	command := args[0]
	flags := flag.NewFlagSet("edition "+command, flag.ContinueOnError)
	rootFlag := flags.String("repo", "", "repository root; defaults to the current checkout")
	version := flags.String("version", "", "exact docs edition X.Y.Z")
	source := flags.String("source", "", "full reviewed source commit SHA")
	previous := flags.String("previous-version", "", "preceding registered docs edition")
	change := flags.String("change", "", "initial, major, minor, or patch")
	selfManaged := flags.String("self-managed", "", "exact self-managed stack version")
	compute := flags.String("compute-plane", "", "exact compute-plane stack version")
	obs := flags.String("observability", "", "exact observability stack version")
	nav := flags.String("navigation", "navigation.yml", "branch-local navigation path under fern/")
	qualification := flags.String("qualification", "", "public review URL recording whole-combination approval; omit for an unqualified candidate")
	out := flags.String("out", "", "new preparation directory outside the repository")
	commit := flags.String("commit", "", "exact published docs branch commit")
	local := flags.Bool("local-refs", false, "validate local branches only (test/local rehearsal; publishing uses remote refs)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected edition arguments")
	}
	allowed := map[string]string{
		"prepare": "repo version source previous-version change self-managed compute-plane observability navigation qualification out",
		"check":   "repo local-refs", "register": "repo version commit local-refs", "manifest": "repo", "links": "repo",
	}
	fields, ok := allowed[command]
	if !ok {
		return fmt.Errorf("unknown edition command %q", command)
	}
	var invalid string
	flags.Visit(func(f *flag.Flag) {
		if !strings.Contains(" "+fields+" ", " "+f.Name+" ") {
			invalid = f.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("--%s is not valid for edition %s", invalid, command)
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
	switch command {
	case "links":
		return stageEditionLinks(root)
	case "prepare":
		plan := editionPreparation{Source: *source, Version: *version, PreviousVersion: *previous, Change: *change, SelfManaged: *selfManaged, ComputePlane: *compute, Observability: *obs, Navigation: *nav, Qualification: *qualification}
		return prepareEdition(root, *out, plan)
	case "check":
		return validateEditionRegistry(root, !*local)
	case "register":
		return registerEdition(root, editionRelease{Version: *version, Ref: "docs/releases/" + *version, Commit: *commit}, !*local)
	case "manifest":
		catalog, err := LoadCatalog(filepath.Join(root, "docs", "version-catalog", "main.yaml"))
		if err != nil {
			return err
		}
		data, err := manifestForEdition(catalog)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return fmt.Errorf("unknown edition command %q", command)
	}
}

func prepareEdition(root, out string, plan editionPreparation) error {
	if !fullLowercaseCommitSHARe.MatchString(plan.Source) {
		return fmt.Errorf("edition preparation requires a full --source commit SHA")
	}
	if !safeFernPath(plan.Navigation) {
		return fmt.Errorf("navigation must stay under fern/")
	}
	for _, version := range []string{plan.SelfManaged, plan.ComputePlane, plan.Observability} {
		if !validStableStackVersion(version) {
			return fmt.Errorf("edition preparation requires three explicit stable stack versions; latest selection is not allowed")
		}
	}
	if err := validateEditionBump(plan.PreviousVersion, plan.Version, plan.Change); err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("edition preparation requires --out")
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, out)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("preparation output must be outside the source repository")
	}
	if _, err := os.Stat(out); err == nil {
		return checkPreparedEdition(out, plan)
	} else if !os.IsNotExist(err) {
		return err
	}
	catalog, err := catalogForPreparation(root, plan)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(out), ".docs-edition-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	staged := filepath.Join(temporary, "source")
	if err := stageEditionSource(root, staged, plan.Source); err != nil {
		return err
	}
	if err := WriteCatalog(filepath.Join(staged, "docs", "version-catalog", "main.yaml"), catalog); err != nil {
		return err
	}
	if err := SyncDocs(staged, catalog, false); err != nil {
		return err
	}
	if err := writeBranchConfiguration(staged, plan); err != nil {
		return err
	}
	if err := stageEditionLinks(staged); err != nil {
		return err
	}
	plan.Digest, err = editionTreeDigest(staged)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, ".docs-edition-preparation.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(staged, out); err != nil {
		return err
	}
	fmt.Printf("Prepared docs edition %s (%s) at %s. Review the catalog, manifest, navigation, and qualification before creating docs/releases/%s.\n", plan.Version, catalog.DocsEdition.Status, out, plan.Version)
	return nil
}

func catalogForPreparation(root string, plan editionPreparation) (*Catalog, error) {
	catalog, err := catalogAtCommit(root, plan.Source)
	if err != nil {
		return nil, err
	}
	selected := map[string]string{releaseSetStackControlPlane: plan.SelfManaged, releaseSetStackComputePlane: plan.ComputePlane, releaseSetStackObservability: plan.Observability}
	for name, want := range selected {
		stack, _ := catalog.ReleaseSet.Stacks.byName(name)
		if stack.Version != want {
			return nil, fmt.Errorf("reviewed catalog selects %s %s, requested %s; synchronize and review the exact inventory first", name, stack.Version, want)
		}
	}
	if _, err := gitOutput(root, "show", plan.Source+":fern/"+filepath.ToSlash(filepath.Clean(plan.Navigation))); err != nil {
		return nil, fmt.Errorf("reviewed source must contain navigation %s: %w", plan.Navigation, err)
	}
	catalog.DocsEdition = &DocsEdition{Version: plan.Version, PreviousVersion: plan.PreviousVersion, Change: plan.Change, Status: ReleaseSetDevelopment, Qualification: plan.Qualification}
	if plan.Qualification != "" {
		catalog.DocsEdition.Status = ReleaseSetQualified
	}
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	if err := validateEditionSources(root, catalog); err != nil {
		return nil, err
	}
	registry, err := readEditionRegistry(root)
	if err != nil {
		return nil, err
	}
	var previous *Catalog
	if plan.PreviousVersion != "" {
		for _, entry := range registry.Editions {
			if entry.Version == plan.PreviousVersion {
				previous, err = catalogAtCommit(root, entry.Commit)
				if err != nil {
					return nil, err
				}
			}
		}
		if previous == nil || plan.PreviousVersion != registry.Default {
			return nil, fmt.Errorf("previous version must be the registered default edition")
		}
	} else if len(registry.Editions) > 0 {
		return nil, fmt.Errorf("existing editions require an explicit previous version")
	}
	if err := validateEditionTransition(previous, catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func stageEditionSource(root, out, commit string) error {
	if _, err := gitOutput(root, "clone", "--quiet", "--shared", "--no-checkout", root, out); err != nil {
		return err
	}
	origin, err := gitOutput(root, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if _, err := gitOutput(out, "remote", "set-url", "origin", trimOutput(origin)); err != nil {
		return err
	}
	if _, err := gitOutput(out, "sparse-checkout", "set", "docs", "fern", "tools/docs-version-sync"); err != nil {
		return err
	}
	_, err = gitOutput(out, "checkout", "--quiet", "--detach", commit)
	return err
}

func writeBranchConfiguration(root string, plan editionPreparation) error {
	path := filepath.Join(root, "fern", "docs.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}
	archives := editionArchives(config)
	delete(config, "products")
	delete(config, "navigation")
	delete(config, "tabs")
	config["versions"] = append([]any{map[string]any{"display-name": plan.Version, "slug": plan.Version, "path": plan.Navigation}}, archives...)
	data, err = yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	// Only the canonical registry owns the edition history. A release branch
	// renders its own default and must not recursively validate other editions.
	return os.WriteFile(filepath.Join(root, "fern", "editions.yml"), []byte("schema_version: 1\neditions: []\n"), 0o644)
}

// Archives retain the historical product/version URLs while the default becomes
// the branch's own edition. Fern composes only that default when resolving a ref.
func editionArchives(config map[string]any) []any {
	var archives []any
	if versions, ok := config["versions"].([]any); ok {
		for _, item := range versions {
			version, ok := item.(map[string]any)
			if ok && version["hidden"] == true {
				archives = append(archives, version)
			}
		}
	}
	products, _ := config["products"].([]any)
	for _, item := range products {
		product, ok := item.(map[string]any)
		if !ok {
			continue
		}
		versions, _ := product["versions"].([]any)
		for _, item := range versions {
			version, ok := item.(map[string]any)
			if !ok || version["slug"] == "dev" {
				continue
			}
			archives = append(archives, map[string]any{
				"display-name": fmt.Sprintf("%s %s archive", product["display-name"], version["display-name"]),
				"slug":         fmt.Sprintf("%s/%s", product["slug"], version["slug"]), "path": version["path"], "hidden": true,
			})
		}
	}
	return archives
}

func editionTreeDigest(root string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, tree := range []string{"docs", "fern"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(path)
				if err != nil {
					return fmt.Errorf("edition symlink %s: %w", relative, err)
				}
				resolved, err := filepath.Rel(root, target)
				if err != nil || (!strings.HasPrefix(resolved, "docs"+string(filepath.Separator)) && !strings.HasPrefix(resolved, "fern"+string(filepath.Separator))) {
					return fmt.Errorf("edition symlink %s must resolve within docs/ or fern/", relative)
				}
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				fmt.Fprintf(hash, "symlink\x00%s\x00", link)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(relative), len(data))
			_, err = hash.Write(data)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func checkPreparedEdition(out string, want editionPreparation) error {
	data, err := os.ReadFile(filepath.Join(out, ".docs-edition-preparation.json"))
	if err != nil {
		return fmt.Errorf("refusing to overwrite existing output %s", out)
	}
	var existing editionPreparation
	if err := json.Unmarshal(data, &existing); err != nil {
		return err
	}
	digest := existing.Digest
	existing.Digest = ""
	if existing != want {
		return fmt.Errorf("refusing to overwrite a different preparation at %s", out)
	}
	actual, err := editionTreeDigest(out)
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("prepared content at %s changed; refusing to overwrite it", out)
	}
	fmt.Printf("Docs edition %s is already prepared at %s.\n", want.Version, out)
	return nil
}
