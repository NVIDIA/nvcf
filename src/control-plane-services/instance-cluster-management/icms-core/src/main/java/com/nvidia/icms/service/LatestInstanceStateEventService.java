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

import static com.nvidia.icms.scheduled.GpuUsageTaskController.GPU_USAGE_EVENT_NAME;
import static com.nvidia.icms.service.InstanceServiceHelper.getCloudProvider;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel.LaunchSpecification;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import java.util.List;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor

/**
 We should send LatestInstanceState event when there is change in instance state, this event will help to monitor latest state of instance in telemetry
 We should also send LatestInstanceState event along with GpuUsagePerInstance which keep track of all active (starting/running) instances in regular interval (1 hour)
 **/
public class LatestInstanceStateEventService {

    private final TelemetryEventClient telemetryEventClient;

    private final ObjectMapper objectMapper;

    public void sendLatestInstanceStateEvent(InstanceV2Entity entity) {

        try {
            LaunchSpecification launchSpecification = getLaunchSpecificationForTelemetry(
                    entity.getRequestRawData());

            GenericMetric genericMetric = new GenericMetric()
                    .withEventName(Events.LATEST_INSTANCE_STATE.toString())

                    // Instance information
                    .withRequestId(entity.getRequestId())
                    .withInstanceId(entity.getInstanceId())
                    .withInstanceState(entity.getInstanceStateName().getStateName())
                    .withGpuName(entity.getGpu())
                    .withInstanceType(entity.getInstanceType())
                    .withCapacityType(entity.getCapacityType())
                    .withRegionName(entity.getRegion())
                    .withInstanceCreateTime(TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid()))

                    // Cluster information
                    .withClusterId(entity.getZone())
                    .withCloudProvider(getCloudProvider(entity))

                    // NCA ID information
                    .withNcaId(entity.getNcaId())

                    // Function information
                    .withFunctionId(launchSpecification.getFunctionId())
                    .withFunctionVersionId(launchSpecification.getVersionId())
                    .withFunctionName(launchSpecification.getFunctionName())

                    // Deployment information
                    .withDeploymentId(launchSpecification.getDeploymentId())
                    .withGpuSpecificationId(launchSpecification.getGpuSpecificationId())

                    // Task information
                    .withTaskId(launchSpecification.getTaskId())
                    .withTaskName(launchSpecification.getTaskName())

                    // Reservation information
                    .withReservationId(entity.getReservationId());

            telemetryEventClient.triggerEvent(List.of(genericMetric));
        } catch (Exception exception) {

            // Suppressing the exception avoid blocking main API flow
            log.error(
                    "LatestInstanceStateEventService: failed to send LatestInstanceState event, error: {}, exception: ",
                    exception.getMessage(), exception);
        }

    }


    public ClientRequestDataModel.LaunchSpecification getLaunchSpecificationForTelemetry(
            @Nullable String request) {
        try {
            return getLaunchSpecificationFromRequest(request);
        } catch (Exception exception) {
            log.error("LatestInstanceStateEventService: Failed to parse request info for telemetry, error: {}, exception: ",
                    exception.getMessage(), exception);
        }

        // Adding default values
        return ClientRequestDataModel.LaunchSpecification.builder().build();
    }


    public ClientRequestDataModel.LaunchSpecification getLaunchSpecificationFromRequest(
            @Nullable String request) {
        if (!StringUtils.isEmpty(request)) {
            ClientRequestDataModel clientRequestDataModel = parseRequestInfo(request);
            if (clientRequestDataModel.getLaunchSpecification() != null) {
                return clientRequestDataModel.getLaunchSpecification();
            }
        }

        // Adding default values
        return ClientRequestDataModel.LaunchSpecification.builder().build();
    }

    @Observed
    public ClientRequestDataModel parseRequestInfo(String request) {
        try {
            ClientRequestDataModel requestData;
            requestData =
                    objectMapper.readValue(request, ClientRequestDataModel.class);
            return requestData;
        } catch (Exception e) {
            String errMsg =
                    String.format("Failed to parse request information, error: %s", e.getMessage());
            log.error("LatestInstanceStateEventService: error: {}, exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

}
