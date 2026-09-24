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
package com.nvidia.nvcf.service.scheduler;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.ACTIVE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.configuration.scheduler.FunctionDeploymentsTaskProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionDeploymentReconciliationService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.GracefulDeploymentCleanupService;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class FunctionDeploymentsTaskOwnershipTest {

    private static final List<String> REGIONS =
            List.of("us-east-1", "us-west-2", "eu-west-1");
    private static final UUID DEPLOYMENT_ID =
            UUID.fromString("10000000-0000-0000-0000-000000000000");
    private static final UUID FUNCTION_ID =
            UUID.fromString("20000000-0000-0000-0000-000000000000");

    @Mock
    private FunctionLookupService functionLookupService;

    @Mock
    private GracefulDeploymentCleanupService gracefulDeploymentCleanupService;

    @Mock
    private FunctionDeploymentReconciliationService functionDeploymentReconciliationService;

    @Mock
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Mock
    private Tracer tracer;

    @Mock
    private FunctionDeploymentService functionDeploymentService;

    private final List<FunctionDeploymentsTask> tasks = new ArrayList<>();

    @AfterEach
    void closeTasks() {
        tasks.forEach(FunctionDeploymentsTask::close);
    }

    @Test
    void shouldAssignDeploymentsUsingUuidHash() {
        var expectedOwners = Map.of(
                UUID.fromString("00000000-0000-0000-0000-000000000000"), "us-east-1",
                UUID.fromString("00000000-0000-0000-0000-000000000001"), "us-west-2",
                UUID.fromString("00000000-0000-0000-0000-000000000002"), "eu-west-1",
                UUID.fromString("00000000-0000-0000-0000-0000e0000000"), "us-west-2");
        var regionalTasks = REGIONS.stream()
                .map(region -> Map.entry(region, task(region)))
                .toList();

        expectedOwners.forEach((functionVersionId, expectedOwner) -> {
            var deployment = deployment(functionVersionId);

            assertThat(regionalTasks.stream()
                               .filter(entry -> entry.getValue().owns(deployment))
                               .map(Map.Entry::getKey))
                    .containsExactly(expectedOwner);
        });
    }

    @Test
    void shouldPassCurrentRegionToReconciliation() {
        var currentRegion = "us-west-2";
        var function = mock(FunctionEntity.class);
        var deploymentContext = mock(FunctionDeploymentContext.class);
        var span = mock(Span.class);
        when(function.getFunctionStatus()).thenReturn(ACTIVE);

        task(currentRegion).handleFunctionDeployment(function, deploymentContext, span);

        verify(functionDeploymentReconciliationService)
                .reconcile(function, deploymentContext, currentRegion);
    }

    private FunctionDeploymentsTask task(String currentRegion) {
        var taskProperties = new FunctionDeploymentsTaskProperties();
        taskProperties.setCurrentRegion(currentRegion);
        taskProperties.setRegions(REGIONS);
        taskProperties.setMaxConcurrency(1);
        var task = new FunctionDeploymentsTask(
                functionLookupService,
                gracefulDeploymentCleanupService,
                taskProperties,
                functionDeploymentReconciliationService,
                functionDeploymentLookupService,
                tracer,
                functionDeploymentService);
        tasks.add(task);
        return task;
    }

    private static FunctionDeploymentEntity deployment(UUID functionVersionId) {
        return FunctionDeploymentEntity.builder()
                .key(FunctionDeploymentKey.builder()
                             .functionVersionId(functionVersionId)
                             .build())
                .deploymentId(DEPLOYMENT_ID)
                .functionId(FUNCTION_ID)
                .ncaId("test-nca")
                .build();
    }
}
