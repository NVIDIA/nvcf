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

package com.nvidia.apikeys.config;

import static org.assertj.core.api.Assertions.assertThat;

import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class NakPropertiesTest {

    @ParameterizedTest
    @CsvSource({
            "true, false, true",
            "false, false, true",
            "false, true, true",
            "true, true, false",
    })
    void legacyKeyMigrationRequiresV1KeyManagementDisabled(
            boolean v1KeyManagementEnabled, boolean migrationEnabled, boolean allowed) {
        var properties = properties(v1KeyManagementEnabled, migrationEnabled);

        assertThat(properties.isLegacyKeyMigrationAllowed()).isEqualTo(allowed);
    }

    @ParameterizedTest
    @CsvSource({"true, true", "false, true"})
    void validationReportsTheConflict(boolean v1KeyManagementEnabled, boolean migrationEnabled) {
        try (var factory = Validation.buildDefaultValidatorFactory()) {
            var messages = factory.getValidator()
                    .validate(properties(v1KeyManagementEnabled, migrationEnabled)).stream()
                    .map(ConstraintViolation::getMessage)
                    .toList();

            assertThat(messages.contains(
                    "migration.legacy-keys.enabled requires v1-key-management-enabled=false"))
                    .isEqualTo(v1KeyManagementEnabled);
        }
    }

    private static NakProperties properties(
            boolean v1KeyManagementEnabled, boolean migrationEnabled) {
        var migration = new MigrationProperties();
        migration.getLegacyKeys().setEnabled(migrationEnabled);
        return NakProperties.builder()
                .v1KeyManagementEnabled(v1KeyManagementEnabled)
                .migration(migration)
                .build();
    }
}
