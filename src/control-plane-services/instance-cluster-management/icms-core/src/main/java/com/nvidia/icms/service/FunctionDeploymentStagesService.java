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
package com.nvidia.icms.service;

import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.fnds.FunctionDeploymentStagesClient;
import com.nvidia.icms.outbound.fnds.model.FndsMessageDetailModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageV2Model;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionDeploymentStagesService {

    private final InstanceServiceHelper instanceServiceHelper;
    private final FunctionDeploymentStagesClient functionDeploymentStagesClient;
    private final InstanceRequestV2Repository instanceRequestV2Repository;

    //TODO yury add caching for request data - needed data will not changed over time
    public void sendFunctionDeploymentStage(
            @NotNull InstanceV2Entity instanceEntity, @NotNull String event) {
        Optional<InstanceRequestV2Entity> instanceRequest = instanceRequestV2Repository.findRequestById(
                instanceEntity.getRequestId());

        if (instanceRequest.isPresent()) {
            sendFunctionDeploymentStage(instanceRequest.get(), instanceEntity, event);
        } else {
            String message = String.format(
                    "Deployment stage cannot be sent, requestId %s not found",
                    instanceEntity.getRequestId());

            FndsMessageV2Model fndsMessageModel = toFndsMessageModel(instanceEntity, null, null,
                                                                   event, null, null);
            functionDeploymentStagesClient.sendStageTelemetryEvent(fndsMessageModel, null, message, true);

            log.warn("InstanceId {}: {}",
                     instanceEntity.getInstanceId(),
                     message);
        }
    }


    public void sendFunctionDeploymentStage(
            @NotNull InstanceRequestV2Entity instanceRequest,
            @NotNull InstanceV2Entity instanceEntity, @NotNull String event) {

        try {
            ClientRequestDataModel.LaunchSpecification launchSpecification = instanceServiceHelper.getLaunchSpecificationFromRequest(
                    instanceRequest.getRequest());

            if (launchSpecification != null) {
                FndsMessageV2Model fndsMessageModel = toFndsMessageModel(instanceEntity,
                                                                       launchSpecification.getFunctionId(),
                                                                       launchSpecification.getVersionId(),
                                                                       event,
                                                                       instanceRequest.getDeploymentId(),
                                                                       instanceRequest.getGpuSpecificationId());
                functionDeploymentStagesClient.sendFunctionDeploymentStage(fndsMessageModel);
            }
        } catch (Exception e) {
            String message = String.format("Deployment stage cannot be sent, Error: %s",
                                           e.getMessage());

            FndsMessageV2Model fndsMessageModel = toFndsMessageModel(instanceEntity, null, null, event, null, null);
            functionDeploymentStagesClient.sendStageTelemetryEvent(fndsMessageModel, null, message, true);

            log.error("InstanceId {}: {}",
                     instanceEntity.getInstanceId(),
                     message,
                     e);
        }
    }


    public FndsMessageV2Model toFndsMessageModel(
            InstanceV2Entity instanceEntity, String functionId, String functionVersionId,
            String event, UUID deploymentId, UUID gpuSpecificationId) {

        return FndsMessageV2Model.builder()
                .ncaId(instanceEntity.getNcaId())
                .functionId(functionId)
                .functionVersionId(functionVersionId)
                .deploymentId(deploymentId)
                .gpuSpecificationId(gpuSpecificationId)
                .instanceId(instanceEntity.getInstanceId())
                .event(event)
                .eventType("sis")
                .timestamp(Instant.now().truncatedTo(ChronoUnit.MILLIS).toString())
                .details(FndsMessageDetailModel.builder()
                                 .instanceType(instanceEntity.getInstanceType())
                                 .logMessage(instanceEntity.getErrorLog())
                                 .build())
                /* Yury: temporary commented until adding FnDS V2
                .gpuSpecificationId(gpuSpecificationId)*/
                .build();
    }


}
