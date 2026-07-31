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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ByocMessageGeneratorTest {

    @Mock
    private ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    private ByocServiceHelper byocServiceHelper;

    @Mock
    private com.nvidia.icms.service.InstanceServiceHelper instanceServiceHelper;

    private ByocMessageGenerator byocMessageGenerator;

    @BeforeEach
    void setUp() {
        byocMessageGenerator = new ByocMessageGenerator(
                byocConfigurationProperties, byocServiceHelper,
                instanceServiceHelper);
    }

    @Test
    void generateSqsMessageModelForByocFunction_forwardsModelsUnchanged() {
        when(byocConfigurationProperties.getEnv()).thenReturn("stage");

        var instanceRequest = SpotInstanceRequestSchema.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .ncaId("nca-id")
                .gpu("A100")
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .models("[{\"name\":\"model-1\"}]")
                .functionType(FunctionType.LLM)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();

        var destination = RequestInstanceDestination.builder()
                .instanceType(InstanceTypeV5Udt.builder()
                        .name(DUMMY_BYOC_INSTANCE_TYPE)
                        .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                        .gpuCount(8)
                        .build())
                .clusterGroupName("NVCA_TARGETING")
                .authorizedNcaIds(Set.of("nca-id"))
                .cloudProvider(CloudProvider.AWS)
                .gpuName("A100")
                .build();

        var message = byocMessageGenerator.generateSqsMessageModelForByocFunction(
                "request-id", 1, instanceRequest, destination, "customer");

        assertEquals(instanceRequest.getModels(), message.getLaunchSpecification().getModels());
    }

    @Test
    void generateSqsMessageModelForByocFunction_emitsLegacyActionWireString() {
        when(byocConfigurationProperties.getEnv()).thenReturn("stage");

        var instanceRequest = SpotInstanceRequestSchema.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .ncaId("nca-id")
                .gpu("A100")
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .functionType(FunctionType.LLM)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();

        var destination = RequestInstanceDestination.builder()
                .instanceType(InstanceTypeV5Udt.builder()
                        .name(DUMMY_BYOC_INSTANCE_TYPE)
                        .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                        .gpuCount(8)
                        .build())
                .clusterGroupName("NVCA_TARGETING")
                .authorizedNcaIds(Set.of("nca-id"))
                .cloudProvider(CloudProvider.AWS)
                .gpuName("A100")
                .build();

        var message = byocMessageGenerator.generateSqsMessageModelForByocFunction(
                "request-id", 1, instanceRequest, destination, "customer");

        assertEquals("RequestSpotInstances", message.getAction(),
                "BYOC SQS payload must carry the legacy 'RequestSpotInstances' wire string "
                        + "so NVCA's ApplyCreationMessage handler recognizes it.");
    }
}
