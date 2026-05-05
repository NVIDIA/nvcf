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

package dbutil

import (
	"reflect"
	"testing"

	"github.com/hashicorp/vault/sdk/database/dbplugin"
)

func TestStatementCompatibilityHelper(t *testing.T) {
	const (
		creationStatement = "creation"
		renewStatement    = "renew"
		revokeStatement   = "revoke"
		rollbackStatement = "rollback"
	)

	expectedStatements := dbplugin.Statements{
		Creation:             []string{creationStatement},
		Rollback:             []string{rollbackStatement},
		Revocation:           []string{revokeStatement},
		Renewal:              []string{renewStatement},
		CreationStatements:   creationStatement,
		RenewStatements:      renewStatement,
		RollbackStatements:   rollbackStatement,
		RevocationStatements: revokeStatement,
	}

	statements1 := dbplugin.Statements{
		CreationStatements:   creationStatement,
		RenewStatements:      renewStatement,
		RollbackStatements:   rollbackStatement,
		RevocationStatements: revokeStatement,
	}

	if !reflect.DeepEqual(expectedStatements, StatementCompatibilityHelper(statements1)) {
		t.Fatalf("mismatch: %#v, %#v", expectedStatements, statements1)
	}

	statements2 := dbplugin.Statements{
		Creation:   []string{creationStatement},
		Rollback:   []string{rollbackStatement},
		Revocation: []string{revokeStatement},
		Renewal:    []string{renewStatement},
	}

	if !reflect.DeepEqual(expectedStatements, StatementCompatibilityHelper(statements2)) {
		t.Fatalf("mismatch: %#v, %#v", expectedStatements, statements2)
	}

	statements3 := dbplugin.Statements{
		CreationStatements: creationStatement,
	}
	expectedStatements3 := dbplugin.Statements{
		Creation:           []string{creationStatement},
		CreationStatements: creationStatement,
	}
	if !reflect.DeepEqual(expectedStatements3, StatementCompatibilityHelper(statements3)) {
		t.Fatalf("mismatch: %#v, %#v", expectedStatements3, statements3)
	}
}
