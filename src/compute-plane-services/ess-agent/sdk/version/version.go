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

package version

import (
	"bytes"
	"fmt"
)

// VersionInfo
type VersionInfo struct {
	Revision          string `json:"revision,omitempty"`
	Version           string `json:"version,omitempty"`
	VersionPrerelease string `json:"version_prerelease,omitempty"`
	VersionMetadata   string `json:"version_metadata,omitempty"`
	BuildDate         string `json:"build_date,omitempty"`
}

func GetVersion() *VersionInfo {
	ver := Version
	rel := VersionPrerelease
	md := VersionMetadata
	if GitDescribe != "" {
		ver = GitDescribe
	}
	if GitDescribe == "" && rel == "" && VersionPrerelease != "" {
		rel = "dev"
	}

	return &VersionInfo{
		Revision:          GitCommit,
		Version:           ver,
		VersionPrerelease: rel,
		VersionMetadata:   md,
		BuildDate:         BuildDate,
	}
}

func (c *VersionInfo) VersionNumber() string {
	if Version == "unknown" && VersionPrerelease == "unknown" {
		return "(version unknown)"
	}

	version := c.Version

	if c.VersionPrerelease != "" {
		version = fmt.Sprintf("%s-%s", version, c.VersionPrerelease)
	}

	if c.VersionMetadata != "" {
		version = fmt.Sprintf("%s+%s", version, c.VersionMetadata)
	}

	return version
}

func (c *VersionInfo) FullVersionNumber(rev bool) string {
	var versionString bytes.Buffer

	if Version == "unknown" && VersionPrerelease == "unknown" {
		return "Vault (version unknown)"
	}

	fmt.Fprintf(&versionString, "Vault v%s", c.Version)
	if c.VersionPrerelease != "" {
		fmt.Fprintf(&versionString, "-%s", c.VersionPrerelease)
	}

	if c.VersionMetadata != "" {
		fmt.Fprintf(&versionString, "+%s", c.VersionMetadata)
	}

	if rev && c.Revision != "" {
		fmt.Fprintf(&versionString, " (%s)", c.Revision)
	}

	if c.BuildDate != "" {
		fmt.Fprintf(&versionString, ", built %s", c.BuildDate)
	}

	return versionString.String()
}
