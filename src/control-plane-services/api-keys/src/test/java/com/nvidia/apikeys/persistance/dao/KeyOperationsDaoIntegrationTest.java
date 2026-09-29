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

package com.nvidia.apikeys.persistance.dao;

import static com.nvidia.apikeys.TestData.TEST_TIME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyOperationModel;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
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
class KeyOperationsDaoIntegrationTest {

    @Autowired
    private KeyOperationsDao dao;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void createAssignsDefaultsAndRoundTripsScope() {
        KeyOperationModel created = dao.create(operation().build());

        assertThat(created.getOperationId()).isNotNull();
        assertThat(created.getOperationStatus()).isEqualTo(KeyOperationStatus.PENDING);
        assertThat(created.getMatchedCount()).isZero();
        assertThat(created.getCompletedCount()).isZero();
        assertThat(created.getFailedCount()).isZero();
        assertThat(created.getCreatedAt()).isEqualTo(TEST_TIME);
        assertThat(created.getUpdatedAt()).isEqualTo(TEST_TIME);

        assertThat(dao.get(created.getOperationId())).contains(created);
    }

    @Test
    void createRejectsExistingOperationId() {
        UUID operationId = UUID.randomUUID();
        dao.create(operation().operationId(operationId).build());

        assertThatThrownBy(() -> dao.create(operation().operationId(operationId).build()))
                .isInstanceOf(CassandraException.class)
                .hasMessageContaining(operationId.toString());
    }

    @Test
    void updatePersistsProgressAndPagingState() {
        KeyOperationModel created = dao.create(operation().build());

        Instant later = TEST_TIME.plus(Duration.ofMinutes(5));
        TestClock.setBaseClock(TestClock.fixed(later, ZoneId.systemDefault()));
        dao.update(created.toBuilder()
                           .operationStatus(KeyOperationStatus.RUNNING)
                           .matchedCount(10L)
                           .completedCount(4L)
                           .failedCount(1L)
                           .selectionState("keys_by_account_owner_and_service")
                           .pagingState("opaque-paging-state")
                           .build());

        assertThat(dao.get(created.getOperationId()))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getOperationStatus()).isEqualTo(KeyOperationStatus.RUNNING);
                    assertThat(stored.getMatchedCount()).isEqualTo(10L);
                    assertThat(stored.getCompletedCount()).isEqualTo(4L);
                    assertThat(stored.getFailedCount()).isEqualTo(1L);
                    assertThat(stored.getPagingState()).isEqualTo("opaque-paging-state");
                    assertThat(stored.getCreatedAt()).isEqualTo(TEST_TIME);
                    assertThat(stored.getUpdatedAt()).isEqualTo(later);
                    assertThat(stored.getNcaIds()).containsExactlyInAnyOrder("nca-1", "nca-2");
                });
    }

    @Test
    void getReturnsEmptyForUnknownOperation() {
        assertThat(dao.get(UUID.randomUUID())).isEmpty();
    }

    private static KeyOperationModel.KeyOperationModelBuilder operation() {
        return KeyOperationModel.builder()
                .actorType("SERVICE")
                .actorId("service-admin")
                .operation("SUSPEND")
                .ncaIds(Set.of("nca-1", "nca-2"))
                .issuerServiceIds(Set.of("service-a"))
                .userIds(Set.of("owner-1@example.com"))
                .reason("account offboarding")
                .cutoffAt(TEST_TIME);
    }
}
