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

// Package managementtls builds the TLS trust configuration for the CLI's
// management-API path from the control-plane profile's managementTls (R-4).
// It is a separate contract from the worker transportTls; the two must never be
// inferred from one another (POR section 9.1). There is no user-facing
// skip-verify: trust is always established by system roots, a configured CA
// bundle, or both during an explicit root-CA rotation window.
package managementtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"

	"nvcf-cli/internal/selfhosted/controlplaneprofile"
)

// TLSConfig builds a *tls.Config for the management-API client from the
// profile's managementTls. It never exposes InsecureSkipVerify as a user knob:
//   - system (or empty): verify against system trust roots.
//   - bundle: verify against the provided CA trust bundle only. The bundle may
//     carry more than one root during a planned root-CA rotation window.
func TLSConfig(m controlplaneprofile.ManagementTLS) (*tls.Config, error) {
	switch strings.TrimSpace(m.TrustMode) {
	case "", controlplaneprofile.TrustModeSystem:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil

	case controlplaneprofile.TrustModeBundle:
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(m.CABundlePEM)) {
			return nil, fmt.Errorf("managementTls.caBundlePem: no certificates parsed")
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil

	default:
		return nil, fmt.Errorf("managementTls.trustMode: must be system or bundle")
	}
}
