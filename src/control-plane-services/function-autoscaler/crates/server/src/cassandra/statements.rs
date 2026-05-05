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

// Default TTL values — now configurable via CassandraSettings

#[derive(Debug, Clone, Copy)]
pub enum ActiveFunctionTable {
    RecentlyInvokedFunctions,
    RunningFunctionsWithoutInvocations,
}

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
// nca_id_string is a regular column, not part of PK
pub(crate) fn get_select_recently_invoked_functions_in_token_range_stmt(keyspace: &str) -> String {
    format!(
        "SELECT function_id, function_version_id, nca_id_string \
         FROM {}.recently_invoked_functions \
         WHERE token(function_id, function_version_id) >= ? AND token(function_id, function_version_id) <= ?;",
        keyspace
    )
}

pub(crate) fn get_delete_recently_invoked_function_stmt(keyspace: &str) -> String {
    format!(
        "DELETE FROM {}.recently_invoked_functions \
         WHERE function_id = ? AND function_version_id = ?;",
        keyspace
    )
}

// running_functions_without_invocations Table
// nca_id_string is a regular column, not part of PK
pub(crate) fn get_select_running_functions_without_invocations_in_token_range_stmt(
    keyspace: &str,
) -> String {
    format!(
        "SELECT function_id, function_version_id, nca_id_string \
         FROM {}.running_functions_without_invocations \
         WHERE token(function_id, function_version_id) >= ? AND token(function_id, function_version_id) <= ?;",
        keyspace
    )
}

pub(crate) fn get_delete_running_function_without_invocations_stmt(keyspace: &str) -> String {
    format!(
        "DELETE FROM {}.running_functions_without_invocations \
         WHERE function_id = ? AND function_version_id = ?;",
        keyspace
    )
}

// recently_invoked_functions_history Table
// nca_id_string is a regular column, not part of PK
pub(crate) fn get_select_recently_invoked_function_history_by_id_stmt(keyspace: &str) -> String {
    format!(
        "SELECT function_id, function_version_id, nca_id_string, num_workers, \
         last_predicted_desired_instance_count, \
         last_predicted_error_code, last_updated_at \
         FROM {}.recently_invoked_functions_history \
         WHERE function_id = ? AND function_version_id = ? LIMIT 1;",
        keyspace
    )
}

pub(crate) fn get_delete_recently_invoked_function_history_pk_stmt(keyspace: &str) -> String {
    format!(
        "DELETE FROM {}.recently_invoked_functions_history \
         WHERE function_id = ? AND function_version_id = ?;",
        keyspace
    )
}

pub(crate) fn get_insert_recently_invoked_functions_history_pk_stmt(keyspace: &str) -> String {
    format!(
        "INSERT INTO {}.recently_invoked_functions_history (function_id, function_version_id, nca_id_string, num_workers) \
         VALUES (?, ?, ?, ?)",
        keyspace
    )
}

// running_functions_without_invocations_history Table
// nca_id_string is a regular column, not part of PK
pub(crate) fn get_select_running_function_without_invocations_history_by_id_stmt(
    keyspace: &str,
) -> String {
    format!(
        "SELECT function_id, function_version_id, nca_id_string, num_workers, \
         last_predicted_desired_instance_count, \
         last_predicted_error_code, last_updated_at \
         FROM {}.running_functions_without_invocations_history \
         WHERE function_id = ? AND function_version_id = ? LIMIT 1;",
        keyspace
    )
}

pub(crate) fn get_delete_running_function_without_invocations_history_pk_stmt(
    keyspace: &str,
) -> String {
    format!(
        "DELETE FROM {}.running_functions_without_invocations_history \
         WHERE function_id = ? AND function_version_id = ?;",
        keyspace
    )
}

pub(crate) fn get_insert_running_functions_without_invocations_history_pk_stmt(
    keyspace: &str,
) -> String {
    format!(
        "INSERT INTO {}.running_functions_without_invocations_history (function_id, function_version_id, nca_id_string, num_workers) \
         VALUES (?, ?, ?, ?)",
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
// CQL UPDATE requires a SET clause; we set node_id to itself as a no-op.
// Returns [applied]=true if the row was updated, false if another node now owns the lock.
// Bind order: (ttl_seconds, node_id, lock_name, node_id)
pub(crate) fn get_stmt_refresh_lock(keyspace: &str) -> String {
    format!(
        "UPDATE {}.locks USING TTL ? SET node_id = ? WHERE lock_name = ? IF node_id = ?",
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
        "INSERT INTO {}.recently_invoked_functions (function_id, function_version_id, nca_id_string, last_updated_at) VALUES (?, ?, ?, ?) USING TTL {}",
        keyspace,
        ttl_seconds
    )
}

// Inserts to the running_functions_without_invocations table with a configurable row TTL (from CassandraSettings.recently_invoked_ttl_seconds, default 1800s).
// If function discovery logic doesn't report the function as active, the row is pruned automatically after the TTL expires.
pub(crate) fn get_stmt_insert_to_running_functions_without_invocations(
    keyspace: &str,
    ttl_seconds: i32,
) -> String {
    format!(
        "INSERT INTO {}.running_functions_without_invocations (function_id, function_version_id, nca_id_string, last_updated_at) VALUES (?, ?, ?, ?) USING TTL {}",
        keyspace,
        ttl_seconds
    )
}

// Inserts to the recently_invoked_functions_history table must be done with a row TTL of 180 seconds.
// If function discovery logic doesn't report the function as active, the row is pruned automatically after 180 seconds.
// The table itself has no default TTL and is kept for historical context if needed.
// We want to add a configurable job to prune the table periodically for inactive rows.
pub(crate) fn get_stmt_str_insert_to_recently_invoked_functions_history_prediction_row(
    keyspace: &str,
    ttl_seconds: i32,
) -> String {
    format!(
        "INSERT INTO {}.recently_invoked_functions_history (function_id, function_version_id, nca_id_string, \
         num_workers, last_predicted_desired_instance_count, \
         last_predicted_error_code, last_updated_at) \
         VALUES (?, ?, ?, ?, ?, ?, ?) USING TTL {}",
        keyspace, ttl_seconds
    )
}

// Inserts to the running_functions_without_invocations_history table must be done with a row TTL of 300 seconds.
// If function discovery logic doesn't report the function as active, the row is pruned automatically after 300 seconds.
// The table itself has no default TTL and is kept for historical context if needed.
// We want to add a configurable job to prune the table periodically for inactive rows.
pub(crate) fn get_stmt_str_insert_to_running_functions_without_invocations_history_prediction_row(
    keyspace: &str,
    ttl_seconds: i32,
) -> String {
    format!(
        "INSERT INTO {}.running_functions_without_invocations_history (function_id, function_version_id, nca_id_string, \
         num_workers, last_predicted_desired_instance_count, \
         last_predicted_error_code, last_updated_at) \
         VALUES (?, ?, ?, ?, ?, ?, ?) USING TTL {}",
        keyspace, ttl_seconds
    )
}
