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
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.assertj.core.api.Assertions.tuple;
import static org.awaitility.Awaitility.await;

import com.datastax.driver.core.Session;
import com.datastax.driver.core.exceptions.InvalidQueryException;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import org.junit.jupiter.api.Test;

class OwnerStatusMigrationIntegrationTest {

    @Test
    void migrationIndexesExistingStatusesAndCanBeReapplied() throws IOException {
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        // The deployment keyspace is separate from the suite's local schema.
        session.execute("CREATE KEYSPACE api_keys_api WITH replication = "
                + "{'class': 'SimpleStrategy', 'replication_factor': 1}");
        try {
            applyMigration(session, "03_init_tables.up.sql");
            applyMigration(session, "04_add_multi_tenant_schema.up.sql");
            seedStatuses(session);

            for (String table : new String[] {
                    "owner_status_by_account", "owner_status_by_account_and_service"}) {
                assertThatThrownBy(() -> session.execute("SELECT * FROM api_keys_api."
                        + table + " WHERE owner_id = 'alice'"))
                        .isInstanceOf(InvalidQueryException.class)
                        .hasMessageContaining("ALLOW FILTERING");
            }

            applyMigration(session, "05_add_owner_status_indexes.up.sql");
            // Cassandra builds new indexes over existing records asynchronously.
            await().atMost(Duration.ofSeconds(30))
                    .ignoreException(InvalidQueryException.class)
                    .untilAsserted(() -> assertStatusesDiscoverable(session));

            applyMigration(session, "05_add_owner_status_indexes.up.sql");
            assertStatusesDiscoverable(session);
        } finally {
            session.execute("DROP KEYSPACE api_keys_api");
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
        assertThat(session.execute("SELECT * FROM api_keys_api.keys").all()).isEmpty();
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
