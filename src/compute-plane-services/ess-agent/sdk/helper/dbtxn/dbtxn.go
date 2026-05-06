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

package dbtxn

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ExecuteDBQuery handles executing one single statement while properly releasing its resources.
// - ctx: 	Required
// - db: 	Required
// - config: 	Optional, may be nil
// - query: 	Required
func ExecuteDBQuery(ctx context.Context, db *sql.DB, params map[string]string, query string) error {
	parsedQuery := parseQuery(params, query)

	stmt, err := db.PrepareContext(ctx, parsedQuery)
	if err != nil {
		return err
	}
	defer stmt.Close()

	return execute(ctx, stmt)
}

// ExecuteDBQueryDirect handles executing one single statement without preparing the query
// before executing it, which can be more efficient.
// - ctx: 	Required
// - db: 	Required
// - config: 	Optional, may be nil
// - query: 	Required
func ExecuteDBQueryDirect(ctx context.Context, db *sql.DB, params map[string]string, query string) error {
	parsedQuery := parseQuery(params, query)
	_, err := db.ExecContext(ctx, parsedQuery)
	return err
}

// ExecuteTxQuery handles executing one single statement while properly releasing its resources.
// - ctx: 	Required
// - tx: 	Required
// - config: 	Optional, may be nil
// - query: 	Required
func ExecuteTxQuery(ctx context.Context, tx *sql.Tx, params map[string]string, query string) error {
	parsedQuery := parseQuery(params, query)

	stmt, err := tx.PrepareContext(ctx, parsedQuery)
	if err != nil {
		return err
	}
	defer stmt.Close()

	return execute(ctx, stmt)
}

// ExecuteTxQueryDirect handles executing one single statement.
// - ctx: 	Required
// - tx: 	Required
// - config: 	Optional, may be nil
// - query: 	Required
func ExecuteTxQueryDirect(ctx context.Context, tx *sql.Tx, params map[string]string, query string) error {
	parsedQuery := parseQuery(params, query)
	_, err := tx.ExecContext(ctx, parsedQuery)
	return err
}

func execute(ctx context.Context, stmt *sql.Stmt) error {
	if _, err := stmt.ExecContext(ctx); err != nil {
		return err
	}
	return nil
}

func parseQuery(m map[string]string, tpl string) string {
	if m == nil || len(m) <= 0 {
		return tpl
	}

	for k, v := range m {
		tpl = strings.ReplaceAll(tpl, fmt.Sprintf("{{%s}}", k), v)
	}
	return tpl
}
