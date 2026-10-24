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
package com.nvidia.ess.metrics;

import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;

import com.nvidia.ess.constants.AuthorizationType;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.apache.commons.lang3.RandomUtils;

@ExtendWith(MockitoExtension.class)
class CustomMetricsRegistryTest {

    private final MeterRegistry meterRegistry = new SimpleMeterRegistry();

    private CustomMetricsRegistry customMetricsRegistry;

    @BeforeEach
    public void init() {
        customMetricsRegistry = new CustomMetricsRegistry();
        customMetricsRegistry.setMeterRegistry(meterRegistry);
    }

    @Test
    void recordSecretRead() {
        customMetricsRegistry.recordSecretRead(TEST_NAMESPACE, AuthorizationType.OAUTH, true);
    }

    @Test
    void recordSecretVersionsList() {
        customMetricsRegistry.recordSecretVersionsList(TEST_NAMESPACE, true);

    }

    @Test
    void recordSecretPathsList() {
        customMetricsRegistry.recordSecretPathsList(TEST_NAMESPACE, true);

    }

    @Test
    void recordSecretCreate() {
        customMetricsRegistry.recordSecretCreate(TEST_NAMESPACE, true);

    }

    @Test
    void recordSecretDelete() {
        customMetricsRegistry.recordSecretDelete(TEST_NAMESPACE, true);

    }

    @Test
    void recordSecretPayloadSize() {
        customMetricsRegistry.recordSecretPayloadSize(TEST_NAMESPACE, RandomUtils.insecure().randomInt());

    }

    @Test
    void recordRetryableError() {
        customMetricsRegistry.recordRetryableError(TEST_NAMESPACE, "exception cause/description");
    }

    @Test
    void recordExhaustedRetryableError() {
        customMetricsRegistry.recordExhaustedRetryableError(TEST_NAMESPACE, "exception cause/description");
    }

    @Test
    void recordPartialEntityDeletionOnPath() {
        customMetricsRegistry.recordPartialEntityDeletionOnPath(TEST_NAMESPACE);

    }

    @Test
    void recordPartialEntityDeletionOnEntity() {
        customMetricsRegistry.recordPartialEntityDeletionOnEntity(TEST_NAMESPACE);

    }

    @Test
    void recordPartialSecretDeletionOnPath() {
        customMetricsRegistry.recordPartialSecretDeletionOnPath(TEST_NAMESPACE);

    }

    @Test
    void recordLwtFailure() {
        customMetricsRegistry.recordNonRetryableLwtFailure(TEST_NAMESPACE, LwtOperation.PATH_DELETION);

    }

    @Test
    void recordRetryableLwtFailure() {
        customMetricsRegistry.recordRetryableLwtFailure(TEST_NAMESPACE, LwtOperation.SECRET_CREATION);
    }

    @Test
    void recordRetryablePartialSecretCreationOnVersion() {
        customMetricsRegistry.recordRetryablePartialSecretCreationOnVersion(TEST_NAMESPACE);
    }

    @Test
    void recordSecretCreateCasError() {
        customMetricsRegistry.recordSecretCreateCasError(TEST_NAMESPACE);
    }
}
