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
package com.nvidia.ess.persistence.models;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.within;

import java.time.Instant;
import java.util.HashMap;
import org.junit.jupiter.api.Test;

/**
 * Tests to verify that @Builder.Default works correctly with @SuperBuilder
 * for the namespace model hierarchy, ensuring createdAt and updatedAt
 * default to Instant.now() when not explicitly set.
 */
class NamespaceModelDefaultsTest {

    @Test
    void namespaceWithoutEntityTypesModel_builderDefaults_shouldInitializeTimestamps() {
        // When: building without specifying createdAt or updatedAt
        NamespaceWithoutEntityTypesModel model = NamespaceWithoutEntityTypesModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .build();
        
        // Then: timestamps should be initialized (not null) and reasonable (not too old, not in future)
        Instant now = Instant.now();
        
        assertThat(model.getCreatedAt())
                .isNotNull()
                .describedAs("createdAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5)); // Allow 5s for clock skew
        
        assertThat(model.getUpdatedAt())
                .isNotNull()
                .describedAs("updatedAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5));
        
        // Verify timestamps are reasonably close to each other (same builder call)
        // Allow up to 1 second difference to account for any clock adjustments
        assertThat(model.getCreatedAt())
                .isCloseTo(model.getUpdatedAt(), within(1000, java.time.temporal.ChronoUnit.MILLIS));
    }

    @Test
    void entityTypeInNamespaceModel_builderDefaults_shouldInheritTimestampDefaults() {
        // When: building child class without specifying inherited timestamp fields
        EntityTypeInNamespaceModel model = EntityTypeInNamespaceModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .entityType(EntityTypeUdt.builder()
                        .name("test-entity-type")
                        .build())
                .build();
        
        // Then: inherited timestamp defaults should work
        Instant now = Instant.now();
        
        assertThat(model.getCreatedAt())
                .isNotNull()
                .describedAs("createdAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5));
        
        assertThat(model.getUpdatedAt())
                .isNotNull()
                .describedAs("updatedAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5));
    }

    @Test
    void namespaceModel_builderDefaults_shouldInheritTimestampDefaults() {
        // When: building child class without specifying inherited timestamp fields
        NamespaceModel model = NamespaceModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .entityTypes(new HashMap<>())
                .build();
        
        // Then: inherited timestamp defaults should work
        Instant now = Instant.now();
        
        assertThat(model.getCreatedAt())
                .isNotNull()
                .describedAs("createdAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5));
        
        assertThat(model.getUpdatedAt())
                .isNotNull()
                .describedAs("updatedAt should be recent (within last minute)")
                .isAfter(now.minusSeconds(60))
                .isBefore(now.plusSeconds(5));
    }

    @Test
    void namespaceWithoutEntityTypesModel_builderWithExplicitTimestamps_shouldUseProvidedValues() {
        // Given: specific timestamps
        Instant specificCreatedAt = Instant.parse("2020-01-01T00:00:00Z");
        Instant specificUpdatedAt = Instant.parse("2021-01-01T00:00:00Z");
        
        // When: building with explicit timestamps
        NamespaceWithoutEntityTypesModel model = NamespaceWithoutEntityTypesModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .createdAt(specificCreatedAt)
                .updatedAt(specificUpdatedAt)
                .build();
        
        // Then: should use the provided values, not defaults
        assertThat(model.getCreatedAt()).isEqualTo(specificCreatedAt);
        assertThat(model.getUpdatedAt()).isEqualTo(specificUpdatedAt);
    }

    @Test
    void entityTypeInNamespaceModel_builderWithExplicitTimestamps_shouldUseProvidedValues() {
        // Given: specific timestamps
        Instant specificCreatedAt = Instant.parse("2020-01-01T00:00:00Z");
        Instant specificUpdatedAt = Instant.parse("2021-01-01T00:00:00Z");
        
        // When: building with explicit timestamps
        EntityTypeInNamespaceModel model = EntityTypeInNamespaceModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .createdAt(specificCreatedAt)
                .updatedAt(specificUpdatedAt)
                .entityType(EntityTypeUdt.builder()
                        .name("test-entity-type")
                        .build())
                .build();
        
        // Then: should use the provided values, not defaults
        assertThat(model.getCreatedAt()).isEqualTo(specificCreatedAt);
        assertThat(model.getUpdatedAt()).isEqualTo(specificUpdatedAt);
    }

    @Test
    void namespaceModel_builderWithExplicitTimestamps_shouldUseProvidedValues() {
        // Given: specific timestamps
        Instant specificCreatedAt = Instant.parse("2020-01-01T00:00:00Z");
        Instant specificUpdatedAt = Instant.parse("2021-01-01T00:00:00Z");
        
        // When: building with explicit timestamps
        NamespaceModel model = NamespaceModel.builder()
                .namespace("test-namespace")
                .entityHashSize(10)
                .createdAt(specificCreatedAt)
                .updatedAt(specificUpdatedAt)
                .entityTypes(new HashMap<>())
                .build();
        
        // Then: should use the provided values, not defaults
        assertThat(model.getCreatedAt()).isEqualTo(specificCreatedAt);
        assertThat(model.getUpdatedAt()).isEqualTo(specificUpdatedAt);
    }
}
