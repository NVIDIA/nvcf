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

// Package createtable implements `gds-admin create-table`. It creates the
// GDS DynamoDB table, waits until it is ACTIVE, and enables TTL on
// expires_at and point-in-time recovery. On an existing table it checks the
// key schema and only enables what is missing, so it is safe to run on
// every deployment.
//
// It creates a single-region table. Add Global Table replicas afterwards;
// docs/storage.md shows how.
package createtable

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/dynamodb"
)

// Name is the subcommand name.
const Name = "create-table"

// Summary is the one-line description gds-admin prints in its usage.
const Summary = "create the DynamoDB table, enable TTL and point-in-time recovery"

// UsageError reports invalid flags or arguments.
type UsageError struct {
	Err error
}

func (e UsageError) Error() string { return e.Err.Error() }

func (e UsageError) Unwrap() error { return e.Err }

const description = `Creates the GDS DynamoDB table, waits until it is ACTIVE, and enables TTL
on expires_at and point-in-time recovery. On an existing table it checks
the key schema and enables only what is missing.`

// NewClientFunc returns the client the command uses.
type NewClientFunc func(ctx context.Context, opts dynamodb.ClientOptions) (dynamodb.SchemaAPI, error)

func defaultClient(ctx context.Context, opts dynamodb.ClientOptions) (dynamodb.SchemaAPI, error) {
	return dynamodb.NewClient(ctx, opts)
}

// Run parses the arguments that follow "create-table" and creates the
// table. Usage goes to stderr, the result to stdout. Invalid flags and
// arguments are a UsageError; -h and --help return an error matching
// flag.ErrHelp.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return RunWith(ctx, args, stdout, stderr, defaultClient)
}

// RunWith is Run with the client constructor injected, for tests.
func RunWith(ctx context.Context, args []string, stdout, stderr io.Writer, newClient NewClientFunc) error {
	fs := flag.NewFlagSet(Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		table              = fs.String("table", dynamodb.DefaultTableName, "DynamoDB table name")
		region             = fs.String("region", "", "AWS region; defaults to AWS_REGION or the shared AWS configuration")
		endpoint           = fs.String("endpoint", "", "DynamoDB endpoint override, for dynamodb-local")
		deletionProtection = fs.Bool("deletion-protection", true, "create the table with deletion protection")
		pitr               = fs.Bool("pitr", true, "enable point-in-time recovery (dynamodb-local does not support it)")
		ttl                = fs.Bool("ttl", true, "enable TTL on "+dynamodb.AttributeExpiresAt)
		waitTimeout        = fs.Duration("wait-timeout", 5*time.Minute, "how long to wait for the table to become ACTIVE")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: gds-admin %s [flags]\n\n%s\n\nFlags:\n", Name, description)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return UsageError{err}
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return UsageError{fmt.Errorf("unexpected arguments: %q", fs.Args())}
	}
	if *table == "" {
		return UsageError{errors.New("--table must not be empty")}
	}

	client, err := newClient(ctx, dynamodb.ClientOptions{Region: *region, Endpoint: *endpoint})
	if err != nil {
		return err
	}
	created, err := dynamodb.CreateTable(ctx, client, dynamodb.TableOptions{
		TableName:           *table,
		DeletionProtection:  *deletionProtection,
		PointInTimeRecovery: *pitr,
		TimeToLive:          *ttl,
		WaitTimeout:         *waitTimeout,
	})
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(stdout, "created table %s\n", *table)
	} else {
		fmt.Fprintf(stdout, "table %s exists; key schema checked\n", *table)
	}
	if *ttl {
		fmt.Fprintf(stdout, "TTL on %s is enabled\n", dynamodb.AttributeExpiresAt)
	}
	if *pitr {
		fmt.Fprintln(stdout, "point-in-time recovery is enabled")
	}
	return nil
}
