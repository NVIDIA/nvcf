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

// Command gds-admin administers a Global Discovery Service deployment.
//
//	gds-admin create-table [flags]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/cmd/gds-admin/createtable"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type command struct {
	name, summary string
	run           func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

var commands = []command{
	{createtable.Name, createtable.Summary, createtable.Run},
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gds-admin <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-14s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Run "gds-admin <command> -h" for the flags of a command.`)
}

// run executes one command and returns the exit code: 0 on success and for
// help, 1 when the command fails, 2 for usage errors.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	}
	for _, c := range commands {
		if c.name != args[0] {
			continue
		}
		err := c.run(ctx, args[1:], stdout, stderr)
		switch {
		case err == nil, errors.Is(err, flag.ErrHelp):
			return 0
		case errors.As(err, new(createtable.UsageError)):
			fmt.Fprintf(stderr, "gds-admin %s: %v\n", c.name, err)
			return 2
		default:
			fmt.Fprintf(stderr, "gds-admin %s: %v\n", c.name, err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "gds-admin: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}
