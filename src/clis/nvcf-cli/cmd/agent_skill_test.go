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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCmd executes the rootCmd with the given args and returns stdout/stderr/err.
// It resets global flag variables that cobra binds so tests don't bleed state.
func runCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// Reset agent-skill flag globals; cobra doesn't re-zero bound variables
	// between Execute calls, so tests that set --target would pollute later
	// tests that omit it.
	agentSkillTarget = ""
	agentSkillFile = ""
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), errb.String(), err
}

func TestAgentSkillInstall_TargetWritesPublicUserSkills(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "skills")

	out, _, err := runCmd(t, "agent-skill", "install", "--target", target)
	require.NoError(t, err)
	assert.Contains(t, out, "Installed")
	assert.Contains(t, out, target)

	cliSkill := filepath.Join(target, "nvcf-self-managed-cli", "SKILL.md")
	body, err := os.ReadFile(cliSkill)
	require.NoError(t, err)
	assert.Greater(t, len(body), 100, "SKILL.md should have content")
	assert.Contains(t, string(body), "NVCF Self-Hosted CLI")

	_, err = os.ReadFile(filepath.Join(target, "nvcf-self-managed-installation", "SKILL.md"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(target, "SKILL.md"))
	assert.True(t, os.IsNotExist(err), "target should contain skill directories, not a top-level SKILL.md")

	// Version marker exists without masquerading as a skill.
	_, err = os.ReadFile(filepath.Join(target, ".nvcf-cli-public-skills.version"))
	require.NoError(t, err)
}

func TestAgentSkillInstall_DefaultTargetsResolveHome(t *testing.T) {
	// Patch HOME via t.Setenv so we don't actually write to the user's
	// ~/.claude/. cobra calls os.UserHomeDir() which honors HOME on Unix.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	_, _, err := runCmd(t, "agent-skill", "install")
	require.NoError(t, err)

	// Both default locations got every public user skill.
	_, err1 := os.ReadFile(filepath.Join(fakeHome, ".claude", "skills", "nvcf-self-managed-cli", "SKILL.md"))
	_, err2 := os.ReadFile(filepath.Join(fakeHome, ".agents", "skills", "nvcf-self-managed-cli", "SKILL.md"))
	_, err3 := os.ReadFile(filepath.Join(fakeHome, ".claude", "skills", "nvcf-self-managed-installation", "SKILL.md"))
	_, err4 := os.ReadFile(filepath.Join(fakeHome, ".agents", "skills", "nvcf-self-managed-installation", "SKILL.md"))
	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NoError(t, err3)
	require.NoError(t, err4)
}

func TestAgentSkillUninstall_RemovesManagedSkillsOnly(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "skills")
	unrelated := filepath.Join(target, "unrelated-skill")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(unrelated, "SKILL.md"), []byte("keep"), 0o644))

	_, _, err := runCmd(t, "agent-skill", "install", "--target", target)
	require.NoError(t, err)

	_, _, err = runCmd(t, "agent-skill", "uninstall", "--target", target)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(target, "nvcf-self-managed-cli"))
	assert.True(t, os.IsNotExist(err), "managed CLI skill should be gone")
	_, err = os.Stat(filepath.Join(target, "nvcf-self-managed-installation"))
	assert.True(t, os.IsNotExist(err), "managed installation skill should be gone")
	_, err = os.Stat(unrelated)
	require.NoError(t, err, "unrelated skill should remain")
}

func TestAgentSkillShow_DefaultPrintsSkillMD(t *testing.T) {
	out, _, err := runCmd(t, "agent-skill", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "NVCF Self-Hosted CLI")
	assert.Greater(t, len(out), 200, "SKILL.md output should be substantive")
}

func TestAgentSkillShow_FileFlagPrintsRelativePath(t *testing.T) {
	out, _, err := runCmd(t, "agent-skill", "show", "--file", "nvcf-self-managed-installation/SKILL.md")
	require.NoError(t, err)
	assert.Greater(t, len(out), 100)
	assert.Contains(t, out, "NVCF Self-Managed Stack Operations")
}

func TestAgentSkillShow_RejectsPathTraversal(t *testing.T) {
	cases := []string{"../etc/passwd", "/etc/passwd", "prompts/../../../etc/passwd"}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c, "/", "_"), func(t *testing.T) {
			_, _, err := runCmd(t, "agent-skill", "show", "--file", c)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid --file")
		})
	}
}

func TestAgentSkillVersion_PrintsBuildAndManifestSummary(t *testing.T) {
	out, _, err := runCmd(t, "agent-skill", "version")
	require.NoError(t, err)
	assert.Contains(t, out, "nvcf-cli build:")
	assert.Contains(t, out, "embedded public user skills:")
	assert.Contains(t, out, "manifest schemaVersion 1")
}

func TestAgentSkill_TargetFlagTildeExpansion(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	_, _, err := runCmd(t, "agent-skill", "install", "--target", "~/skills")
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(fakeHome, "skills", "nvcf-self-managed-cli", "SKILL.md"))
	require.NoError(t, err)
}
