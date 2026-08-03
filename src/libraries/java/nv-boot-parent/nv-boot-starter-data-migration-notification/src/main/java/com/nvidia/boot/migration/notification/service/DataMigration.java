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

package com.nvidia.boot.migration.notification.service;

import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * Annotate migration tasks with {@code @DataMigration} to document the schema changes and cleanup
 * expectations associated with the migration. The annotation is retained at runtime and can be
 * discovered by migration tooling or application code.
 *
 * <pre>{@code
 * @DataMigration(
 *         keyspace = "nvcf_api",
 *         newTables = {"table1", "table2"},
 *         newColumns = {"table1.field1"},
 *         requiresTables = {"gpu_specifications"},
 *         description = "Migrate GPU specs from functions_deployment_v2 to gpu_specifications")
 * public class AsyncFooMigration {
 * }
 * }</pre>
 *
 * Attributes:
 * <ul>
 *     <li>{@code keyspace}: Cassandra keyspace affected by the migration. Required.</li>
 *     <li>{@code newTables}: Tables created or introduced by the migration.</li>
 *     <li>{@code newColumns}: Columns created or introduced by the migration, usually in
 *     {@code table.column} format.</li>
 *     <li>{@code requiresTables}: Existing tables that must be present before the migration can
 *     run.</li>
 *     <li>{@code description}: Human-readable summary of the migration's purpose. Required.</li>
 * </ul>
 */
@Retention(RetentionPolicy.RUNTIME)
@Target(ElementType.TYPE)
public @interface DataMigration {

    String keyspace();

    String[] newTables() default {};

    String[] newColumns() default {};

    String[] requiresTables() default {};

    String description();
}
