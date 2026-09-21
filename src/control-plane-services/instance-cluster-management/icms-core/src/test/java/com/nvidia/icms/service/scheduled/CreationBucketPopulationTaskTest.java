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
package com.nvidia.icms.service.scheduled;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.function.Consumer;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CreationBucketPopulationTaskTest {

    @Mock
    private InstanceRequestV2Repository requestRepository;
    @Mock
    private InstanceV2Repository instanceRepository;
    @Mock
    private IcmsConfigurationProperties configuration;

    private CreationBucketPopulationTask task;

    @BeforeEach
    void setUp() {
        task = new CreationBucketPopulationTask(
                requestRepository, instanceRepository, configuration);
        when(configuration.getDatabaseReadPageSize()).thenReturn(500);
    }

    @Test
    void execute_populatesMissingBucketsAndSkipsExistingBuckets() {
        Instant creationTime = Instant.parse("2026-09-15T18:42:31Z");
        Instant expectedBucket = creationTime.truncatedTo(ChronoUnit.DAYS);
        var missingRequest = InstanceRequestV2Entity.builder()
                .requestId("request-1")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(creationTime))
                .build();
        var populatedRequest = InstanceRequestV2Entity.builder()
                .requestId("request-2")
                .creationBucket(expectedBucket)
                .build();
        var missingInstance = InstanceV2Entity.builder()
                .instanceId("instance-1")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(creationTime))
                .build();

        doAnswer(invocation -> {
            Consumer<InstanceRequestV2Entity> action = invocation.getArgument(0);
            action.accept(missingRequest);
            action.accept(populatedRequest);
            return null;
        }).when(requestRepository).findAllRequestsAndApplyAction(any(), eq(5), eq(0), eq(500));
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            action.accept(missingInstance);
            return null;
        }).when(instanceRepository).findAllInstancesAndApplyAction(any(), eq(5));
        var result = task.execute();

        assertEquals(new CreationBucketPopulationTask.PopulationResult(1, 0, 1, 0), result);
        assertEquals(expectedBucket, missingRequest.getCreationBucket());
        assertEquals(expectedBucket, missingInstance.getCreationBucket());
        verify(requestRepository).update(missingRequest);
        verify(instanceRepository).update(missingInstance);
    }

    @Test
    void execute_continuesAfterMalformedRecords() {
        var request = InstanceRequestV2Entity.builder().requestId("request-1").build();
        var instance = InstanceV2Entity.builder().instanceId("instance-1").build();
        doAnswer(invocation -> {
            Consumer<InstanceRequestV2Entity> action = invocation.getArgument(0);
            action.accept(request);
            return null;
        }).when(requestRepository).findAllRequestsAndApplyAction(any(), eq(5), eq(0), eq(500));
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            action.accept(instance);
            return null;
        }).when(instanceRepository).findAllInstancesAndApplyAction(any(), eq(5));

        var result = task.execute();

        assertEquals(new CreationBucketPopulationTask.PopulationResult(0, 1, 0, 1), result);
    }
}
