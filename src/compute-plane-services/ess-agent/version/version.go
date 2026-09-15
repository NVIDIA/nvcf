/*
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0

Not a contribution
Changes made by NVIDIA CORPORATION & AFFILIATES enabling ESS agent rebrand and packaging or otherwise documented as
NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
<NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
*/
package version

import "fmt"

const (
	Version           = "1.0.5"
	VersionPrerelease = "" // "-dev", "-beta", "-rc1", etc. (include dash)
)

var (
	// added for nv
	// rename Name to "ess-agent"
	Name      string = "ess-agent"
	GitCommit string

	HumanVersion = fmt.Sprintf("%s v%s%s (%s)",
		Name, Version, VersionPrerelease, GitCommit)
)
