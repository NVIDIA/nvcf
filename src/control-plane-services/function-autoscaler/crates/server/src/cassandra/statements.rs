/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// locks table
pub(crate) fn get_select_locks_stmt(keyspace: &str) -> String {
    format!(
        "SELECT lock_name, node_id, acquired_at FROM {}.locks WHERE lock_name = ?;",
        keyspace
    )
}

pub(crate) fn get_delete_locks_stmt(keyspace: &str) -> String {
    format!("DELETE FROM {}.locks WHERE lock_name = ?;", keyspace)
}

// healthy_nodes table
pub(crate) fn get_select_all_nodes_stmt(keyspace: &str) -> String {
    format!(
        "SELECT node_id, last_updated_at FROM {}.healthy_nodes;",
        keyspace
    )
}

pub(crate) fn get_delete_node_stmt(keyspace: &str) -> String {
    format!("DELETE FROM {}.healthy_nodes WHERE node_id = ?;", keyspace)
}

// recently_invoked_functions Table
// account_id is a regular column, not part of PK
pub(crate) fn get_select_recently_invoked_functions_in_token_range_stmt(keyspace: &str) -> String {
    format!(
        "SELECT function_id, function_version_id, account_id \
         FROM {}.recently_invoked_functions \
         WHERE token(function_id, function_version_id) >= ? AND token(function_id, function_version_id) <= ?;",
        keyspace
    )
}

pub(crate) fn get_health_check_query_stmt(keyspace: &str) -> String {
    format!("SELECT now() from {}.healthy_nodes LIMIT 1;", keyspace)
}

// Inserts to the locks table must be done with a row TTL.
// Bind order: (lock_name, node_id, acquired_at, ttl_seconds)
pub(crate) fn get_stmt_insert_to_locks(keyspace: &str) -> String {
    format!(
        "INSERT INTO {}.locks (lock_name, node_id, acquired_at) VALUES (?, ?, ?) IF NOT EXISTS USING TTL ?",
        keyspace,
    )
}

// LWT conditional update — only refreshes TTL if node_id still matches this node.
// Refresh every non-key lock column so Cassandra's per-cell TTL semantics
// cannot leave a partially expired row behind.
// Returns [applied]=true if the row was updated, false if another node now owns the lock.
// Bind order: (ttl_seconds, node_id, acquired_at, lock_name, node_id)
pub(crate) fn get_stmt_refresh_lock(keyspace: &str) -> String {
    format!(
        "UPDATE {}.locks USING TTL ? SET node_id = ?, acquired_at = ? WHERE lock_name = ? IF node_id = ?",
        keyspace,
    )
}

// Inserts to the healthy_nodes table with a configurable row TTL (from CassandraSettings.node_health_ttl_seconds, default 180s).
// The node is pruned automatically after TTL expires if it stops reporting healthy.
pub(crate) fn get_stmt_insert_to_nodes(keyspace: &str, ttl_seconds: i32) -> String {
    format!(
        "INSERT INTO {}.healthy_nodes (node_id, last_updated_at) VALUES (?, ?) USING TTL {}",
        keyspace, ttl_seconds
    )
}

// Inserts to the recently_invoked_functions table with a configurable row TTL (from CassandraSettings.recently_invoked_ttl_seconds, default 1800s).
pub(crate) fn get_stmt_insert_to_recently_invoked_functions(
    keyspace: &str,
    ttl_seconds: i32,
) -> String {
    format!(
        "INSERT INTO {}.recently_invoked_functions (function_id, function_version_id, account_id, last_updated_at) VALUES (?, ?, ?, ?) USING TTL {}",
        keyspace,
        ttl_seconds
    )
}

#[cfg(test)]
mod tests {
    use super::{
        get_select_recently_invoked_functions_in_token_range_stmt,
        get_stmt_insert_to_recently_invoked_functions, get_stmt_refresh_lock,
    };

    #[test]
    fn refresh_lock_renews_every_non_key_column() {
        let statement = get_stmt_refresh_lock("test_keyspace");

        assert!(statement.contains("SET node_id = ?, acquired_at = ?"));
        assert!(statement.contains("IF node_id = ?"));
    }

    #[test]
    fn active_function_statements_preserve_table_and_ttl() {
        let select = get_select_recently_invoked_functions_in_token_range_stmt("test_keyspace");
        let insert = get_stmt_insert_to_recently_invoked_functions("test_keyspace", 1800);

        assert!(select.contains("FROM test_keyspace.recently_invoked_functions"));
        assert!(insert.contains("INTO test_keyspace.recently_invoked_functions"));
        assert!(insert.ends_with("USING TTL 1800"));
    }
}
