/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/agentskill"
)

var (
	agentSkillTarget string
	agentSkillFile   string
)

var agentSkillCmd = &cobra.Command{
	Use:   "agent-skill",
	Short: "Install or manage the nvcf-cli agent skill bundle",
	Long: `Install, manage, and operate the nvcf-cli agent skill — a packaged set of
markdown files that AI coding agents (Claude Code, Cursor, Codex, etc.) load
to understand how to drive nvcf-cli.

By default, 'install' writes the bundle to BOTH ~/.claude/skills/nvcf-cli/
and ~/.agents/skills/nvcf-cli/ (the standard agent-ecosystem directories).
Use --target to override to a single location.`,
}

var agentSkillInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install the embedded skill bundle to ~/.claude/skills/nvcf-cli/ and ~/.agents/skills/nvcf-cli/",
	SilenceUsage: true,
	RunE:         runAgentSkillInstall,
}

func runAgentSkillInstall(c *cobra.Command, _ []string) error {
	targets, err := resolveAgentSkillTargets()
	if err != nil {
		return err
	}
	if err := agentskill.Install(c.Context(), targets); err != nil {
		return err
	}
	fmt.Fprintf(c.OutOrStdout(), "Installed nvcf-cli agent skill to:\n")
	for _, t := range targets {
		fmt.Fprintf(c.OutOrStdout(), "  %s\n", t)
	}
	return nil
}

var agentSkillUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove the installed skill bundle from ~/.claude/skills/nvcf-cli/ and ~/.agents/skills/nvcf-cli/",
	SilenceUsage: true,
	RunE:         runAgentSkillUninstall,
}

func runAgentSkillUninstall(c *cobra.Command, _ []string) error {
	targets, err := resolveAgentSkillTargets()
	if err != nil {
		return err
	}
	if err := agentskill.Uninstall(c.Context(), targets); err != nil {
		return err
	}
	fmt.Fprintf(c.OutOrStdout(), "Removed nvcf-cli agent skill from:\n")
	for _, t := range targets {
		fmt.Fprintf(c.OutOrStdout(), "  %s\n", t)
	}
	return nil
}

var agentSkillShowCmd = &cobra.Command{
	Use:          "show",
	Short:        "Print the embedded SKILL.md (or another file via --file)",
	SilenceUsage: true,
	RunE:         runAgentSkillShow,
}

func runAgentSkillShow(c *cobra.Command, _ []string) error {
	rel := agentSkillFile
	if rel == "" {
		rel = "SKILL.md"
	}
	if strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("invalid --file %q: must be a relative path under data/", rel)
	}
	body, err := fs.ReadFile(agentskill.FS(), "data/"+rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	_, err = c.OutOrStdout().Write(body)
	return err
}

var agentSkillVersionCmd = &cobra.Command{
	Use:          "version",
	Short:        "Print the build SHA + embedded-skill summary",
	SilenceUsage: true,
	RunE:         runAgentSkillVersion,
}

func runAgentSkillVersion(c *cobra.Command, _ []string) error {
	fmt.Fprintf(c.OutOrStdout(), "nvcf-cli build: %s\n", agentskill.BuildSHA())
	m, err := agentskill.LoadManifest(agentskill.FS())
	if err != nil {
		fmt.Fprintf(c.OutOrStdout(), "embedded skill: <load failed: %v>\n", err)
		return err
	}
	fmt.Fprintf(c.OutOrStdout(), "embedded skill: %d files, %d bytes (manifest schemaVersion %d)\n",
		m.TotalFiles, m.TotalBytes, m.SchemaVersion)
	return nil
}

// resolveAgentSkillTargets returns the list of directories install/uninstall
// will operate on. With --target set, it's exactly that one directory; the
// caller is responsible for any tilde expansion. Without --target, it's the
// two ecosystem-standard locations under $HOME.
func resolveAgentSkillTargets() ([]string, error) {
	if agentSkillTarget != "" {
		return []string{expandHome(agentSkillTarget)}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate user home: %w", err)
	}
	return []string{
		filepath.Join(home, ".claude", "skills", "nvcf-cli"),
		filepath.Join(home, ".agents", "skills", "nvcf-cli"),
	}, nil
}

// expandHome handles a leading "~" or "~/" in a user-supplied path. cobra
// doesn't auto-expand and a path like "~/skills/foo" otherwise gets created
// as a literal "~" subdirectory in the cwd.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p // "~someuser/" not supported — leave as-is
}

func init() {
	agentSkillCmd.AddCommand(agentSkillInstallCmd)
	agentSkillCmd.AddCommand(agentSkillUninstallCmd)
	agentSkillCmd.AddCommand(agentSkillShowCmd)
	agentSkillCmd.AddCommand(agentSkillVersionCmd)

	// --target is meaningful only on install + uninstall (show + version
	// don't touch the filesystem). Bind to the same package-level var on
	// both subs so the underlying resolveAgentSkillTargets logic doesn't
	// branch on which sub fired.
	const targetUsage = "Override default install/uninstall directory (default: both ~/.claude/skills/nvcf-cli/ and ~/.agents/skills/nvcf-cli/)"
	agentSkillInstallCmd.Flags().StringVar(&agentSkillTarget, "target", "", targetUsage)
	agentSkillUninstallCmd.Flags().StringVar(&agentSkillTarget, "target", "", targetUsage)
	agentSkillShowCmd.Flags().StringVar(&agentSkillFile, "file", "",
		"Print a specific file from the embedded bundle (default: SKILL.md)")

	rootCmd.AddCommand(agentSkillCmd)
}
