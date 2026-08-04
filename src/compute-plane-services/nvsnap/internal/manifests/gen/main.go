/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Writes the criu-v2 restore placeholders from their source manifests.
//
//	go run ./internal/manifests/gen            # write
//	go run ./internal/manifests/gen -check     # report drift, write nothing
//
// -check is what the test uses, so a hand-edited placeholder fails CI instead
// of silently diverging from its source.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/manifests"
)

func main() {
	check := flag.Bool("check", false, "report drift instead of writing")
	dir := flag.String("dir", "deploy/k8s/workloads", "workload manifest directory")
	flag.Parse()

	sources, err := filepath.Glob(filepath.Join(*dir, "*.yaml"))
	if err != nil || len(sources) == 0 {
		fmt.Fprintf(os.Stderr, "no manifests under %s\n", *dir)
		os.Exit(1)
	}

	drift := 0
	for _, src := range sources {
		if strings.HasSuffix(filepath.Base(src), "-restore.yaml") {
			continue
		}
		body, rerr := os.ReadFile(src) //nolint:gosec // fixed set of in-repo manifests
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", src, rerr)
			os.Exit(1)
		}
		if !manifests.IsCRIUV2Source(body) {
			continue
		}
		out, gerr := manifests.RenderRestore(body)
		if gerr != nil {
			// Report and keep going: one workload that has not adopted the
			// convention must not block regenerating the others.
			fmt.Fprintf(os.Stderr, "SKIP %v\n", gerr)
			drift++
			continue
		}
		dst := strings.TrimSuffix(src, ".yaml") + "-restore.yaml"

		if *check {
			cur, _ := os.ReadFile(dst) //nolint:gosec // fixed set of in-repo manifests
			if string(cur) != string(out) {
				fmt.Printf("DRIFT %s\n", filepath.Base(dst))
				drift++
			}
			continue
		}
		if werr := os.WriteFile(dst, out, 0o644); werr != nil { //nolint:gosec // world-readable manifest
			fmt.Fprintf(os.Stderr, "write %s: %v\n", dst, werr)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", filepath.Base(dst))
	}

	if *check && drift > 0 {
		fmt.Fprintf(os.Stderr, "\n%d placeholder(s) differ from what their source implies.\n"+
			"Run: go run ./internal/manifests/gen\n", drift)
		os.Exit(1)
	}
}
