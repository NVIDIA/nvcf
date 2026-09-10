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
package com.nvidia.icms.service.createInstances;

import static com.nvidia.icms.service.byoc.ClusterTargetingHelper.isClusterHealthyAndCapacityAvailable;
import static com.nvidia.icms.service.createInstances.RequestInstanceDestination.isReservedOrReservedBackupDestination;
import static com.nvidia.icms.service.createInstances.RequestInstanceDestination.isReservedOrReservedBackupDestinations;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.InstanceServiceUtil.isTargetingEnabled;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.service.byoc.ByocCreateService;
import com.nvidia.icms.service.byoc.ByocValidationService;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;
import com.nvidia.icms.service.internal.InstanceValidationService;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import jakarta.validation.constraints.NotNull;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class CreateInstanceService {

    private final ByocCreateService byocCreateService;
    private final InstanceLifecycleService instanceLifecycleService;
    private final ByocValidationService byocValidationService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final RequestDestinationProvider requestDestinationProvider;
    private final ClusterTargetingHelper clusterTargetingHelper;
    private final TelemetryEventClient telemetryEventClient;
    private final ReservationProcessor reservationProcessor;
    private final ComputePlatformService computePlatformService;
    private final InstanceValidationService instanceValidationService;

    public CreateSpotInstancesResponse processInstanceRequest(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, Object> auditProps) {
        logIncomingInstanceCreationRequest(instanceRequest);

        // Validations for taskId
        instanceValidationService.validateTaskWorkload(instanceRequest);

        byocValidationService.validateNotEmpty(instanceRequest.getNcaId(), instanceRequest.getGpu());

        Map<String, CloudHealthEntity> cloudHealthByClusterId = clusterTargetingHelper.getAllClusterHealthInMap();

        if (isTargetingEnabled(instanceRequest)) {
            return processTargetedInstanceRequest(customer, instanceRequest, auditProps, cloudHealthByClusterId);
        } else {
            log.info("InstanceRequest: {}: Backend '{}' provided; NON_TARGETED flow selected",
                    instanceRequest.getLoggingId(), instanceRequest.getBackend());
            return processNonTargetedInstanceRequest(customer, instanceRequest, auditProps, cloudHealthByClusterId);
        }
    }


    protected @NotNull CreateSpotInstancesResponse processTargetedInstanceRequest(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, Object> auditProps,
            @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {

        // GET all READY BYOC and non-BYOC clusters for provided request but without checking a capacity
        Set<RequestInstanceDestination> filteredDestinations = filterAndValidateDestinations(requestDestinationProvider.getAllTargetedDestinations(instanceRequest, cloudHealthByClusterId),
                instanceRequest, cloudHealthByClusterId);
        // Generate list of non-BYOC clusters which will contain 1 non-BYOC cluster per region, NVCA and reserved destinations,
        // For non-BYOC targeting we want to send SQS message at region level as zone level is not present
        filteredDestinations = keepNonByocAsSingleDestinationPerRegion(filteredDestinations);

        return byocCreateService.processCreateRequest(customer,
                                                      filteredDestinations,
                                                      instanceRequest,
                                                      auditProps);
    }


    // Returns non-empty destination set
    private Set<RequestInstanceDestination> filterAndValidateDestinations(@NotNull Set<RequestInstanceDestination> destinations,
                                               @NotNull SpotInstanceRequestSchema instanceRequest,
                                               @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {
                                                
        if (!icmsConfigurationProperties.isNcaAllowedForGpu(
                instanceRequest.getGpu(), instanceRequest.getNcaId())) {
            destinations = new HashSet<>(destinations);
            destinations.removeIf(destination ->
                    computePlatformService.isComputePlatformProvider(destination.getCloudProvider()));
        }

        if (destinations.isEmpty()) {
            String errMsg = String.format("There are no available clusters for %s GPU or %s instance type",
                                          instanceRequest.getGpu(), instanceRequest.getInstanceType());
            reportAndThrowError(String.format("%s: %s", instanceRequest.getLoggingId(), errMsg), errMsg);
        }

        // Check if ncaId has a non-BYOC reservation and use it instead
        Set<RequestInstanceDestination> filteredDestinations = new HashSet<>(reservationProcessor.filterDestinationBasedOnReservation(destinations, instanceRequest, cloudHealthByClusterId));
        if (isSetEmptyOrNull(filteredDestinations)) {
            String errMsg = String.format("There are no available clusters with reservation for %s GPU or %s instance type",
                                          instanceRequest.getGpu(), instanceRequest.getInstanceType());
            reportAndThrowError(String.format("%s: %s", instanceRequest.getLoggingId(), errMsg), errMsg);
        }

        filterDestinationBasedOnCapacity(filteredDestinations, cloudHealthByClusterId, instanceRequest);
        if (isSetEmptyOrNull(filteredDestinations)) {
            String errMsg = String.format("There are no available clusters with capacity for %s GPU or %s instance type",
                    instanceRequest.getGpu(), instanceRequest.getInstanceType());
            reportAndThrowError(String.format("%s: %s", instanceRequest.getLoggingId(), errMsg), errMsg);
        }

        log.info("InstanceRequest: {}: {} total destinations filtered after applying capacity validation",
                 instanceRequest.getLoggingId(), filteredDestinations.size());

        return filteredDestinations;
    }


    private void reportAndThrowError(@NotNull String errorMessage, @NotNull String exceptionError) {
        log.error(errorMessage);
        throw new IcmsBadRequestException(exceptionError);
    }


    protected void filterDestinationBasedOnCapacity(
            @NotNull Set<RequestInstanceDestination> destinations,
            @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId,
            @NotNull SpotInstanceRequestSchema instanceRequest ) {

        //If reservation is used, "destination" will have only GNF zones with reservation and reserved capacity available. (RESERVED or RESERVED_BACKUP or RESERVED + RESERVED_BACKUP)
        // if not, destination will have mix of healthy NVCA and non-BYOC zones with not checked capacity (SPOT)
        if (isReservedOrReservedBackupDestinations(destinations)) {
            return;
        }

        Set<String> gpuName = Set.of(instanceRequest.getGpu());

        boolean isFunctionRequest = instanceRequest.getTaskId() == null;
        //TODO Yury do we need to check capacity on destination? Is it needed for non-BYOC?
        destinations.removeIf(r ->
            !isClusterHealthyAndCapacityAvailable(cloudHealthByClusterId.get(r.getClusterId()), gpuName, isFunctionRequest)
        );
    }

    protected @NotNull CreateSpotInstancesResponse processNonTargetedInstanceRequest(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, Object> auditProps,
            @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {

        // Request is for BYOC, performing nca-id based validation.
        Set<RequestInstanceDestination> filteredDestinations = filterAndValidateDestinations(requestDestinationProvider.getAllNonTargetedDestinations(instanceRequest),
                instanceRequest, cloudHealthByClusterId);

        RequestInstanceDestination firstDestination = filteredDestinations.stream().iterator().next();

        if (computePlatformService.isComputePlatformBackend(instanceRequest.getBackend())) {
            // legacy flow for non-targeted non-BYOC or bart
            return processNonTargetedNonByocRequest(customer, instanceRequest, auditProps, filteredDestinations);
        } else {
            // non-targeted NVCA flow
            return byocCreateService.processCreateRequest(customer, Set.of(firstDestination),
                                                          instanceRequest, auditProps);
        }
    }

    protected @NotNull CreateSpotInstancesResponse processNonTargetedNonByocRequest(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, Object> auditProps,
            @NotNull Set<RequestInstanceDestination> destinations) {
        if (!icmsConfigurationProperties.isGpuSupported(instanceRequest.getGpu())) {

            log.error("{}: Request is marked for legacy non-BYOC flow but {} gpu is not whitelisted",
                      instanceRequest.getLoggingId(), instanceRequest.getGpu());

            throw new IcmsBadRequestException(
                    String.format("Invalid %s gpu provided", instanceRequest.getInstanceType()));
        }

        if (!icmsConfigurationProperties.isInstanceTypeSupported(instanceRequest.getInstanceType())) {
            log.error(
                    "{}: Request is marked for legacy non-BYOC flow but {} instanceType is not whitelisted",
                    instanceRequest.getLoggingId(), instanceRequest.getInstanceType());

            throw new IcmsBadRequestException(
                    String.format("Invalid %s instance type provided", instanceRequest.getInstanceType()));
        }

        return instanceLifecycleService.requestNonByocInstances(customer, instanceRequest, auditProps, destinations);
    }


    protected @NotNull Set<RequestInstanceDestination> keepNonByocAsSingleDestinationPerRegion(
            @NotNull Set<RequestInstanceDestination> destinations) {

        Set<RequestInstanceDestination> result = new HashSet<>();
        Set<String> regionsAlreadyCovered = new HashSet<>();

        for (RequestInstanceDestination destination : destinations) {
            // Selecting destination as it is if it is for NVCA or reserved (RESERVED or RESERVED_BACKUP or RESERVED + RESERVED_BACKUP)
            if (!computePlatformService.isPlatformCluster(destination.getClusterGroupName())
                    || isReservedOrReservedBackupDestination(destination)) {
                result.add(destination);
            } else {
                // If a non-BYOC zone for the region is not already added then add it
                // TODO: When regions are not provided we we set Global queue for function/task for such request we should select destination per unique queue not region
                // In that case we will send multiple messages in global SQS queue because of destination selection is based on region not on unique queue
                if (StringUtils.isNotBlank(destination.getRegion()) &&
                        !regionsAlreadyCovered.contains(destination.getRegion().toLowerCase())) {
                    result.add(destination);
                    regionsAlreadyCovered.add(destination.getRegion().toLowerCase());
                }
            }
        }
        return result;
    }

    private void logIncomingInstanceCreationRequest(@NotNull SpotInstanceRequestSchema instanceRequest) {
        try {
            String instanceRequestCreationBody = instanceRequest.toString();
            log.info("class: CreateInstanceService, received instance creation request, request: {} ", instanceRequestCreationBody);


            GenericMetric genericMetric = new GenericMetric()
                    .withInstanceCreationRequestBody(instanceRequestCreationBody)
                    .withEventName(Events.INSTANCE_CREATION_REQUEST_RECEIVED.toString());

            // Adding function/task details to trace easily in telemetry
            UUID taskId = instanceRequest.getTaskId();
            if (taskId != null) {
                genericMetric.withTaskId(taskId.toString());
            }

            UUID functionId = instanceRequest.getFunctionId();
            if (functionId != null) {
                genericMetric.withFunctionId(functionId);
            }

            UUID functionVersionId = instanceRequest.getFunctionVersionId();
            if (functionVersionId != null) {
                genericMetric.withFunctionVersionId(functionVersionId);
            }

            UUID deploymentId = instanceRequest.getDeploymentId();
            if (deploymentId != null) {
                genericMetric.withDeploymentId(deploymentId);
            }

            UUID gpuSpecificationId = instanceRequest.getGpuSpecificationId();
            if (gpuSpecificationId != null) {
                genericMetric.withGpuSpecificationId(gpuSpecificationId);
            }

            String ncaId = instanceRequest.getNcaId();
            if (StringUtils.isNotBlank(ncaId)) {
                genericMetric.withNcaId(ncaId);
            }

            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TelemetryEventClient.EventMetaData.IS_TARGETING.getName(),
                    isTargetingEnabled(instanceRequest));
            genericMetric.withMetadata(metadata);

            telemetryEventClient.triggerEvent(List.of(genericMetric));

        } catch (Exception exception) {
            // Suppressing the error
            log.error("class: CreateInstanceService, logIncomingInstanceCreationRequest:" +
                    " failed to log incoming instance creation request body, error: {}, exception: ", exception.getMessage(), exception);
        }
    }


}
