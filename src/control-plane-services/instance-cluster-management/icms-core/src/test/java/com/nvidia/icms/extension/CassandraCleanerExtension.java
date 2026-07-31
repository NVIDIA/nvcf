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
package com.nvidia.icms.extension;


import com.datastax.driver.core.ResultSet;
import com.datastax.driver.core.Session;
import com.nvidia.icms.integration.IntegrationTest;
import org.junit.jupiter.api.extension.BeforeEachCallback;
import org.junit.jupiter.api.extension.ExtensionContext;

public class CassandraCleanerExtension implements BeforeEachCallback {

    private static final String KEY_SPACE = "test";

    private static final String GET_ALL_TABLE_NAMES_QUERY
            =
            "SELECT table_name FROM system_schema.tables WHERE keyspace_name = '" + KEY_SPACE + "'";

    private static final String TRUNCATE_TABLE_QUERY_TEMPLATE =
            "truncate table " + KEY_SPACE + ".%s";

    @Override
    public void beforeEach(ExtensionContext context) {
        cleanDatabase(context);
    }

    private void cleanDatabase(ExtensionContext context) {
        Session cqlSession = IntegrationTest.CQL_SESSION;
        int  maxRetries = 5;
        for(int i = 0; i < maxRetries; i++) { // 5 retries
            try {
                ResultSet resultSet =
                        cqlSession.execute(GET_ALL_TABLE_NAMES_QUERY);
                resultSet.forEach(row -> cqlSession
                        .execute(String.format(TRUNCATE_TABLE_QUERY_TEMPLATE,
                                               row.getString("table_name"))));
                break;
            } catch (Exception e) {
                System.out.println("Error of pod cleaning up");
                System.out.println(e);
                if (i == maxRetries - 1) { // last attempt
                    throw e;
                }
            }
        }
    }
}
