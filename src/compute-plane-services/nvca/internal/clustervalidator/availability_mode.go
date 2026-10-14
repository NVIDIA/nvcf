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

package clustervalidator

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

// HAModeEnv carries the self-managed stack's highAvailability.mode, the
// guarantee a control-plane run checks the cluster against. The launcher
// passes the mode it installed: nvcf-cli from --mode, the chart from
// clusterValidator.haMode.
const HAModeEnv = "NVCF_HA_MODE"

// HAModeMarker prefixes the line a control-plane run prints right after the
// RoleMarker line, naming the HA mode it checks against, for example
// "HA mode: enforced" or "HA mode: not supplied". A launcher that forwarded a
// mode reads its absence as an image that predates HAModeEnv, so the text is
// part of the interface and appears on no other line.
const HAModeMarker = "HA mode: "

// HAMode is a highAvailability.mode value. The empty mode is one no launcher
// supplied.
type HAMode string

// The highAvailability.mode values the stack accepts.
const (
	HAModeNone      HAMode = "none"
	HAModePreferred HAMode = "preferred"
	HAModeEnforced  HAMode = "enforced"
)

// haModeNotSuppliedWarning is the one warning of a control-plane run given no
// HA mode. Sizing, PDBs and zones are not graded without one; only placement
// is, per workload, from how firmly its own rendered terms separate its pods.
const haModeNotSuppliedWarning = "HA mode not supplied; the HA invariants were checked against the rendered " +
	"placement only"

// guaranteed reports whether m asks for the HA guarantee.
func (m HAMode) guaranteed() bool { return m == HAModePreferred || m == HAModeEnforced }

// parseHAMode reads a HAModeEnv value, case-insensitive and trimmed. valid is
// false for a value that names no mode, which then counts as not supplied.
func parseHAMode(v string) (mode HAMode, valid bool) {
	switch m := HAMode(strings.ToLower(strings.TrimSpace(v))); m {
	case "", HAModeNone, HAModePreferred, HAModeEnforced:
		return m, true
	}
	return "", false
}

// haModeAbsentRow is the summary row of an HA check under a mode that asks for
// no HA guarantee. It counts as passed: under none the stack chose no
// guarantee, and without a mode the run's warnings already say so, so the row
// alone does not turn a clean run into one that passed with warnings.
func haModeAbsentRow(label string, mode HAMode) summaryRow {
	msg := label + ": Not Applicable (HA mode none)"
	if mode == "" {
		msg = label + ": Not Assessed (HA mode not supplied)"
	}
	return summaryRow{Passed: true, NotApplicable: true, NAMsg: msg}
}

// readHAMode prints the HAModeMarker line for raw, the HAModeEnv value, and
// returns the mode with the warnings it gives the run.
func readHAMode(log *logrus.Entry, raw string) (HAMode, []string) {
	mode, valid := parseHAMode(raw)
	var warnings []string
	if !valid {
		warnings = append(warnings, fmt.Sprintf("Ignoring %s=%q: the stack's highAvailability.mode is none, "+
			"preferred or enforced", HAModeEnv, strings.TrimSpace(raw)))
	}
	if mode == "" {
		log.Info(HAModeMarker + "not supplied")
		return "", append(warnings, haModeNotSuppliedWarning)
	}
	log.Info(HAModeMarker + string(mode))
	return mode, warnings
}
