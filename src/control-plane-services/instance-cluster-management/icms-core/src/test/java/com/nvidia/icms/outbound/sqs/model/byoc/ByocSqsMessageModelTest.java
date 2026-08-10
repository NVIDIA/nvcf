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
package com.nvidia.icms.outbound.sqs.model.byoc;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel.ByocLaunchSpecification;
import com.nvidia.icms.outbound.sqs.model.FunctionDetails;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import io.swagger.v3.oas.annotations.media.Schema;
import org.junit.jupiter.api.Test;

import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;

class ByocSqsMessageModelTest {

    private final JsonMapper objectMapper = new JsonMapper();

    @Test
    void testByocSqsMessageModelBuilder() throws Exception {
        // Test 1: Build with inline launch specification
        ByocSqsMessageModel byocSqsMessageModel = ByocSqsMessageModel.builder()
                .requestId("test-request-id")
                .sub("test-customer")
                .ncaId("test-nca-id")
                .action("RequestSpotInstances")
                .instanceType("test-instance-type")
                .instanceCount(2)
                .gpuType("test-gpu-type")
                .requestedGPUCount(1)
                .launchSpecification(ByocLaunchSpecification.builder()
                        .instanceType("test-instance-type")
                        .instanceTypeName("test-instance-type-name")
                        .instanceTypeValue("test-instance-type-value")
                        .instanceCount(2)
                        .gpuType("test-gpu-type")
                        .requestedGPUCount(1)
                        .containerImage("test-container-image")
                        .environment("test-environment")
                        .spotEnvironment("test")
                        .icmsEnvironment("test")
                        .cloudProvider("test")
                        .deploymentId(UUID.randomUUID())
                        .gpuSpecificationId(UUID.randomUUID())
                        .build())
                .accountName("test-account-name")
                .functionDetails(FunctionDetails.builder()
                        .functionId(UUID.randomUUID())
                        .functionVersionId(UUID.randomUUID())
                        .functionType(FunctionType.DEFAULT)
                        .build())
                .messageBatchId("test-message-batch-id")
                .build();

        // Serialize to JSON and parse back
        String jsonString = objectMapper.writeValueAsString(byocSqsMessageModel);
        JsonNode jsonNode = objectMapper.readTree(jsonString);

        // Validate all fields directly from JSON
        assertEquals("test-request-id", jsonNode.get("requestId").asText());
        assertEquals("test-customer", jsonNode.get("sub").asText());
        assertEquals("test-nca-id", jsonNode.get("ncaId").asText());
        assertEquals("RequestSpotInstances", jsonNode.get("action").asText());
        assertEquals("test-instance-type", jsonNode.get("instanceType").asText());
        assertEquals(2, jsonNode.get("instanceCount").asInt());
        assertEquals("test-gpu-type", jsonNode.get("gpuType").asText());
        assertEquals(1, jsonNode.get("requestedGPUCount").asInt());
        assertEquals("test-account-name", jsonNode.get("accountName").asText());
        assertEquals("test-message-batch-id", jsonNode.get("messageBatchId").asText());

        // Validate launchSpecification object
        JsonNode launchSpecNode = jsonNode.get("launchSpecification");
        assertNotNull(launchSpecNode);
        assertEquals("test-instance-type", launchSpecNode.get("instanceType").asText());
        assertEquals("test-instance-type-name", launchSpecNode.get("instanceTypeName").asText());
        assertEquals("test-instance-type-value", launchSpecNode.get("instanceTypeValue").asText());
        assertEquals(2, launchSpecNode.get("instanceCount").asInt());
        assertEquals("test-gpu-type", launchSpecNode.get("gpuType").asText());
        assertEquals(1, launchSpecNode.get("requestedGPUCount").asInt());
        assertEquals("test-container-image", launchSpecNode.get("containerImage").asText());
        assertEquals("test-environment", launchSpecNode.get("environment").asText());
        assertEquals("test", launchSpecNode.get("spotEnvironment").asText());
        assertEquals("test", launchSpecNode.get("icmsEnvironment").asText());
        assertEquals("test", launchSpecNode.get("cloudProvider").asText());
        assertNotNull(launchSpecNode.get("deploymentId"));
        assertNotNull(launchSpecNode.get("gpuSpecificationId"));

        // Validate functionDetails object
        JsonNode functionDetailsNode = jsonNode.get("functionDetails");
        assertNotNull(functionDetailsNode);
        assertNotNull(functionDetailsNode.get("functionId"));
        assertNotNull(functionDetailsNode.get("functionVersionId"));
        assertEquals("DEFAULT", functionDetailsNode.get("functionType").asText());

        // Test 2: Build with pre-built launch specification object
        ByocLaunchSpecification preBuiltLaunchSpec = ByocLaunchSpecification.builder()
                .instanceType("prebuilt-instance-type")
                .instanceTypeName("prebuilt-instance-type-name")
                .instanceTypeValue("prebuilt-instance-type-value")
                .instanceCount(3)
                .gpuType("prebuilt-gpu-type")
                .requestedGPUCount(2)
                .containerImage("prebuilt-container-image")
                .environment("prebuilt-environment")
                .spotEnvironment("prebuilt")
                .icmsEnvironment("prebuilt")
                .cloudProvider("prebuilt")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();

        ByocSqsMessageModel byocSqsMessageModel2 = ByocSqsMessageModel.builder()
                .requestId("test-request-id-2")
                .sub("test-customer-2")
                .ncaId("test-nca-id-2")
                .action("RequestSpotInstances")
                .instanceType("prebuilt-instance-type")
                .instanceCount(3)
                .gpuType("prebuilt-gpu-type")
                .requestedGPUCount(2)
                .launchSpecification(preBuiltLaunchSpec)
                .accountName("test-account-name-2")
                .functionDetails(FunctionDetails.builder()
                        .functionId(UUID.randomUUID())
                        .functionVersionId(UUID.randomUUID())
                        .functionType(FunctionType.DEFAULT)
                        .build())
                .messageBatchId("test-message-batch-id-2")
                .build();

        // Serialize to JSON and parse back for the second model
        String jsonString2 = objectMapper.writeValueAsString(byocSqsMessageModel2);
        JsonNode jsonNode2 = objectMapper.readTree(jsonString2);

        // Validate the second model structure and key fields
        assertNotNull(jsonNode2);
        assertTrue(jsonNode2.has("launchSpecification"));
        assertTrue(jsonNode2.has("functionDetails"));
        
        // Validate launchSpecification is properly nested in the second model
        JsonNode launchSpecNode2 = jsonNode2.get("launchSpecification");
        assertNotNull(launchSpecNode2);
        assertEquals("prebuilt-instance-type", launchSpecNode2.get("instanceType").asText());
        assertEquals("prebuilt-instance-type-name", launchSpecNode2.get("instanceTypeName").asText());
        assertEquals("prebuilt-instance-type-value", launchSpecNode2.get("instanceTypeValue").asText());
        assertEquals(3, launchSpecNode2.get("instanceCount").asInt());
        assertEquals("prebuilt-gpu-type", launchSpecNode2.get("gpuType").asText());
        assertEquals(2, launchSpecNode2.get("requestedGPUCount").asInt());
        assertEquals("prebuilt-container-image", launchSpecNode2.get("containerImage").asText());
        assertEquals("prebuilt-environment", launchSpecNode2.get("environment").asText());
        assertEquals("prebuilt", launchSpecNode2.get("spotEnvironment").asText());
        assertEquals("prebuilt", launchSpecNode2.get("icmsEnvironment").asText());
        assertEquals("prebuilt", launchSpecNode2.get("cloudProvider").asText());
        assertNotNull(launchSpecNode2.get("deploymentId"));
        assertNotNull(launchSpecNode2.get("gpuSpecificationId"));
    }

    @Test
    void testByocLaunchSpecificationBuilder() {
        var byocLaunchSpecification = ByocLaunchSpecification.builder()
                .instanceType("test-instance-type")
                .instanceTypeName("test-instance-type-name")
                .instanceTypeValue("test-instance-type-value")
                .instanceCount(2)
                .gpuType("test-gpu-type")
                .requestedGPUCount(1)
                .containerImage("test-container-image")
                .environment("test-environment")
                .spotEnvironment("test")
                .icmsEnvironment("test")
                .cloudProvider("test")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();

        assertNotNull(byocLaunchSpecification);
        assertEquals("test-instance-type", byocLaunchSpecification.getInstanceType());
        assertEquals("test-instance-type-name", byocLaunchSpecification.getInstanceTypeName());
        assertEquals("test-instance-type-value", byocLaunchSpecification.getInstanceTypeValue());
        assertEquals(2, byocLaunchSpecification.getInstanceCount());
        assertEquals("test-gpu-type", byocLaunchSpecification.getGpuType());
        assertEquals(1, byocLaunchSpecification.getRequestedGPUCount());
        assertEquals("test-container-image", byocLaunchSpecification.getContainerImage());
        assertEquals("test-environment", byocLaunchSpecification.getEnvironment());
        assertEquals("test", byocLaunchSpecification.getSpotEnvironment());
        assertEquals("test", byocLaunchSpecification.getIcmsEnvironment());
        assertEquals("test", byocLaunchSpecification.getCloudProvider());
        assertNotNull(byocLaunchSpecification.getDeploymentId());
        assertNotNull(byocLaunchSpecification.getGpuSpecificationId());
    }
}