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
package com.nvidia.nvct.service.instance;

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.util.TestConstants.GFN;
import static com.nvidia.nvct.util.TestConstants.T10;
import static com.nvidia.nvct.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.InstanceStateEnum;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService.Instance;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.InstanceState;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.Placement;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceServiceTest {

    private static final String INSTANCE_ID = "instance-1";
    private static final String LOCATION = "NP-LAX-03";
    private static final Instant CREATED_AT = Instant.parse("2026-05-28T12:00:00Z");

    @Mock
    private IcmsClient icmsClient;

    private InstanceService instanceService;

    @BeforeEach
    void beforeEach() {
        instanceService = new InstanceService(icmsClient);
    }

    @Test
    void getInstancesUsesTaskIdEndpoint() {
        var task = createTask(TEST_NCA_ID);
        when(icmsClient.getInstancesByTaskId(TEST_NCA_ID, TEST_TASK_ID_1))
                .thenReturn(List.of(createDeploymentInstance()));

        var instances = instanceService.getInstances(task);

        assertThat(instances).isPresent();
        assertThat(instances.get()).singleElement().satisfies(instance -> {
            assertThat(instance.getInstanceId()).isEqualTo(INSTANCE_ID);
            assertThat(instance.getTaskId()).isEqualTo(TEST_TASK_ID_1);
            assertThat(instance.getInstanceType()).isEqualTo(T10_INSTANCE_TYPE);
            assertThat(instance.getInstanceState()).isEqualTo(InstanceStateEnum.RUNNING);
            assertThat(instance.getIcmsRequestId()).isEqualTo(TEST_ICMS_REQ_ID_1);
            assertThat(instance.getNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(instance.getGpu()).isEqualTo(T10);
            assertThat(instance.getBackend()).isEqualTo(GFN);
            assertThat(instance.getLocation()).isEqualTo(LOCATION);
            assertThat(instance.getInstanceCreatedAt()).isEqualTo(CREATED_AT);
            assertThat(instance.getInstanceUpdatedAt()).isEqualTo(CREATED_AT);
        });
        verify(icmsClient).getInstancesByTaskId(TEST_NCA_ID, TEST_TASK_ID_1);
    }

    private static TaskEntity createTask(String ncaId) {
        return TaskEntity.builder()
                .taskId(TEST_TASK_ID_1)
                .ncaId(ncaId)
                .name(TEST_TASK_NAME_1)
                .status(RUNNING)
                .gpuSpec(GpuSpecUdt.builder()
                                 .backend(GFN)
                                 .gpu(T10)
                                 .instanceType(T10_INSTANCE_TYPE)
                                 .build())
                .maxQueuedDuration(Duration.ofHours(24))
                .terminalGracePeriodDuration(Duration.ofHours(1))
                .build();
    }

    private static Instance createDeploymentInstance() {
        return Instance.builder()
                .createTime(CREATED_AT)
                .instanceId(INSTANCE_ID)
                .cloudProvider(GFN)
                .instanceType(T10_INSTANCE_TYPE)
                .placement(Placement.builder()
                                   .availabilityZone(LOCATION)
                                   .build())
                .state(InstanceState.builder()
                               .name("running")
                               .build())
                .launchRequestId(TEST_ICMS_REQ_ID_1.toString())
                .build();
    }
}
