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
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyOperationModel;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
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
    void updatePersistsProgressAndPagingState() {
        KeyOperationModel created = dao.create(operation().build());

        Instant later = TEST_TIME.plus(Duration.ofMinutes(5));
        TestClock.setBaseClock(TestClock.fixed(later, ZoneId.systemDefault()));
        dao.update(created.toBuilder()
                           .operationStatus(KeyOperationStatus.RUNNING)
                           .matchedCount(10L)
                           .completedCount(4L)
                           .failedCount(1L)
                           .selectionState("keys_by_owner_and_account_and_service")
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
    void createKeepsSuppliedIdStatusAndCounters() {
        UUID operationId = UUID.randomUUID();

        KeyOperationModel created = dao.create(operation()
                                                       .operationId(operationId)
                                                       .operationStatus(KeyOperationStatus.RUNNING)
                                                       .matchedCount(7L)
                                                       .completedCount(2L)
                                                       .failedCount(1L)
                                                       .build());

        assertThat(created.getOperationId()).isEqualTo(operationId);
        assertThat(dao.get(operationId))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getOperationStatus()).isEqualTo(KeyOperationStatus.RUNNING);
                    assertThat(stored.getMatchedCount()).isEqualTo(7L);
                    assertThat(stored.getCompletedCount()).isEqualTo(2L);
                    assertThat(stored.getFailedCount()).isEqualTo(1L);
                });
    }

    @Test
    void createDoesNotOverwriteExistingOperation() {
        UUID operationId = UUID.randomUUID();
        dao.create(operation().operationId(operationId).reason("first").build());

        assertThatThrownBy(() -> dao.create(operation()
                                                    .operationId(operationId)
                                                    .reason("second")
                                                    .build()))
                .isInstanceOf(CassandraException.class)
                .hasMessageContaining(operationId.toString());

        assertThat(dao.get(operationId))
                .get()
                .extracting(KeyOperationModel::getReason)
                .isEqualTo("first");
    }

    @Test
    void updateWithNullClearsPagingStateOnCompletion() {
        KeyOperationModel running = dao.update(dao.create(operation().build()).toBuilder()
                                                       .operationStatus(KeyOperationStatus.RUNNING)
                                                       .pagingState("opaque-paging-state")
                                                       .build());

        dao.update(running.toBuilder()
                           .operationStatus(KeyOperationStatus.COMPLETED)
                           .pagingState(null)
                           .build());

        assertThat(dao.get(running.getOperationId()))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getOperationStatus())
                            .isEqualTo(KeyOperationStatus.COMPLETED);
                    assertThat(stored.getPagingState()).isNull();
                    assertThat(stored.getReason()).isEqualTo("account offboarding");
                });
    }

    @Test
    void scopeSetsRoundTripWhenPartlyUnset() {
        KeyOperationModel created = dao.create(operation()
                                                       .issuerServiceIds(null)
                                                       .userIds(null)
                                                       .build());

        assertThat(dao.get(created.getOperationId()))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getNcaIds()).containsExactlyInAnyOrder("nca-1", "nca-2");
                    assertThat(stored.getIssuerServiceIds()).isNullOrEmpty();
                    assertThat(stored.getUserIds()).isNullOrEmpty();
                    assertThat(stored.getCutoffAt()).isEqualTo(TEST_TIME);
                    assertThat(stored.getActorType()).isEqualTo("SERVICE");
                    assertThat(stored.getActorId()).isEqualTo("service-admin");
                    assertThat(stored.getOperation()).isEqualTo("SUSPEND");
                });
    }

    @Test
    void getReturnsEmptyForUnknownOperation() {
        assertThat(dao.get(UUID.randomUUID())).isEmpty();
    }

    @Test
    void updateDoesNotCreateUnknownOperation() {
        UUID operationId = UUID.randomUUID();
        KeyOperationModel unknown = operation()
                .operationId(operationId)
                .operationStatus(KeyOperationStatus.RUNNING)
                .build();

        assertThrowsExceptionWithDetails(
                NotFoundException.class, () -> dao.update(unknown),
                "Key operation not found: " + operationId);
        assertThat(dao.get(operationId)).isEmpty();
    }

    @Test
    void concurrentCreatesWithSameIdHaveOneWinner() throws Exception {
        UUID operationId = UUID.randomUUID();
        int writers = 4;
        CountDownLatch start = new CountDownLatch(1);
        ExecutorService executor = Executors.newFixedThreadPool(writers);
        try {
            List<Future<KeyOperationModel>> results = new ArrayList<>();
            for (int i = 0; i < writers; i++) {
                String reason = "writer-" + i;
                results.add(executor.submit(() -> {
                    start.await();
                    return dao.create(operation().operationId(operationId).reason(reason).build());
                }));
            }
            start.countDown();

            List<KeyOperationModel> winners = new ArrayList<>();
            for (Future<KeyOperationModel> result : results) {
                try {
                    winners.add(result.get(30, TimeUnit.SECONDS));
                } catch (ExecutionException e) {
                    assertThat(e.getCause()).isInstanceOf(CassandraException.class);
                }
            }

            assertThat(winners).hasSize(1);
            assertThat(dao.get(operationId))
                    .get()
                    .extracting(KeyOperationModel::getReason)
                    .isEqualTo(winners.getFirst().getReason());
        } finally {
            executor.shutdownNow();
        }
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
