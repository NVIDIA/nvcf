// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/NVIDIA/nvcf/tools/ncp-local-cluster/credential-provider-go/internal/provider"
)

func main() {
	// Configure logger to write to stderr, without prefix, date, or time
	logger := log.New(os.Stderr, "", 0)

	// Define a new flag set for the "get-credentials" subcommand
	getCredentialsCmd := flag.NewFlagSet("get-credentials", flag.ExitOnError)
	configFile := getCredentialsCmd.String("config-file", "", "Path to the Docker config.json file.")
	// Allow -c as a shorthand for --config-file
	getCredentialsCmd.StringVar(configFile, "c", "", "Path to the Docker config.json file (shorthand).")

	if len(os.Args) < 2 {
		logger.Println("Error: Expected 'get-credentials' subcommand.")
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "get-credentials":
		// Parse flags specific to "get-credentials" from os.Args[2:]
		err := getCredentialsCmd.Parse(os.Args[2:])
		if err != nil {
			// ExitOnError in NewFlagSet should handle this, but as a safeguard:
			logger.Printf("Error parsing flags for get-credentials: %v\n", err)
			printUsage() // Or getCredentialsCmd.Usage()
			os.Exit(1)
		}
	default:
		logger.Printf("Error: Unknown subcommand '%s'. Expected 'get-credentials'.\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if *configFile == "" {
		logger.Println("Error: --config-file flag is required for 'get-credentials'.")
		// It's good practice for subcommands to have their own usage message
		getCredentialsCmd.Usage = func() {
			fmt.Fprintf(os.Stderr, "Usage: %s get-credentials --config-file <path-to-config.json>\n", os.Args[0])
			fmt.Fprintln(os.Stderr, "Flags:")
			getCredentialsCmd.PrintDefaults()
		}
		getCredentialsCmd.Usage()
		os.Exit(1)
	}

	requestBody, err := io.ReadAll(os.Stdin)
	if err != nil {
		logger.Printf("Error reading request body from stdin: %v\n", err)
		// For critical errors like failing to read stdin, an exit(1) is more direct.
		// The Kubelet spec for exit 0 with {} is more for provider-specific logic failures.
		os.Exit(1)
	}

	if len(requestBody) == 0 {
		logger.Println("Error: Request body from stdin is empty.")
		// As per previous discussion, provider.HandleGetCredentials will handle image validation.
		// If Kubelet truly sends empty for a probe, the provider might need to respond gracefully.
		// For now, this check is for basic sanity before calling the provider.
		// Kubelet usually expects {} and exit 0 for recoverable/expected errors.
		fmt.Fprintln(os.Stdout, "{}")
		os.Exit(0) // Exit 0 for empty stdin, as Kubelet might expect a plugin to not crash.
	}

	response, err := provider.HandleGetCredentials(requestBody, *configFile)
	if err != nil {
		logger.Printf("Error getting credentials: %v\n", err)
		fmt.Fprintln(os.Stdout, "{}") // Print empty JSON on error as a common Kubelet expectation.
		os.Exit(0)                    // Kubelet often expects exit 0 even if the provider has an internal issue.
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		logger.Printf("Error marshalling response to JSON: %v\n", err)
		fmt.Fprintln(os.Stdout, "{}")
		os.Exit(0) // Exit 0 as Kubelet might expect this.
	}

	fmt.Fprintln(os.Stdout, string(responseJSON))
}

func printUsage() {
	// General usage for the application itself
	fmt.Fprintf(os.Stderr, "Usage: %s <subcommand> [flags]\n", os.Args[0])
	fmt.Fprintln(os.Stderr, "Available subcommands:")
	fmt.Fprintln(os.Stderr, "  get-credentials   Retrieves credentials for a given image.")
	fmt.Fprintln(os.Stderr, "Flags for get-credentials:")
	// Manually list flags here or create a dummy flag set to print defaults
	// For simplicity with one subcommand:
	fmt.Fprintln(os.Stderr, "  --config-file string")
	fmt.Fprintln(os.Stderr, "    \tPath to the Docker config.json file.")
	fmt.Fprintln(os.Stderr, "  -c string")
	fmt.Fprintln(os.Stderr, "    \tPath to the Docker config.json file (shorthand).")
}
