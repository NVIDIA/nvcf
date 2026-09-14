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

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_EVALUATION;
import static com.nvidia.icms.service.InstanceServiceHelper.getFunctionId;
import static com.nvidia.icms.service.InstanceServiceHelper.getFunctionVersionId;
import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel.ByocLaunchSpecification;
import com.nvidia.icms.outbound.sqs.model.FunctionDetails;
import com.nvidia.icms.outbound.sqs.model.TaskDetails;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.util.TimeUtils;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

@Component
@Slf4j
@AllArgsConstructor
public class ByocMessageGenerator {

    private final ByocConfigurationProperties byocConfigurationProperties;
    private final ByocServiceHelper byocServiceHelper;
    private final InstanceServiceHelper instanceServiceHelper;

    public ByocSqsMessageModel generateSqsMessageModelForByocTask(
            String customer, UUID requestId,
            RequestInstanceDestination requestInstanceDestination,
            SpotInstanceRequestSchema instanceRequest, int instanceCountForBatch,
            ByocLaunchSpecification launchSpecification,
            TaskDetails taskDetails) {
        // Set additional params in launchSpecification based on requestInstanceDestination
        launchSpecification.setInstanceType(requestInstanceDestination.getInstanceType().getValue());
        launchSpecification.setInstanceCount(instanceCountForBatch);
        launchSpecification.setRequestedGPUCount(requestInstanceDestination.getInstanceType().getGpuCount());
        launchSpecification.setInstanceTypeValue(
                requestInstanceDestination.getInstanceType().getValue());
        launchSpecification.setInstanceTypeName(requestInstanceDestination.getInstanceType().getName());
        launchSpecification.setCloudProvider(requestInstanceDestination.getCloudProvider().name());
        launchSpecification.setDeploymentId(instanceRequest.getDeploymentId());
        launchSpecification.setGpuSpecificationId(instanceRequest.getGpuSpecificationId());

        return ByocSqsMessageModel.builder()
                .requestId(requestId.toString())
                .sub(customer)
                .ncaId(instanceRequest.getNcaId())
                .action(instanceRequest.getAction().getRequestAction())
                .instanceType(requestInstanceDestination.getInstanceType().getValue())
                .instanceCount(instanceCountForBatch)
                .gpuType(instanceRequest.getGpu())
                .requestedGPUCount(
                        requestInstanceDestination.getInstanceType().getGpuCount())
                .launchSpecification(launchSpecification)
                .accountName(instanceRequest.getAccountName())
                .taskDetails(taskDetails)
                .traceState(instanceServiceHelper.getTraceStateMap())
                .traceParent(instanceServiceHelper.getTraceParent())
                .messageBatchId(generateRandomUUID())
                .build();
    }

    public ByocSqsMessageModel generateSqsMessageModelForByocFunction(String requestId,
                                                                         int instancesCountPerMessage,
                                                                         SpotInstanceRequestSchema instanceRequest,
                                                                         RequestInstanceDestination requestInstanceDestination,
                                                                         String customer) {

        return ByocSqsMessageModel.builder()
                .instanceCount(instancesCountPerMessage)
                .requestId(requestId)
                .sub(customer)
                .action(instanceRequest.getAction().getRequestAction())
                .instanceType(requestInstanceDestination.getInstanceType().getValue())
                .instanceTypeValue(requestInstanceDestination.getInstanceType().getValue())
                .instanceTypeName(requestInstanceDestination.getInstanceType().getName())
                .ncaId(instanceRequest.getNcaId())
                .functionId(getFunctionId(instanceRequest))
                .functionVersionId(getFunctionVersionId(instanceRequest))
                .functionDetails(FunctionDetails.builder()
                        .functionType(instanceRequest.getFunctionType())
                        .functionId(instanceRequest.getFunctionId())
                        .functionVersionId(instanceRequest.getFunctionVersionId())
                        .build())
                .clusterGroup(requestInstanceDestination.getClusterGroupName())
                .requestedGPUCount(
                        requestInstanceDestination.getInstanceType().getGpuCount())
                .launchSpecification(
                        generateByocLaunchSpecification(instanceRequest,
                                                                requestInstanceDestination, instancesCountPerMessage))
                .traceParent(instanceServiceHelper.getTraceParent())
                .traceState(instanceServiceHelper.getTraceStateMap())
                .messageBatchId(generateRandomUUID())
                .build();
    }

    private ByocLaunchSpecification generateByocLaunchSpecification(@NotNull SpotInstanceRequestSchema instanceRequest,
                                                                                    @NotNull RequestInstanceDestination requestInstanceDestination, int instancesCountPerMessage) {
        return ByocLaunchSpecification.builder()
                // helm details
                .helmChart(instanceRequest.getHelmChart())
                .configuration(instanceRequest.getConfiguration())
                .models(instanceRequest.getModels())

                // model caching details
                .cacheHandle(instanceRequest.getCacheHandle())
                .cacheSize(byocServiceHelper.getRoundedOfCacheSizeInBytes(instanceRequest.getCacheSize()))
                .cacheArtifacts(instanceRequest.isCacheArtifacts())

                // Function deployment details
                .gpuName(instanceRequest.getGpu())
                .cloudProvider(requestInstanceDestination.getCloudProvider().name())
                .spotEnvironment(byocConfigurationProperties.getEnv())
                .icmsEnvironment(byocConfigurationProperties.getEnv())
                // NOTE: We are passing environment received from NVCF as is to BYOC clusters
                .environment(getStringValue(instanceRequest.getEnvironment()))
                .instanceTypeValue(requestInstanceDestination.getInstanceType().getValue())
                .instanceTypeName(requestInstanceDestination.getInstanceType().getName())
                .instanceCount(instancesCountPerMessage)
                .requestedGPUCount(requestInstanceDestination.getInstanceType().getGpuCount())
                .gpuType(instanceRequest.getGpu())
                .containerImage(instanceRequest.getContainerImage())
                .telemetries(getStringValue(instanceRequest.getTelemetries()))

                // detailed targeting fields
                .clusters(instanceRequest.getClusters())
                .regions(instanceRequest.getRegions())
                .attributes(instanceRequest.getAttributes())
                .deploymentId(instanceRequest.getDeploymentId())
                .gpuSpecificationId(instanceRequest.getGpuSpecificationId())
                .build();
    }

    InstanceRequestV2Entity generateInstanceRequestEntity(String customer, SpotInstanceRequestSchema instanceRequest,
                                                          UUID requestId, String request, Set<String> attributes) {
        return InstanceRequestV2Entity.builder()
                .customer(customer)
                .action(instanceRequest.getAction())
                .createTimeuuid(TimeUtils.getTimeUuidNow())
                .requestId(requestId.toString())
                .state(SpotInstanceRequestState.OPEN)
                .statusCode(PENDING_EVALUATION.toString())
                .statusMessage("open")
                .statusUpdateTime(Instant.now())
                .request(request)
                .resourceProvider(ResourceProvider.BYOC)
                .checkBatchwiseInfo(false)
                .clusters(instanceRequest.getClusters())
                .regions(instanceRequest.getRegions())
                .attributes(attributes)
                .instanceCount(instanceRequest.getInstanceCount())
                .ncaId(instanceRequest.getNcaId())
                .functionId(instanceRequest.getFunctionId())
                .functionVersionId(instanceRequest.getFunctionVersionId())
                .taskId(instanceRequest.getTaskId())
                .deploymentId(instanceRequest.getDeploymentId())
                .gpuSpecificationId(instanceRequest.getGpuSpecificationId())
                .build();
    }
} 