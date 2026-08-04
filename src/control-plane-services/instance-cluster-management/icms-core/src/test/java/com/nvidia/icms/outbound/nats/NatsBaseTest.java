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
package com.nvidia.icms.outbound.nats;

import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.TestUtil.getRandomStringWithPrefix;

import com.nvidia.icms.inbound.rest.model.TaskType;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel.ByocLaunchSpecification;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import com.nvidia.icms.outbound.sqs.model.TaskDetails;
import com.nvidia.icms.outbound.sqs.model.FunctionDetails;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.UUID;

public class NatsBaseTest {

    public static ByocSqsMessageModel getByocSqsMessageModel() {
        ByocLaunchSpecification byocLaunchSpecification = ByocLaunchSpecification.builder()
                .instanceType(getRandomStringWithPrefix("instanceType", 5))
                .instanceTypeName(getRandomStringWithPrefix("instanceTypeName", 5))
                .instanceTypeValue(getRandomStringWithPrefix("instanceTypeValue", 5))
                .instanceCount(1)
                .gpuType(getRandomStringWithPrefix("gpuType", 5))
                .requestedGPUCount(1)
                .containerImage(getRandomStringWithPrefix("containerImage", 5))
                .environment(getRandomStringWithPrefix("environment", 5))
                .spotEnvironment("test")
                .icmsEnvironment("test")
                .cloudProvider("test")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();

        return ByocSqsMessageModel.builder()
                .requestId(getRandomStringWithPrefix("requestId", 5))
                .sub(getRandomStringWithPrefix("customer", 5))
                .ncaId(getRandomStringWithPrefix("ncaId", 5))
                .action("RequestSpotInstances")
                .instanceType(byocLaunchSpecification.getInstanceType())
                .instanceCount(1)
                .gpuType(byocLaunchSpecification.getGpuType())
                .requestedGPUCount(1)
                .launchSpecification(byocLaunchSpecification)
                .accountName(getRandomStringWithPrefix("accountName", 5))
                .functionDetails(FunctionDetails.builder()
                        .functionId(UUID.randomUUID())
                        .functionVersionId(UUID.randomUUID())
                        .functionType(FunctionType.DEFAULT)
                        .build())
                .messageBatchId(generateRandomUUID())
                .build();
    }

    public static ByocSqsMessageModel getByocTaskSqsMessageModel() {
        ByocLaunchSpecification byocLaunchSpecification = ByocLaunchSpecification.builder()
                .instanceType(getRandomStringWithPrefix("instanceType", 5))
                .instanceTypeName(getRandomStringWithPrefix("instanceTypeName", 5))
                .instanceTypeValue(getRandomStringWithPrefix("instanceTypeValue", 5))
                .instanceCount(1)
                .gpuType(getRandomStringWithPrefix("gpuType", 5))
                .requestedGPUCount(1)
                .containerImage(getRandomStringWithPrefix("containerImage", 5))
                .environment(getRandomStringWithPrefix("environment", 5))
                .maxRuntimeDuration("PT1H")
                .maxQueuedDuration("PT1H")
                .terminationGracePeriodDuration("PT5M")
                .resultHandlingStrategy("STORE")
                .spotEnvironment("test")
                .icmsEnvironment("test")
                .cloudProvider("test")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();

        return ByocSqsMessageModel.builder()
                .requestId(getRandomStringWithPrefix("requestId", 5))
                .sub(getRandomStringWithPrefix("customer", 5))
                .ncaId(getRandomStringWithPrefix("ncaId", 5))
                .action("RequestSpotInstancesForTask")
                .instanceType(byocLaunchSpecification.getInstanceType())
                .instanceCount(1)
                .gpuType(byocLaunchSpecification.getGpuType())
                .requestedGPUCount(1)
                .launchSpecification(byocLaunchSpecification)
                .accountName(getRandomStringWithPrefix("accountName", 5))
                .taskDetails(TaskDetails.builder()
                        .taskId(UUID.randomUUID().toString())
                        .taskType(TaskType.CONTAINER)
                        .build())
                .messageBatchId(generateRandomUUID())
                .build();
    }

    public static ByocTerminatePodMessageModel getByocTerminatePodMessageModel() {
        Set<String> instanceIds = new HashSet<>();
        instanceIds.add(getRandomStringWithPrefix("instanceIds", 5));
        instanceIds.add(getRandomStringWithPrefix("instanceIds", 5));

        return ByocTerminatePodMessageModel.builder()
                .requestId(getRandomStringWithPrefix("requestId", 5))
                .instanceIds(instanceIds)
                .build();
    }

}
