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

import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.driver.core.Row;
import com.datastax.driver.core.Session;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class MultiTenantSchemaIntegrationTest {

    private static final String MANAGEMENT_TABLE = "keys_by_account_owner_and_service";

    private static final List<String> SAI_INDEXES = List.of(
            "keys_by_scope_nca_idx",
            "keys_by_scope_owner_idx",
            "keys_by_scope_owner_type_idx",
            "keys_by_scope_service_idx",
            "keys_by_scope_status_idx",
            "keys_by_scope_created_at_idx");

    @Test
    void keysTableKeepsHashPartitionKeyAndAddsNcaId() {
        assertThat(partitionKeyColumns("keys")).containsExactly("api_key_hash");
        assertThat(clusteringColumns("keys")).isEmpty();
        assertThat(regularColumns("keys")).contains("nca_id", "status", "expires_at", "deletes_at",
                                                    "key_details");
    }

    @Test
    void tenantAwareManagementTableUsesAccountOwnerPartition() {
        assertThat(partitionKeyColumns(MANAGEMENT_TABLE))
                .containsExactly("nca_id", "owner_type", "owner_id");
        assertThat(clusteringColumns(MANAGEMENT_TABLE))
                .containsExactly("issuer_service_id", "key_id");
        assertThat(regularColumns(MANAGEMENT_TABLE))
                .contains("key_status", "created_at", "expires_at", "deletes_at", "key_details");
    }

    @Test
    void ownerStatusTablesAreKeyedByAccountAndOptionalIssuer() {
        assertThat(partitionKeyColumns("owner_status_by_account"))
                .containsExactly("nca_id", "owner_type", "owner_id");
        assertThat(clusteringColumns("owner_status_by_account")).isEmpty();

        assertThat(partitionKeyColumns("owner_status_by_account_and_service"))
                .containsExactly("nca_id", "owner_type", "owner_id", "issuer_service_id");
        assertThat(clusteringColumns("owner_status_by_account_and_service")).isEmpty();
    }

    @Test
    void bulkOperationTableIsKeyedByOperationId() {
        assertThat(partitionKeyColumns("key_operations_by_id"))
                .containsExactly("operation_id");
        assertThat(clusteringColumns("key_operations_by_id")).isEmpty();
        assertThat(regularColumns("key_operations_by_id"))
                .contains("nca_ids", "issuer_service_ids", "user_ids", "operation_status",
                          "paging_state", "cutoff_at");
    }

    @Test
    void managementTableHasStorageAttachedIndexes() {
        Map<String, Map<String, String>> indexes = indexesOn(MANAGEMENT_TABLE);

        assertThat(indexes.keySet()).containsExactlyInAnyOrderElementsOf(SAI_INDEXES);
        indexes.values().forEach(options -> assertThat(options.toString())
                .contains("StorageAttachedIndex"));
    }

    @Test
    void legacyOwnerIndexTableRemainsForDualWrite() {
        assertThat(partitionKeyColumns("keys_by_owner_and_service"))
                .containsExactly("owner_type", "owner_id");
        assertThat(clusteringColumns("keys_by_owner_and_service"))
                .containsExactly("issuer_service_id", "key_id");
    }

    private static List<String> partitionKeyColumns(String table) {
        return columns(table, "partition_key");
    }

    private static List<String> clusteringColumns(String table) {
        return columns(table, "clustering");
    }

    private static List<String> regularColumns(String table) {
        return columns(table, "regular");
    }

    private static List<String> columns(String table, String kind) {
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        var result = session.execute(
                "SELECT column_name, kind, position FROM system_schema.columns "
                        + "WHERE keyspace_name = ? AND table_name = ?",
                KEY_SPACE, table);
        return result.all().stream()
                .filter(row -> kind.equals(row.getString("kind")))
                .sorted((left, right) -> Integer.compare(
                        left.getInt("position"), right.getInt("position")))
                .map(row -> row.getString("column_name"))
                .toList();
    }

    private static Map<String, Map<String, String>> indexesOn(String table) {
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        var result = session.execute(
                "SELECT index_name, options FROM system_schema.indexes "
                        + "WHERE keyspace_name = ? AND table_name = ?",
                KEY_SPACE, table);
        return result.all().stream().collect(Collectors.toMap(
                row -> row.getString("index_name"),
                MultiTenantSchemaIntegrationTest::indexOptions));
    }

    private static Map<String, String> indexOptions(Row row) {
        return row.getMap("options", String.class, String.class);
    }
}
