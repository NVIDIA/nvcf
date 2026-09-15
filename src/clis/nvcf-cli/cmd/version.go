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
	"runtime"

	"github.com/spf13/cobra"
)

// Version information (injected at build time)
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
	GitBranch = "unknown"
	BuildUser = "unknown"
	GoVersion = "unknown"
	Platform  = runtime.GOOS + "/" + runtime.GOARCH
)

var versionCmd = &cobra.Command{
	Use:          "version",
	Short:        "Show version information",
	SilenceUsage: true,
	Long: `Display version information for the NVCF CLI.

This command shows the version, build information, and runtime details
of the currently installed NVCF CLI binary.`,
	Run: runVersion,
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersion(cmd *cobra.Command, args []string) {
	fmt.Printf("NVCF CLI Version Information:\n")
	fmt.Printf("  Version:     %s\n", Version)
	fmt.Printf("  Git Commit:  %s\n", GitCommit)
	fmt.Printf("  Git Branch:  %s\n", GitBranch)
	fmt.Printf("  Build Date:  %s\n", BuildDate)
	fmt.Printf("  Build User:  %s\n", BuildUser)

	// Show Go version - prefer the build-time injected version, fall back to runtime
	goVer := GoVersion
	if goVer == "unknown" || goVer == "" {
		goVer = runtime.Version()
	}
	fmt.Printf("  Go Version:  %s\n", goVer)
	fmt.Printf("  Platform:    %s\n", Platform)
}
