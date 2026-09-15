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
package com.nvidia.nvcf.service.metrics;

import static com.nvidia.nvcf.util.NvcfConstants.TAG_FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_FUNCTION_VERSION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_NCA_ID;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.MeterRegistry;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class FunctionDeploymentMetricsService {
    public static final String FUNCTION_INSTANCE_REQUEST_COUNTER_NAME = "nvcf.instance.request";
    public static final String FUNCTION_INSTANCE_DELETE_COUNTER_NAME = "nvcf.instance.delete";

    private final LoadingCache<FunctionDeploymentMetricsKey, Counter> functionInstanceRequestCounter;
    private final LoadingCache<FunctionDeploymentMetricsKey, Counter> functionInstanceDeleteCounter;

    public FunctionDeploymentMetricsService(MeterRegistry meterRegistry) {
        this.functionInstanceRequestCounter = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(45))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, counter, cause) -> {
                    if (counter instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(functionDeploymentMetricsKey -> Counter.builder(FUNCTION_INSTANCE_REQUEST_COUNTER_NAME)
                        .tag(TAG_FUNCTION_ID, functionDeploymentMetricsKey.functionId().toString())
                        .tag(TAG_FUNCTION_VERSION_ID, functionDeploymentMetricsKey.functionVersionId().toString())
                        .tag(TAG_NCA_ID, functionDeploymentMetricsKey.ncaId())
                        .tag(TAG_INSTANCE_TYPE, functionDeploymentMetricsKey.instanceType())
                        .register(meterRegistry));
        this.functionInstanceDeleteCounter = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(45))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, counter, cause) -> {
                    if (counter instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(functionDeploymentMetricsKey -> Counter.builder(FUNCTION_INSTANCE_DELETE_COUNTER_NAME)
                        .tag(TAG_FUNCTION_ID, functionDeploymentMetricsKey.functionId().toString())
                        .tag(TAG_FUNCTION_VERSION_ID, functionDeploymentMetricsKey.functionVersionId().toString())
                        .tag(TAG_NCA_ID, functionDeploymentMetricsKey.ncaId())
                        .tag(TAG_INSTANCE_TYPE, functionDeploymentMetricsKey.instanceType())
                        .register(meterRegistry));
    }

    public void recordFunctionDeploymentInstanceRequest(
            UUID functionId,
            UUID functionVersionId,
            String ncaId,
            String instanceType,
            int count
    ) {
        functionInstanceRequestCounter
                .get(
                        new FunctionDeploymentMetricsKey(
                                functionId,
                                functionVersionId,
                                ncaId,
                                instanceType
                        )
                )
                .increment(count);
    }

    public void recordFunctionDeploymentInstanceDelete(
            UUID functionId,
            UUID functionVersionId,
            String ncaId,
            String instanceType,
            int count
    ) {
        functionInstanceDeleteCounter
            .get(
                new FunctionDeploymentMetricsKey(
                        functionId,
                        functionVersionId,
                        ncaId,
                        instanceType
                )
            )
            .increment(count);
    }
}
