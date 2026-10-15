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
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import jakarta.validation.constraints.NotNull;
import java.util.Arrays;
import java.util.Collections;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Component;

@Component
@RequiredArgsConstructor
@Slf4j
public class CreateInstanceApiModelConverter {

    public SpotInstanceRequestSchema toSpotInstanceRequestSchema(
            @NotNull CreateSpotInstanceRequestApiModel createRequest) {
        Optional<SpotInstanceRequestAction> action = SpotInstanceRequestAction.toSpotInstanceRequestAction(
                createRequest.getAction());

        if (action.isEmpty()) {
            String errMsg = String.format("Invalid %s action provided", createRequest.getAction());
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        // Normalize new prefix-less action names to their legacy "Spot"-prefixed
        // equivalents so downstream consumers - which only recognize
        // the legacy names - see a consistent action value on every outbound
        // SQS / Cassandra payload generated from this schema.
        var builder = SpotInstanceRequestSchema.builder()
                .action(action.get().toLegacyAction())
                .instanceCount(createRequest.getInstanceCount());

        if (createRequest.getLaunchSpecification() != null) {
            builder.instanceType(createRequest.getLaunchSpecification().getInstanceType())
                    .containerImage(createRequest.getLaunchSpecification().getContainerImage())
                    .environment(createRequest.getLaunchSpecification().getEnvironment())
                    .backend(createRequest.getLaunchSpecification().getBackend())
                    .gpu(createRequest.getLaunchSpecification().getGpu())
                    .ncaId(createRequest.getLaunchSpecification().getNcaId())
                    .helmChart(createRequest.getLaunchSpecification().getHelmChart())
                    .configuration(createRequest.getLaunchSpecification().getConfiguration())
                    .models(createRequest.getLaunchSpecification().getModels())
                    .maxQueuedDuration(createRequest.getLaunchSpecification().getMaxQueuedDuration())
                    .maxRuntimeDuration(createRequest.getLaunchSpecification()
                                                .getMaxRuntimeDuration())
                    .terminationGracePeriodDuration(createRequest.getLaunchSpecification()
                                                            .getTerminationGracePeriodDuration())
                    .resultHandlingStrategy(createRequest.getLaunchSpecification()
                                                    .getResultHandlingStrategy());

            if (createRequest.getLaunchSpecification().getDeploymentId() != null) {
                builder.deploymentId(createRequest.getLaunchSpecification().getDeploymentId());
                if (createRequest.getLaunchSpecification().getGpuSpecificationId() != null) {
                    builder.gpuSpecificationId(createRequest.getLaunchSpecification().getGpuSpecificationId());
                }
                else {
                    // if gpu spec is not provided, use deployment id instead
                    builder.gpuSpecificationId(createRequest.getLaunchSpecification().getDeploymentId());
                }
            }

            if (createRequest.getLaunchSpecification().getPlacement() != null) {
                builder.availabilityZone(createRequest.getLaunchSpecification().getPlacement()
                                                 .getAvailabilityZone());
            }

            // Cache artifacts must be True for model cache request
            if (getCacheArtifactValue(createRequest)) {
                builder.cacheArtifacts(getCacheArtifactValue(createRequest))
                        .cacheSize(createRequest.getLaunchSpecification().getCacheSize())
                        .cacheHandle(createRequest.getLaunchSpecification().getCacheHandle());
            }

            if (!StringUtils.isEmpty(createRequest.getLaunchSpecification().getTelemetries())) {
                builder.telemetries(createRequest.getLaunchSpecification().getTelemetries());
            }

            builder.clusters(createRequest.getLaunchSpecification().getClusters())
                    .regions(createRequest.getLaunchSpecification().getRegions())
                    .attributes(createRequest.getLaunchSpecification().getAttributes());

        }

        builder.functionType(FunctionType.DEFAULT);

        if (createRequest.getFunctionDetails() != null) {
            builder.functionId(createRequest.getFunctionDetails().getFunctionId())
                    .functionName(createRequest.getFunctionDetails().getFunctionName())
                    .functionVersionId(createRequest.getFunctionDetails().getFunctionVersionId())
                    .ownerNcaId(createRequest.getFunctionDetails().getOwnerNcaId());

            if (createRequest.getFunctionDetails().getFunctionType() != null) {
                builder.functionType(createRequest.getFunctionDetails().getFunctionType());
            }
        }

        if (createRequest.getTaskDetails() != null) {
            builder.taskId(createRequest.getTaskDetails().getTaskId())
                    .taskName(createRequest.getTaskDetails().getTaskName())
                    .accountName(createRequest.getTaskDetails().getAccountName())
                    .ownerNcaIdForTask(createRequest.getTaskDetails().getOwnerNcaId());
        }

        return builder.build();
    }

    private boolean getCacheArtifactValue(CreateSpotInstanceRequestApiModel createRequest) {
        if (createRequest.getLaunchSpecification().getCacheArtifacts() == null) {
            return false;
        }
        return createRequest.getLaunchSpecification().getCacheArtifacts();
    }
}
