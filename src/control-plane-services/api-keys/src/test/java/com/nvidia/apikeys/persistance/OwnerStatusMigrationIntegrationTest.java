/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.nvidia.apikeys.persistance;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.tuple;

import com.datastax.driver.core.Session;
import com.datastax.driver.core.exceptions.InvalidQueryException;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import org.junit.jupiter.api.Test;

class OwnerStatusMigrationIntegrationTest {

    @Test
    void migrationAddsOwnerIndexesAndPreservesLegacyKeysWhenReapplied()
            throws IOException, InterruptedException {
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        // The deployment keyspace is separate from the suite's local schema.
        session.execute("CREATE KEYSPACE api_keys_api WITH replication = "
                + "{'class': 'SimpleStrategy', 'replication_factor': 1}");
        try {
            applyMigration(session, "03_init_tables.up.sql");
            session.execute("INSERT INTO api_keys_api.keys (api_key_hash, status, key_details)"
                    + " VALUES ('legacy-hash', 'ACTIVE', 'legacy-details')");
            session.execute("INSERT INTO api_keys_api.keys_by_owner_and_service"
                    + " (owner_type, owner_id, issuer_service_id, key_id, key_status)"
                    + " VALUES ('USER', 'alice', 'service-a', 'legacy-key', 'ACTIVE')");
            applyMigration(session, "04_add_multi_tenant_schema.up.sql");
            seedStatuses(session);

            waitForStatusesDiscoverable(session);

            assertLegacyKeysPreserved(session);
            applyMigration(session, "04_add_multi_tenant_schema.up.sql");
            assertStatusesDiscoverable(session);
            assertLegacyKeysPreserved(session);
        } finally {
            session.execute("DROP KEYSPACE api_keys_api");
        }
    }

    private static void waitForStatusesDiscoverable(Session session) throws InterruptedException {
        // Cassandra makes newly created indexes available asynchronously.
        long deadline = System.nanoTime() + Duration.ofSeconds(30).toNanos();
        while (true) {
            try {
                assertStatusesDiscoverable(session);
                return;
            } catch (AssertionError | InvalidQueryException error) {
                if (System.nanoTime() - deadline >= 0) {
                    throw error;
                }
            }
            Thread.sleep(100);
        }
    }

    private static void seedStatuses(Session session) {
        for (String table : new String[] {
                "owner_status_by_account", "owner_status_by_account_and_service"}) {
            String issuerColumn = table.endsWith("_and_service") ? ", issuer_service_id" : "";
            String issuerValue = table.endsWith("_and_service") ? ", 'service-a'" : "";
            String insert = "INSERT INTO api_keys_api." + table
                    + " (nca_id, owner_type, owner_id, owner_status" + issuerColumn + ") VALUES ";
            session.execute(insert + "('account-a', 'USER', 'alice', 'ACTIVE'" + issuerValue + ")");
            session.execute(insert + "('account-b', 'USER', 'alice', 'SUSPENDED'" + issuerValue + ")");
            session.execute(insert + "('account-a', 'USER', 'bob', 'SUSPENDED'" + issuerValue + ")");
        }
        session.execute("INSERT INTO api_keys_api.owner_status_by_account_and_service"
                + " (nca_id, owner_type, owner_id, issuer_service_id, owner_status)"
                + " VALUES ('account-a', 'USER', 'alice', 'service-b', 'SUSPENDED')");
    }

    private static void assertStatusesDiscoverable(Session session) {
        assertThat(session.execute("SELECT * FROM api_keys_api.keys_by_account_owner_and_service")
                .all()).isEmpty();
        assertThat(session.execute("SELECT nca_id, owner_type, owner_status FROM "
                + "api_keys_api.owner_status_by_account WHERE owner_id = 'alice'").all())
                .extracting(row -> row.getString("nca_id"), row -> row.getString("owner_type"),
                        row -> row.getString("owner_status"))
                .containsExactlyInAnyOrder(
                        tuple("account-a", "USER", "ACTIVE"),
                        tuple("account-b", "USER", "SUSPENDED"));
        assertThat(session.execute("SELECT nca_id, issuer_service_id, owner_status FROM "
                + "api_keys_api.owner_status_by_account_and_service WHERE owner_id = 'alice'")
                .all())
                .extracting(row -> row.getString("nca_id"), row -> row.getString("issuer_service_id"),
                        row -> row.getString("owner_status"))
                .containsExactlyInAnyOrder(
                        tuple("account-a", "service-a", "ACTIVE"),
                        tuple("account-a", "service-b", "SUSPENDED"),
                        tuple("account-b", "service-a", "SUSPENDED"));
        for (String table : new String[] {
                "owner_status_by_account", "owner_status_by_account_and_service"}) {
            assertThat(session.execute("SELECT nca_id, owner_status FROM api_keys_api."
                    + table + " WHERE owner_id = 'bob'").all())
                    .extracting(row -> row.getString("nca_id"), row -> row.getString("owner_status"))
                    .containsExactly(tuple("account-a", "SUSPENDED"));
        }
    }

    private static void assertLegacyKeysPreserved(Session session) {
        assertThat(session.execute("SELECT api_key_hash, status, key_details, nca_id"
                + " FROM api_keys_api.keys").all())
                .extracting(row -> row.getString("api_key_hash"), row -> row.getString("status"),
                        row -> row.getString("key_details"), row -> row.getString("nca_id"))
                .containsExactly(tuple("legacy-hash", "ACTIVE", "legacy-details", null));
        assertThat(session.execute("SELECT owner_type, owner_id, issuer_service_id, key_id, key_status"
                + " FROM api_keys_api.keys_by_owner_and_service").all())
                .extracting(row -> row.getString("owner_type"), row -> row.getString("owner_id"),
                        row -> row.getString("issuer_service_id"), row -> row.getString("key_id"),
                        row -> row.getString("key_status"))
                .containsExactly(tuple("USER", "alice", "service-a", "legacy-key", "ACTIVE"));
    }

    private static void applyMigration(Session session, String filename) throws IOException {
        try (var resource = OwnerStatusMigrationIntegrationTest.class
                .getResourceAsStream("/api_keys_api/" + filename)) {
            assertThat(resource).as("Deployment migration %s", filename).isNotNull();
            String cql = new String(resource.readAllBytes(), StandardCharsets.UTF_8);
            // These migrations contain plain DDL statements and line comments.
            for (String statement : cql.replaceAll("(?m)--.*$", "").split(";")) {
                if (!statement.isBlank()) {
                    session.execute(statement);
                }
            }
        }
    }
}
