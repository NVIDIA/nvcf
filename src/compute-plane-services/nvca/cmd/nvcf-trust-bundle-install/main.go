/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls/trustbundle"
)

const (
	defaultSystemBundle = "/etc/ssl/certs/ca-certificates.crt"
	defaultTrustBundle  = "/nvcf/trust/nvcf-ca-bundle.pem"
	defaultOutputBundle = "/merged-certs/ca-certificates.crt"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("nvcf-trust-bundle-install", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var opts trustbundle.InstallOptions
	fs.StringVar(&opts.SystemBundlePath, "system-bundle", defaultSystemBundle, "path to the system CA bundle")
	fs.StringVar(&opts.TrustBundlePath, "trust-bundle", defaultTrustBundle, "path to the mounted NVCF trust bundle PEM")
	fs.StringVar(&opts.OutputBundlePath, "output-bundle", defaultOutputBundle, "path to write the merged CA bundle")
	fs.StringVar(&opts.ExpectedFingerprint, "expected-fingerprint", "", "expected canonical nvcf-trust-bundle-v1 fingerprint")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(opts.ExpectedFingerprint) == "" {
		_, _ = fmt.Fprintln(stderr, "--expected-fingerprint is required")
		return 2
	}
	if err := trustbundle.MergeFiles(opts); err != nil {
		_, _ = fmt.Fprintf(stderr, "install trust bundle: %v\n", err)
		return 1
	}
	return 0
}
