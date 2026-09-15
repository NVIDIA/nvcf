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
	"encoding/base64"
	"strings"
	"testing"

	"nvcf-cli/internal/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Command Structure ---
// Guards against accidental rename/regression of the `registry-credential`
// command group and its subcommands (NVCF-10082 renamed `registry` ->
// `registry-credential`).

func TestRegistryCredentialCommandStructure(t *testing.T) {
	t.Run("top-level command uses registry-credential", func(t *testing.T) {
		assert.Equal(t, "registry-credential", registryCmd.Use)
	})

	t.Run("registers all expected subcommands", func(t *testing.T) {
		expected := map[string]bool{
			"list":                   false,
			"add":                    false,
			"get <credential-id>":    false,
			"update <credential-id>": false,
			"delete <credential-id>": false,
			"list-recognized":        false,
		}
		for _, sub := range registryCmd.Commands() {
			if _, ok := expected[sub.Use]; ok {
				expected[sub.Use] = true
			}
		}
		for use, found := range expected {
			assert.Truef(t, found, "expected subcommand %q to be registered", use)
		}
	})

	t.Run("add command marks hostname and artifact-type required", func(t *testing.T) {
		annotations := registryAddCmd.Flag("hostname").Annotations
		assert.Contains(t, annotations["cobra_annotation_bash_completion_one_required_flag"], "true")

		annotations = registryAddCmd.Flag("artifact-type").Annotations
		assert.Contains(t, annotations["cobra_annotation_bash_completion_one_required_flag"], "true")
	})

	t.Run("add command exposes secret/username/password flags", func(t *testing.T) {
		for _, name := range []string{"hostname", "username", "password", "secret", "artifact-type", "description", "tag"} {
			assert.NotNilf(t, registryAddCmd.Flag(name), "expected --%s flag on add command", name)
		}
	})

	t.Run("delete command exposes --force flag", func(t *testing.T) {
		assert.NotNil(t, registryDeleteCmd.Flag("force"))
	})

	t.Run("examples reference the new command name", func(t *testing.T) {
		assert.Contains(t, registryCmd.Long, "nvcf-cli registry-credential")
		assert.NotContains(t, registryCmd.Long, "nvcf-cli registry add")
		assert.NotContains(t, registryCmd.Long, "nvcf-cli registry list")
	})
}

// --- validateAndEncodeCredentials ---

func TestValidateAndEncodeCredentials(t *testing.T) {
	t.Run("returns secret as-is when only --secret provided", func(t *testing.T) {
		got, err := validateAndEncodeCredentials("preencoded==", "", "")
		require.NoError(t, err)
		assert.Equal(t, "preencoded==", got)
	})

	t.Run("base64-encodes username:password when both provided", func(t *testing.T) {
		got, err := validateAndEncodeCredentials("", "alice", "s3cret")
		require.NoError(t, err)

		decoded, decodeErr := base64.StdEncoding.DecodeString(got)
		require.NoError(t, decodeErr)
		assert.Equal(t, "alice:s3cret", string(decoded))
	})

	t.Run("rejects --secret combined with --username", func(t *testing.T) {
		_, err := validateAndEncodeCredentials("preencoded==", "alice", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot use --secret with --username/--password")
	})

	t.Run("rejects --secret combined with --password", func(t *testing.T) {
		_, err := validateAndEncodeCredentials("preencoded==", "", "s3cret")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot use --secret with --username/--password")
	})

	t.Run("rejects when no auth flags provided", func(t *testing.T) {
		_, err := validateAndEncodeCredentials("", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must provide either --secret OR both --username and --password")
	})

	t.Run("rejects when only --username provided", func(t *testing.T) {
		_, err := validateAndEncodeCredentials("", "alice", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must provide either --secret OR both --username and --password")
	})

	t.Run("rejects when only --password provided", func(t *testing.T) {
		_, err := validateAndEncodeCredentials("", "", "s3cret")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must provide either --secret OR both --username and --password")
	})

	t.Run("encodes passwords containing colons correctly", func(t *testing.T) {
		// Passwords with `:` are common; the API receives the *first* `:` as the
		// separator. We only need to assert we encode the raw concatenation
		// (server-side decoding is not our concern).
		got, err := validateAndEncodeCredentials("", "alice", "p:a:s:s")
		require.NoError(t, err)

		decoded, decodeErr := base64.StdEncoding.DecodeString(got)
		require.NoError(t, decodeErr)
		assert.Equal(t, "alice:p:a:s:s", string(decoded))
	})
}

// --- parseAndValidateArtifactTypes ---

func TestParseAndValidateArtifactTypes(t *testing.T) {
	t.Run("accepts all valid types", func(t *testing.T) {
		got, err := parseAndValidateArtifactTypes([]string{"CONTAINER", "HELM", "MODEL", "RESOURCE"})
		require.NoError(t, err)
		assert.Equal(t, []client.ArtifactType{
			client.ArtifactType("CONTAINER"),
			client.ArtifactType("HELM"),
			client.ArtifactType("MODEL"),
			client.ArtifactType("RESOURCE"),
		}, got)
	})

	t.Run("normalizes input to uppercase", func(t *testing.T) {
		got, err := parseAndValidateArtifactTypes([]string{"container", "Helm", "MoDeL"})
		require.NoError(t, err)
		assert.Equal(t, []client.ArtifactType{
			client.ArtifactType("CONTAINER"),
			client.ArtifactType("HELM"),
			client.ArtifactType("MODEL"),
		}, got)
	})

	t.Run("rejects unknown type with helpful message", func(t *testing.T) {
		_, err := parseAndValidateArtifactTypes([]string{"CONTAINER", "BOGUS"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid artifact type 'BOGUS'")
		assert.Contains(t, err.Error(), "CONTAINER, HELM, MODEL, RESOURCE")
	})

	t.Run("preserves original case in error message", func(t *testing.T) {
		_, err := parseAndValidateArtifactTypes([]string{"sneaky"})
		require.Error(t, err)
		// Original (lowercase) input is echoed back so users see what they typed.
		assert.Contains(t, err.Error(), "'sneaky'")
	})

	t.Run("returns nil for empty input (caller enforces non-empty)", func(t *testing.T) {
		got, err := parseAndValidateArtifactTypes(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// --- formatArtifactTypes ---

func TestFormatArtifactTypes(t *testing.T) {
	t.Run("joins multiple types with comma+space", func(t *testing.T) {
		got := formatArtifactTypes([]client.ArtifactType{
			client.ArtifactType("CONTAINER"),
			client.ArtifactType("HELM"),
		})
		assert.Equal(t, "CONTAINER, HELM", got)
	})

	t.Run("returns empty string for empty slice", func(t *testing.T) {
		assert.Equal(t, "", formatArtifactTypes(nil))
		assert.Equal(t, "", formatArtifactTypes([]client.ArtifactType{}))
	})

	t.Run("handles single element", func(t *testing.T) {
		got := formatArtifactTypes([]client.ArtifactType{client.ArtifactType("MODEL")})
		assert.Equal(t, "MODEL", got)
	})
}

// --- formatTimestamp ---

func TestFormatTimestamp(t *testing.T) {
	t.Run("reformats valid RFC3339 timestamp", func(t *testing.T) {
		got := formatTimestamp("2025-01-15T10:30:45Z")
		// Expect the output to use the human-friendly "YYYY-MM-DD HH:MM:SS"
		// layout (not the RFC3339 "T...Z" layout). We don't assert the trailing
		// timezone abbreviation since it depends on the host locale.
		assert.Contains(t, got, "2025-01-15")
		assert.Contains(t, got, "10:30:45")
		assert.NotEqual(t, "2025-01-15T10:30:45Z", got, "timestamp should be reformatted, not echoed")
		// The date/time portion should be space-separated, not "T"-separated.
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`, got)
	})

	t.Run("returns input unchanged when not RFC3339", func(t *testing.T) {
		assert.Equal(t, "not-a-timestamp", formatTimestamp("not-a-timestamp"))
	})

	t.Run("returns empty input unchanged", func(t *testing.T) {
		assert.Equal(t, "", formatTimestamp(""))
	})

	t.Run("handles RFC3339 with timezone offset", func(t *testing.T) {
		got := formatTimestamp("2025-01-15T10:30:45-07:00")
		assert.Contains(t, got, "2025-01-15")
		// Either reformatted or returned as-is; both are acceptable, but it
		// must not blow up.
		assert.NotEmpty(t, got)
	})
}

// --- Sanity check: top-level Use constant matches help text ---

func TestRegistryCredentialUseMatchesHelp(t *testing.T) {
	// Quick sanity that all subcommand long-help references use the new
	// command name. This catches drift if anyone re-introduces "registry "
	// as the top-level group name.
	for _, sub := range registryCmd.Commands() {
		assert.NotContains(t, sub.Long, "nvcf-cli registry add",
			"subcommand %q long help still references old `registry add` form", sub.Use)
		assert.NotContains(t, sub.Long, "nvcf-cli registry list",
			"subcommand %q long help still references old `registry list` form", sub.Use)
		assert.NotContains(t, sub.Long, "nvcf-cli registry get",
			"subcommand %q long help still references old `registry get` form", sub.Use)
	}

	// And the top-level Long should reference the new name.
	assert.True(t, strings.Contains(registryCmd.Long, "nvcf-cli registry-credential"),
		"top-level command long help missing `nvcf-cli registry-credential` reference")
}
