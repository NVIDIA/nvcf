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

package dbtesting

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/vault/sdk/database/dbplugin/v5"
)

func getRequestTimeout(t *testing.T) time.Duration {
	rawDur := os.Getenv("VAULT_TEST_DATABASE_REQUEST_TIMEOUT")
	if rawDur == "" {
		// Note: we incremented the default timeout from 5 to 10 seconds in a bid
		// to fix sporadic failures of mssql_test.go tests TestInitialize() and
		// TestUpdateUser_password().

		return 10 * time.Second
	}

	dur, err := time.ParseDuration(rawDur)
	if err != nil {
		t.Fatalf("Failed to parse custom request timeout %q: %s", rawDur, err)
	}
	return dur
}

// AssertInitializeCircleCiTest help to diagnose CircleCI failures within AssertInitialize for mssql tests failing
// with "Failed to initialize: error verifying connection ...". This will now mark a test as failed instead of being fatal
func AssertInitializeCircleCiTest(t *testing.T, db dbplugin.Database, req dbplugin.InitializeRequest) dbplugin.InitializeResponse {
	t.Helper()
	maxAttempts := 5
	var resp dbplugin.InitializeResponse
	var err error

	for i := 1; i <= maxAttempts; i++ {
		resp, err = verifyInitialize(t, db, req)
		if err != nil {
			t.Errorf("Failed AssertInitialize attempt: %d with error:\n%+v\n", i, err)
			time.Sleep(1 * time.Second)
			continue
		}

		if i > 1 {
			t.Logf("AssertInitialize worked the %d time around with a 1 second sleep", i)
		}
		break
	}

	return resp
}

func AssertInitialize(t *testing.T, db dbplugin.Database, req dbplugin.InitializeRequest) dbplugin.InitializeResponse {
	t.Helper()
	resp, err := verifyInitialize(t, db, req)
	if err != nil {
		t.Fatalf("Failed to initialize: %s", err)
	}
	return resp
}

func verifyInitialize(t *testing.T, db dbplugin.Database, req dbplugin.InitializeRequest) (dbplugin.InitializeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), getRequestTimeout(t))
	defer cancel()

	return db.Initialize(ctx, req)
}

func AssertNewUser(t *testing.T, db dbplugin.Database, req dbplugin.NewUserRequest) dbplugin.NewUserResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), getRequestTimeout(t))
	defer cancel()

	resp, err := db.NewUser(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create new user: %s", err)
	}

	if resp.Username == "" {
		t.Fatalf("Missing username from NewUser response")
	}
	return resp
}

func AssertUpdateUser(t *testing.T, db dbplugin.Database, req dbplugin.UpdateUserRequest) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), getRequestTimeout(t))
	defer cancel()

	_, err := db.UpdateUser(ctx, req)
	if err != nil {
		t.Fatalf("Failed to update user: %s", err)
	}
}

func AssertDeleteUser(t *testing.T, db dbplugin.Database, req dbplugin.DeleteUserRequest) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), getRequestTimeout(t))
	defer cancel()

	_, err := db.DeleteUser(ctx, req)
	if err != nil {
		t.Fatalf("Failed to delete user %q: %s", req.Username, err)
	}
}

func AssertClose(t *testing.T, db dbplugin.Database) {
	t.Helper()
	err := db.Close()
	if err != nil {
		t.Fatalf("Failed to close database: %s", err)
	}
}
