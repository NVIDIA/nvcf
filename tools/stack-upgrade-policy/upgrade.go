// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// UpgradeVerdict is whether a cluster may install a target stack version, and
// when it may not, the next version it should install instead. A refusal
// without a next step is a dead end the customer cannot act on.
type UpgradeVerdict struct {
	Allowed bool
	Reason  string
}

// CanUpgrade decides whether a cluster may move directly to target.
//
// installedVersion is the installed stack version the cluster's upgrade
// receipt records. An empty string means the cluster carries no receipt at
// all, which is every cluster installed before receiptsIntroducedIn: a 0.x
// install and a 1.0.0 install are indistinguishable from here, because neither
// wrote one.
//
// The contract is that a customer installs each major version in order, which
// is derived from the version numbers alone. This function is only that
// arithmetic. Whether a required data migration has finished is tracked
// separately, per chart, because a version number cannot express it.
func CanUpgrade(installedVersion, target, receiptsIntroducedIn string) (UpgradeVerdict, error) {
	targetMajor, err := majorOf(target)
	if err != nil {
		return UpgradeVerdict{}, err
	}
	firstRecording, err := majorOf(receiptsIntroducedIn)
	if err != nil {
		return UpgradeVerdict{}, err
	}

	if installedVersion == "" {
		if targetMajor == firstRecording {
			return UpgradeVerdict{Allowed: true}, nil
		}
		return UpgradeVerdict{Reason: fmt.Sprintf(
			"this cluster records no installed stack version, so it predates %s. "+
				"Install the latest %d.x first, which records one, then upgrade to %s.",
			receiptsIntroducedIn, firstRecording, target)}, nil
	}

	installedMajor, err := majorOf(installedVersion)
	if err != nil {
		return UpgradeVerdict{}, err
	}
	switch {
	case targetMajor < installedMajor:
		return UpgradeVerdict{Reason: fmt.Sprintf(
			"%s is older than the installed %s; downgrading across a major version is not supported",
			target, installedVersion)}, nil
	case targetMajor-installedMajor <= 1:
		return UpgradeVerdict{Allowed: true}, nil
	default:
		return UpgradeVerdict{Reason: fmt.Sprintf(
			"%s is %d major versions ahead of the installed %s. "+
				"Install the latest %d.x first, then continue toward %s.",
			target, targetMajor-installedMajor, installedVersion, installedMajor+1, target)}, nil
	}
}

func majorOf(version string) (int, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	major, _, found := strings.Cut(trimmed, ".")
	if !found {
		return 0, fmt.Errorf("not a MAJOR.MINOR.PATCH version: %q", version)
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, fmt.Errorf("not a MAJOR.MINOR.PATCH version: %q", version)
	}
	return n, nil
}
