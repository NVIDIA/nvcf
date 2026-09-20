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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.LockProviderService;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Consumer;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceLookupBackfillTaskTest {

    @Mock
    private InstanceV2Repository instanceRepository;

    @Mock
    private InstanceRequestV2Repository requestRepository;

    @Mock
    private LockProviderService lockProviderService;

    @Mock
    private IcmsConfigurationProperties configuration;

    private InstanceLookupBackfillTask task;

    @BeforeEach
    void setUp() {
        task = new InstanceLookupBackfillTask(
                instanceRepository, requestRepository, lockProviderService, configuration);
    }

    @Test
    void migrateCopiesMissingLookupValues() {
        var deploymentId = UUID.randomUUID();
        var gpuSpecificationId = UUID.randomUUID();
        var instance = instance("instance-1", "request-1", null, null);
        var request = request("request-1", deploymentId, gpuSpecificationId);
        provideInstances(instance);
        when(requestRepository.findRequestById("request-1"))
                .thenReturn(Optional.of(request));

        var summary = task.run(InstanceLookupBackfillMode.MIGRATE);

        verify(instanceRepository).update(instance);
        assertThat(instance.getDeploymentId()).isEqualTo(deploymentId);
        assertThat(instance.getGpuSpecificationId()).isEqualTo(gpuSpecificationId);
        assertThat(summary.migrated()).isEqualTo(1);
        assertThat(summary.isValid()).isTrue();
    }

    @Test
    void validateReportsConflictingLookupValuesWithoutWriting() {
        var instance = instance(
                "instance-1", "request-1", UUID.randomUUID(), UUID.randomUUID());
        var request = request("request-1", UUID.randomUUID(), UUID.randomUUID());
        provideInstances(instance);
        when(requestRepository.findRequestById("request-1"))
                .thenReturn(Optional.of(request));

        var summary = task.run(InstanceLookupBackfillMode.VALIDATE);

        verify(instanceRepository, never()).update(any());
        assertThat(summary.mismatched()).isEqualTo(1);
        assertThat(summary.isValid()).isFalse();
    }

    @Test
    void validateReportsMissingRequest() {
        var instance = instance("instance-1", "request-1", null, null);
        provideInstances(instance);
        when(requestRepository.findRequestById("request-1"))
                .thenReturn(Optional.empty());

        var summary = task.run(InstanceLookupBackfillMode.VALIDATE);

        assertThat(summary.missingRequest()).isEqualTo(1);
        assertThat(summary.isValid()).isFalse();
    }

    @Test
    void validateAllowsTerminatedInstanceWithDeletedRequest() {
        var instance = instance("instance-1", "request-1", null, null);
        instance.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        provideInstances(instance);
        when(requestRepository.findRequestById("request-1"))
                .thenReturn(Optional.empty());

        var summary = task.run(InstanceLookupBackfillMode.VALIDATE);

        assertThat(summary.skippedTerminated()).isEqualTo(1);
        assertThat(summary.isValid()).isTrue();
    }

    @Test
    void disabledDoesNotObtainLockOrScan() {
        when(configuration.getInstanceLookupBackfillTaskMode())
                .thenReturn(InstanceLookupBackfillMode.DISABLED);

        task.execute();

        verify(lockProviderService, never()).obtainLockWithTtl(any(), anyInt());
        verify(instanceRepository, never()).findAllInstancesAndApplyAction(any(), anyInt());
    }

    private void provideInstances(InstanceV2Entity... instances) {
        doAnswer(invocation -> {
            @SuppressWarnings("unchecked")
            var action = (Consumer<InstanceV2Entity>) invocation.getArgument(0);
            List.of(instances).forEach(action);
            return null;
        }).when(instanceRepository).findAllInstancesAndApplyAction(any(), anyInt());
    }

    private static InstanceV2Entity instance(
            String instanceId,
            String requestId,
            UUID deploymentId,
            UUID gpuSpecificationId) {
        return InstanceV2Entity.builder()
                .instanceId(instanceId)
                .requestId(requestId)
                .deploymentId(deploymentId)
                .gpuSpecificationId(gpuSpecificationId)
                .build();
    }

    private static InstanceRequestV2Entity request(
            String requestId,
            UUID deploymentId,
            UUID gpuSpecificationId) {
        return InstanceRequestV2Entity.builder()
                .requestId(requestId)
                .deploymentId(deploymentId)
                .gpuSpecificationId(gpuSpecificationId)
                .build();
    }
}
