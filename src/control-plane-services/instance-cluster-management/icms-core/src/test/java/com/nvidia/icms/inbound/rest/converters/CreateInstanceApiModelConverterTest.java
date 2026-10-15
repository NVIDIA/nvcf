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
package com.nvidia.icms.inbound.rest.converters;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceRequestApiModel;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceLaunchSpecificationApiModel;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceLaunchSpecificationPlacementApiModel;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceFunctionDetailsApiModel;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceTaskDetailsApiModel;
import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.EnumSource;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Duration;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.*;

@ExtendWith(MockitoExtension.class)
class CreateInstanceApiModelConverterTest {

    private CreateInstanceApiModelConverter converter;

    @BeforeEach
    void setUp() {
        converter = new CreateInstanceApiModelConverter();
    }

    @Test
    void testConvertWithValidAction() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES, result.getAction());
        assertEquals(1, result.getInstanceCount());
    }

    @Test
    void testConvertWithInvalidAction() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("INVALID_ACTION");

        // When & Then
        assertThrows(IcmsBadRequestException.class, () -> converter.toSpotInstanceRequestSchema(request));
    }

    @Test
    void testConvertWithLaunchSpecification() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        CreateSpotInstanceLaunchSpecificationApiModel launchSpec = new CreateSpotInstanceLaunchSpecificationApiModel();
        launchSpec.setInstanceType("g4dn.xlarge");
        launchSpec.setContainerImage("test-image:latest");
        launchSpec.setBackend("AWS");
        launchSpec.setGpu("T4");
        launchSpec.setNcaId("test-nca-id");
        launchSpec.setHelmChart("test-chart");
        launchSpec.setConfiguration("test-config");
        launchSpec.setModels("[{\"name\":\"model-1\"}]");
        launchSpec.setMaxQueuedDuration(Duration.ofHours(1));
        launchSpec.setMaxRuntimeDuration(Duration.ofHours(2));
        launchSpec.setTerminationGracePeriodDuration(Duration.ofMinutes(5));
        launchSpec.setResultHandlingStrategy(ResultHandlingStrategy.UPLOAD);
        launchSpec.setDeploymentId(UUID.randomUUID());
        launchSpec.setGpuSpecificationId(UUID.randomUUID());
        launchSpec.setCacheArtifacts(true);
        launchSpec.setCacheSize(100L);
        launchSpec.setCacheHandle("test-cache-handle");
        launchSpec.setTelemetries("test-telemetry");

        CreateSpotInstanceLaunchSpecificationPlacementApiModel placement = new CreateSpotInstanceLaunchSpecificationPlacementApiModel();
        placement.setAvailabilityZone("us-west-2a");
        launchSpec.setPlacement(placement);

        request.setLaunchSpecification(launchSpec);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals("g4dn.xlarge", result.getInstanceType());
        assertEquals("test-image:latest", result.getContainerImage());
        assertEquals("AWS", result.getBackend());
        assertEquals("T4", result.getGpu());
        assertEquals("test-nca-id", result.getNcaId());
        assertEquals("test-chart", result.getHelmChart());
        assertEquals("test-config", result.getConfiguration());
        assertEquals("[{\"name\":\"model-1\"}]", result.getModels());
        assertEquals(Duration.ofHours(1), result.getMaxQueuedDuration());
        assertEquals(Duration.ofHours(2), result.getMaxRuntimeDuration());
        assertEquals(Duration.ofMinutes(5), result.getTerminationGracePeriodDuration());
        assertEquals(ResultHandlingStrategy.UPLOAD, result.getResultHandlingStrategy());
        assertEquals(launchSpec.getDeploymentId(), result.getDeploymentId());
        assertEquals(launchSpec.getGpuSpecificationId(), result.getGpuSpecificationId());
        assertTrue(result.isCacheArtifacts());
        assertEquals(100L, result.getCacheSize());
        assertEquals("test-cache-handle", result.getCacheHandle());
        assertEquals("test-telemetry", result.getTelemetries());
        assertEquals("us-west-2a", result.getAvailabilityZone());
    }

    @ParameterizedTest
    @EnumSource(FunctionType.class)
    void testConvertWithFunctionDetails(FunctionType functionType) {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        CreateSpotInstanceFunctionDetailsApiModel functionDetails = new CreateSpotInstanceFunctionDetailsApiModel();
        functionDetails.setFunctionId(UUID.randomUUID());
        functionDetails.setFunctionVersionId(UUID.randomUUID());
        functionDetails.setOwnerNcaId("test-owner-nca");
        functionDetails.setFunctionType(functionType);

        request.setFunctionDetails(functionDetails);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(functionDetails.getFunctionId(), result.getFunctionId());
        assertEquals(functionDetails.getFunctionVersionId(), result.getFunctionVersionId());
        assertEquals("test-owner-nca", result.getOwnerNcaId());
        assertEquals(functionType, result.getFunctionType());
    }

    @Test
    void testConvertWithTaskDetails() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        CreateSpotInstanceTaskDetailsApiModel taskDetails = new CreateSpotInstanceTaskDetailsApiModel();
        taskDetails.setTaskId(UUID.randomUUID());
        taskDetails.setAccountName("test-account");
        taskDetails.setOwnerNcaId("test-task-owner");

        request.setTaskDetails(taskDetails);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(taskDetails.getTaskId(), result.getTaskId());
        assertEquals("test-account", result.getAccountName());
        assertEquals("test-task-owner", result.getOwnerNcaIdForTask());
    }

    @Test
    void testConvertWithDefaultFunctionType() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(FunctionType.DEFAULT, result.getFunctionType());
    }

    @Test
    void testConvertWithEmptyDeploymentId() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setLaunchSpecification(new CreateSpotInstanceLaunchSpecificationApiModel());
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);
        request.getLaunchSpecification().setDeploymentId(null);
        request.getLaunchSpecification().setGpuSpecificationId(null);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(null, result.getDeploymentId());
        assertEquals(null, result.getGpuSpecificationId());
    }

    @Test
    void testConvertWithOnlyDeploymentId() {
        // Given
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setLaunchSpecification(new CreateSpotInstanceLaunchSpecificationApiModel());
        request.setAction("RequestSpotInstances");
        request.setInstanceCount(1);

        UUID deploymentId = UUID.randomUUID();
        request.getLaunchSpecification().setDeploymentId(deploymentId);
        request.getLaunchSpecification().setGpuSpecificationId(null);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then
        assertNotNull(result);
        assertEquals(deploymentId, result.getDeploymentId());
        assertEquals(deploymentId, result.getGpuSpecificationId());
    }

    // ──────────────────────────────────────────────
    // Action normalization: the two new prefix-less *creation* names must be
    // rewritten to their legacy "Spot"-prefixed equivalents before the schema
    // leaves the controller, so downstream consumers (NVCA, BYOC) - which only
    // recognize the legacy creation names - receive a compatible wire value.
    // ──────────────────────────────────────────────

    @ParameterizedTest(name = "[{index}] inbound \"{0}\" -> {1}")
    @CsvSource({
            "RequestInstances,         REQUEST_SPOT_INSTANCES",
            "RequestInstancesForTask,  REQUEST_SPOT_INSTANCES_FOR_TASK"
    })
    void newCreationActionStringsAreNormalizedToLegacyEnumOnSchema(
            String inboundAction, SpotInstanceRequestAction expectedLegacy) {

        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction(inboundAction);
        request.setInstanceCount(1);

        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        assertNotNull(result);
        assertEquals(expectedLegacy, result.getAction(),
                "Inbound new creation action '" + inboundAction
                        + "' must be normalized to legacy " + expectedLegacy
                        + " on the schema so downstream consumers see the legacy name.");
    }

    @ParameterizedTest(name = "[{index}] inbound \"{0}\" stays {1}")
    @CsvSource({
            // Legacy strings always pass through unchanged.
            "RequestSpotInstances,         REQUEST_SPOT_INSTANCES",
            "RequestSpotInstancesForTask,  REQUEST_SPOT_INSTANCES_FOR_TASK",
            "CancelSpotInstanceRequests,   CANCEL_SPOT_INSTANCE_REQUESTS",
            "DescribeSpotInstanceRequests, DESCRIBE_SPOT_INSTANCE_REQUESTS",
            "TerminateSpotInstanceRequest, TERMINATE_SPOT_INSTANCE_REQUEST",
            "DescribeInstances,            DESCRIBE_INSTANCES",
            "TerminateInstances,           TERMINATE_INSTANCES",
            // The 3 non-creation new actions are intentionally NOT normalized
            // so InstanceController#validateInstancesRequest can echo the
            // user-submitted action name in its 400 error response.
            "CancelInstanceRequests,       CANCEL_INSTANCE_REQUESTS",
            "DescribeInstanceRequests,     DESCRIBE_INSTANCE_REQUESTS",
            "TerminateInstanceRequest,     TERMINATE_INSTANCE_REQUEST"
    })
    void actionStringsOutsideCreationMappingAreUnchangedOnSchema(
            String inboundAction, SpotInstanceRequestAction expected) {

        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction(inboundAction);
        request.setInstanceCount(1);

        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        assertNotNull(result);
        assertEquals(expected, result.getAction(),
                "Inbound action '" + inboundAction
                        + "' must not be rewritten by the creation-action normalizer.");
    }

    @Test
    void requestInstancesWithFullPayload_isNormalizedAndPreservesOtherFields() {
        // Given - a complete inbound request using the new "RequestInstances"
        // action name with all major launch / function fields populated.
        CreateSpotInstanceRequestApiModel request = new CreateSpotInstanceRequestApiModel();
        request.setAction("RequestInstances");
        request.setInstanceCount(2);

        CreateSpotInstanceLaunchSpecificationApiModel launchSpec =
                new CreateSpotInstanceLaunchSpecificationApiModel();
        launchSpec.setInstanceType("dummy_gpu_4.large");
        launchSpec.setContainerImage("nvcr.io/test/image:latest");
        launchSpec.setBackend("AWS");
        launchSpec.setGpu("H100");
        launchSpec.setNcaId("nca-id-e2e");
        launchSpec.setMaxQueuedDuration(Duration.ofMinutes(5));
        launchSpec.setMaxRuntimeDuration(Duration.ofHours(1));
        launchSpec.setTerminationGracePeriodDuration(Duration.ofMinutes(1));
        launchSpec.setResultHandlingStrategy(ResultHandlingStrategy.UPLOAD);
        request.setLaunchSpecification(launchSpec);

        CreateSpotInstanceFunctionDetailsApiModel functionDetails =
                new CreateSpotInstanceFunctionDetailsApiModel();
        UUID functionId = UUID.randomUUID();
        UUID functionVersionId = UUID.randomUUID();
        functionDetails.setFunctionId(functionId);
        functionDetails.setFunctionVersionId(functionVersionId);
        functionDetails.setOwnerNcaId("nca-id-e2e");
        functionDetails.setFunctionType(FunctionType.LLM);
        request.setFunctionDetails(functionDetails);

        // When
        SpotInstanceRequestSchema result = converter.toSpotInstanceRequestSchema(request);

        // Then - action is normalized to legacy, everything else is forwarded verbatim.
        assertNotNull(result);
        assertEquals(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES, result.getAction(),
                "New 'RequestInstances' must be normalized to REQUEST_SPOT_INSTANCES.");
        assertEquals(2, result.getInstanceCount());
        assertEquals("dummy_gpu_4.large", result.getInstanceType());
        assertEquals("nvcr.io/test/image:latest", result.getContainerImage());
        assertEquals("AWS", result.getBackend());
        assertEquals("H100", result.getGpu());
        assertEquals("nca-id-e2e", result.getNcaId());
        assertEquals(Duration.ofMinutes(5), result.getMaxQueuedDuration());
        assertEquals(Duration.ofHours(1), result.getMaxRuntimeDuration());
        assertEquals(Duration.ofMinutes(1), result.getTerminationGracePeriodDuration());
        assertEquals(ResultHandlingStrategy.UPLOAD, result.getResultHandlingStrategy());
        assertEquals(functionId, result.getFunctionId());
        assertEquals(functionVersionId, result.getFunctionVersionId());
        assertEquals("nca-id-e2e", result.getOwnerNcaId());
        assertEquals(FunctionType.LLM, result.getFunctionType());
    }

}
