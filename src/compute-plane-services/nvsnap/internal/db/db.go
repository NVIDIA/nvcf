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

// Package db provides a SQLite-backed checkpoint catalog for nvsnap-server.
// Uses pure-Go SQLite (modernc.org/sqlite) so the server can build with CGO_ENABLED=0.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver registered for database/sql
)

// DB wraps a sql.DB connection to the SQLite checkpoint catalog.
type DB struct {
	db *sql.DB
	// w is the one connection every write goes through. SQLite takes one
	// writer at a time; the process's writers queue for this connection
	// rather than contend for the file's lock, where a writer that waited
	// past the busy timeout failed with SQLITE_BUSY (an instance's agents
	// recording their captures at once, under load). Reads keep the pool:
	// in WAL mode they do not wait for the writer.
	w *sql.DB
}

// Open opens (or creates) a SQLite database at path.
//
// The pragmas go in the DSN so that every connection of the pool gets them:
// set with Exec, they applied to one pooled connection only, and the others
// failed concurrent writes at once with "database is locked (SQLITE_BUSY)".
// Transactions take the write lock when they begin (_txlock=immediate): a
// read that upgrades to a write in WAL mode gets SQLITE_BUSY without waiting
// for the busy timeout. ":memory:" is one database per connection, so it
// gets a single connection.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	if path == ":memory:" {
		dsn = ":memory:?_pragma=busy_timeout(5000)"
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	writer := sqlDB
	if path == ":memory:" {
		sqlDB.SetMaxOpenConns(1)
	} else if writer, err = sql.Open("sqlite", dsn); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	writer.SetMaxOpenConns(1)
	d := &DB{db: sqlDB, w: writer}
	if err := sqlDB.Ping(); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := d.runMigrations(); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return d, nil
}

// Close closes the database connections.
func (d *DB) Close() error {
	err := d.db.Close()
	if d.w != d.db {
		if werr := d.w.Close(); err == nil {
			err = werr
		}
	}
	return err
}
