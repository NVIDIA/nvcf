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

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.HealthInfo;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstance;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.service.extensions.api.InstanceDescriptionHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

import jakarta.annotation.Nullable;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.SHUTTING_DOWN;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.STARTING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.TERMINATED;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.CANNOT_FULFILL;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_EVALUATION;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.SCHEDULE_EXPIRED;
import static com.nvidia.icms.util.InstanceServiceUtil.isModelCacheEnabled;

/*
 * This class will have common service functionality.
 * This service will deal with database read and update operations.
 * eg: describe instance requests, cancel instance request
 */
@Service
@AllArgsConstructor
@Slf4j
public class DescribeAndCancelInstanceService {

    public static final String NON_BYOC_ACKED_INSTANCES_KEY = "nonByocAckedInstances";

    public static final String BYOC_ACKED_INSTANCES_KEY = "byocAckInstances";

    public static final String ACK_EXPIRED_ERROR_LOG =
            "Acknowledged capacity expired without instance creation";

    public static final String CLUSTER_UNHEALTHY_ERROR_LOG =
            "Cluster became unhealthy after acknowledging capacity";

    public static final String CANNOT_FULFILL_ERROR_LOG =
            "Cluster could not fulfill the instance request";

    public static final String SCHEDULE_EXPIRED_BATCH_ERROR_LOG =
            "Instance request schedule expired for this batch";

    public static final String DEPLOYMENT_NOT_FOUND_MSG =
            "No instance requests found for deploymentId %s";

    private final InstanceV2Repository instanceV2Repository;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final ObjectMapper objectMapper;

    private final AppAuditService auditService;

    private final InstanceDescriptionHelper instanceDescriptionHelper;

    private final ByocDescribeHelper byocDescribeHelper;

    private final SqsMessageRepository sqsMessageRepository;

    private final CloudHealthRepository cloudHealthRepository;

    private final InstanceServiceHelper instanceServiceHelper;

    private final ComputePlatformService computePlatformService;

    @Observed
    public GetSpotInstanceRequests describeInstances(
            String customer,
            List<String> instanceIds) {

        if (instanceIds == null || instanceIds.isEmpty()) {
            String err = "instanceIds must be provided";
            log.error(err);
            throw new IcmsBadRequestException(err);
        }

        List<InstanceV2Entity> instanceIdsInformation;
        Map<String, ClusterEntity> clustersCache = new HashMap<>();
        Set<String> uniqueInstanceIds = new HashSet<>(instanceIds);
        instanceIdsInformation =
                instanceV2Repository.findInstancesByCustomerAndIds(customer,
                        new HashSet<>(uniqueInstanceIds));

        if (instanceIdsInformation == null || instanceIdsInformation.isEmpty()) {
            return new GetSpotInstanceRequests(null, new ArrayList<>());
        }

        if (icmsConfigurationProperties.isCheckForDuplicateInstances()) {
            instanceIdsInformation = removeDuplicateInstances(instanceIdsInformation);
        }
        List<SpotInstance> instances = new ArrayList<>();
        Map<String, InstanceRequestV2Entity> mapOfRequestIdVsRequests = new HashMap<>();

        for (InstanceV2Entity instanceEntity : instanceIdsInformation) {

            // Fetching request info
            InstanceRequestV2Entity requestInfo = mapOfRequestIdVsRequests.get(instanceEntity.getRequestId());

            if (requestInfo == null) {
                Optional<InstanceRequestV2Entity> requestInfoOpt =
                        instanceRequestV2Repository.findRequestByIdAndCustomer(instanceEntity.getRequestId(),
                                                                                   customer);
                if (requestInfoOpt.isPresent()) {
                    requestInfo = requestInfoOpt.get();
                    mapOfRequestIdVsRequests.put(requestInfo.getRequestId(), requestInfo);
                }
            }

            ClientRequestDataModel requestData =
                    getClientRequestDataModelForRequest(requestInfo);

            // Fetching instance info
            String instanceType = requestData.getLaunchSpecification().getInstanceType();
            Optional<ZoneInfo> optionalZoneInfo =
                    populateZoneInfoForInstanceEntity(instanceEntity, clustersCache);
            if (optionalZoneInfo.isEmpty()) {
                continue;
            }
            ZoneInfo zoneInfo = optionalZoneInfo.get();

            // Generating response
            instances.add(generateInstance(instanceType, zoneInfo, requestData, instanceEntity));
        }

        return new GetSpotInstanceRequests(null, instances);
    }

    /**
     * NVCF deployment-GET path.
     *
     * <p>Two independent dimensions control response shape:</p>
     * <ul>
     *   <li><b>includeTerminated</b> - filters real lifecycle-terminated rows from
     *   the instance table (instances that actually launched and have since
     *   transitioned to TERMINATED). Used by long-running deployments to keep the
     *   payload small.</li>
     *   <li><b>expiredAckedInstances</b> - opt-in switch for synthetic terminated
     *   placeholders emitted by {@link #populateAcknowledgedInstances} when an
     *   ACKed batch failed to materialize (canceled/schedule-expired, ACK expired,
     *   or cluster unhealthy). Defaults to {@code false}; deployments in initial
     *   scale-up set it to {@code true} to surface failure reasons.</li>
     * </ul>
     *
     * <p>The two flags are orthogonal and both default to {@code false}:
     * {@code includeTerminated=false} does NOT suppress synthetic placeholders
     * (those are governed solely by {@code expiredAckedInstances}), and
     * {@code expiredAckedInstances=false} does NOT hide real DB-row terminations
     * (those are governed solely by {@code includeTerminated}).</p>
     */
    @Observed
    public GetSpotInstanceRequests describeInstancesByDeploymentId(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecId,
            boolean includeTerminated,
            boolean expiredAckedInstances) {
        List<InstanceRequestV2Entity> requests = findRequestsByDeploymentId(
                ncaId,
                deploymentId,
                gpuSpecId);
        Set<String> requestIdSet = requests.stream().map(
                InstanceRequestV2Entity::getRequestId).collect(
                Collectors.toSet());
        List<InstanceV2Entity> instanceEntities = findInstancesByDeploymentColumns(
                deploymentId,
                gpuSpecId);
        Map<String, List<InstanceV2Entity>> mapOfRequestIdVsInstances =
                groupInstancesByRequestId(instanceEntities, requestIdSet);

        return buildInstancesByDeploymentResponse(
                requests,
                mapOfRequestIdVsInstances,
                includeTerminated,
                expiredAckedInstances);
    }

    private List<InstanceRequestV2Entity> findRequestsByDeploymentId(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecId) {
        List<InstanceRequestV2Entity> requests = instanceRequestV2Repository
                .findRequestsByDeploymentId(ncaId, deploymentId, null);

        if (gpuSpecId != null) {
            requests = requests.stream()
                    .filter(r -> gpuSpecId.equals(r.getGpuSpecificationId()))
                    .toList();
        }

        if (requests.isEmpty()) {
            String errMsg = String.format(DEPLOYMENT_NOT_FOUND_MSG, deploymentId);
            log.info("ncaId {} deploymentId {} gpuSpecId {} - {}",
                     ncaId, deploymentId, gpuSpecId, errMsg);
            throw new IcmsNotFoundException(errMsg);
        }

        return requests;
    }

    private List<InstanceV2Entity> findInstancesByDeploymentColumns(
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecId) {
        if (gpuSpecId == null) {
            return instanceV2Repository.findInstancesByDeploymentId(deploymentId);
        }

        return instanceV2Repository.findInstancesByGpuSpecificationId(
                deploymentId,
                gpuSpecId);
    }

    private Map<String, List<InstanceV2Entity>> groupInstancesByRequestId(
            List<InstanceV2Entity> instanceEntities,
            Set<String> requestIds) {
        Map<String, List<InstanceV2Entity>> instancesByRequestId = new HashMap<>();
        requestIds.forEach(requestId -> instancesByRequestId.put(requestId, new ArrayList<>()));

        if (instanceEntities == null || instanceEntities.isEmpty()) {
            return instancesByRequestId;
        }

        for (InstanceV2Entity instanceEntity : instanceEntities) {
            if (instanceEntity == null
                    || StringUtils.isBlank(instanceEntity.getRequestId())
                    || !requestIds.contains(instanceEntity.getRequestId())) {
                continue;
            }
            instancesByRequestId.get(instanceEntity.getRequestId()).add(instanceEntity);
        }

        return instancesByRequestId;
    }

    private GetSpotInstanceRequests buildInstancesByDeploymentResponse(
            List<InstanceRequestV2Entity> requests,
            Map<String, List<InstanceV2Entity>> mapOfRequestIdVsInstances,
            boolean includeTerminated,
            boolean expiredAckedInstances) {
        Map<String, ClusterEntity> clustersCache = new HashMap<>();
        Set<String> healthyZones = getHealthyZones();
        List<SpotInstance> responseInstances = new ArrayList<>();

        for (InstanceRequestV2Entity request : requests) {
            if (request == null) {
                continue;
            }

            List<InstanceV2Entity> instanceEntities = mapOfRequestIdVsInstances != null
                    ? mapOfRequestIdVsInstances.get(request.getRequestId())
                    : null;

            if (icmsConfigurationProperties.isCheckForDuplicateInstances()) {
                instanceEntities = removeDuplicateInstances(instanceEntities);
            }

            populateAcknowledgedInstances(request, instanceEntities, healthyZones,
                                          responseInstances, expiredAckedInstances);

            if (instanceEntities != null && !instanceEntities.isEmpty()) {
                ClientRequestDataModel requestData = getClientRequestDataModelForRequest(request);

                for (InstanceV2Entity instanceEntity : instanceEntities) {
                    // Only filters real lifecycle-terminated rows. Synthetic terminated
                    // placeholders from populateAcknowledgedInstances are already
                    // in the instance list and are NOT affected by includeTerminated, since
                    // they represent failed ACKs rather than lifecycle terminations.
                    if (!includeTerminated
                            && TERMINATED.equals(instanceEntity.getInstanceStateName())) {
                        continue;
                    }
                    String instanceType = requestData.getLaunchSpecification().getInstanceType();
                    Optional<ZoneInfo> optionalZoneInfo =
                            populateZoneInfoForInstanceEntity(instanceEntity, clustersCache);
                    if (optionalZoneInfo.isEmpty()) {
                        continue;
                    }
                    ZoneInfo zoneInfo = optionalZoneInfo.get();
                    responseInstances.add(generateInstance(instanceType, zoneInfo, requestData,
                                                          instanceEntity));
                }
            }
        }

        return new GetSpotInstanceRequests(null, responseInstances);
    }


    @Observed
    public GetSpotInstanceRequests describeAdminInstanceRequests(
            Set<String> requestIds,
            Set<String> stateFilter) {

        if (requestIds == null || requestIds.isEmpty()) {
            String err = "SpotInstanceRequestId must be provided";
            log.error(err);
            throw new IcmsBadRequestException(err);
        }

        List<InstanceRequestV2Entity> requestIdsInformation =
                findByRequestIds(requestIds);
        if (requestIdsInformation == null || requestIdsInformation.isEmpty()) {
            return new GetSpotInstanceRequests(new ArrayList<>(), null);
        }

        Map<String, List<InstanceV2Entity>> mapOfRequestIdVsInstances =
                getMapOfRequestIdVsInstances(requestIdsInformation);

        return getResponseByFilteringRequestInformationBasedOnState(
                mapOfRequestIdVsInstances,
                requestIdsInformation,
                stateFilter);
    }

    @Observed
    public GetSpotInstanceRequests describeInstanceRequests(
            String customer, Set<String> requestIds,
            Set<String> stateFilter) {

        if (requestIds == null || requestIds.isEmpty()) {
            String err = "requestIds must be provided";
            log.error(err);
            throw new IcmsBadRequestException(err);
        }

        List<InstanceRequestV2Entity> requestIdsInformation =
                findByCustomerAndRequestIds(customer, requestIds);
        if (requestIdsInformation == null || requestIdsInformation.isEmpty()) {
            return new GetSpotInstanceRequests(new ArrayList<>(), null);
        }

        Set<String> requestIdSet = requestIdsInformation.stream().map(
                InstanceRequestV2Entity::getRequestId).collect(
                Collectors.toSet());
        Map<String, List<InstanceV2Entity>> mapOfRequestIdVsInstances =
                getMapOfRequestIdVsInstances(customer, requestIdSet);

        return getResponseByFilteringRequestInformationBasedOnState(
                mapOfRequestIdVsInstances,
                requestIdsInformation,
                stateFilter);
    }

    private GetSpotInstanceRequests getResponseByFilteringRequestInformationBasedOnState(
            Map<String, List<InstanceV2Entity>> mapOfRequestIdVsInstances,
            List<InstanceRequestV2Entity> requestIdsInformation, Set<String> stateFilter) {

        List<SpotInstanceRequest> instanceRequests = new ArrayList<>();
        Set<String> healthyZones = getHealthyZones();
        Map<String, ClusterEntity> clustersCache = new HashMap<>();

        try {
            for (InstanceRequestV2Entity instanceRequestEntity : requestIdsInformation) {
                List<InstanceV2Entity> instancesInfo =
                        mapOfRequestIdVsInstances.get(instanceRequestEntity.getRequestId());

                if (icmsConfigurationProperties.isCheckForDuplicateInstances()) {
                    instancesInfo = removeDuplicateInstances(instancesInfo);
                }

                populateAcknowledgedInstances(instanceRequestEntity, stateFilter, instancesInfo,
                                              healthyZones, instanceRequests);

                if (instancesInfo == null || instancesInfo.isEmpty()) {
                    addDefaultResponse(stateFilter, instanceRequestEntity, instanceRequests);
                    continue;
                }

                // If "instanceIfo" is present means requestState is associated with instances
                // Expected requestState: "active", "closed"
                for (InstanceV2Entity instanceInfo : instancesInfo) {
                    if (isRequestIdIncludedBasedOnInstanceState(stateFilter,
                            Set.of(instanceInfo.getRequestState().toString()))) {
                        Optional<SpotInstanceRequest> instanceRequest =
                                generateInstanceRequest(instanceRequestEntity, instanceInfo,
                                                            clustersCache);
                        instanceRequest.ifPresent(instanceRequests::add);
                    }
                }
            }
        } catch (IcmsInternalServerException internalServerException) {
            log.error(
                    "Failed to frame response for describe instance requests, internalServer error: {}; exception: ",
                    internalServerException.getBody().getDetail(), internalServerException);
            throw internalServerException;

        } catch (Exception e) {
            String errMsg = String.format("Error while framing response for describe instance requests, error: %s",
                    e.getMessage());
            log.error("error: {} exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }

        return new GetSpotInstanceRequests(instanceRequests, null);
    }

    private Optional<SpotInstanceRequest> generateInstanceRequest(
            InstanceRequestV2Entity instanceRequestEntity,
            InstanceV2Entity instanceInfo,
            Map<String, ClusterEntity> clustersCache) {

        SpotInstanceRequest instanceRequest =
                getCustomDataFromInstanceRequestEntity(instanceRequestEntity);
        Optional<ZoneInfo> optionalZoneInfo = populateZoneInfoForInstanceEntity(instanceInfo,
                                                                                    clustersCache);
        if (optionalZoneInfo.isEmpty()) {
            return Optional.empty();
        }
        ZoneInfo zoneInfo = optionalZoneInfo.get();

        instanceRequest.setInstanceId(instanceInfo.getInstanceId());
        instanceRequest.setSpotCloudProvider(zoneInfo.getCloudProvider());
        // Adding state and status from instance entity
        instanceRequest.setState(
                instanceInfo.getRequestState());
        instanceRequest.setStatus(
                new SpotInstanceRequestStatus(
                        String.valueOf(instanceInfo.getRequestStatusCode()),
                        instanceInfo.getRequestStatusMessage(),
                        instanceInfo.getRequestStatusUpdateTime()));
        instanceRequest.setLaunchedAvailabilityZone(zoneInfo.getZoneName());
        instanceRequest.getSpotInstanceLaunchSpecification().setPlacement(
                new SpotInstanceLaunchSpecification.Placement(zoneInfo.getZoneName()));
        // Set Capacity type
        instanceRequest.getSpotInstanceLaunchSpecification().setCapacityType(
                getCapacityType(instanceInfo));
        instanceRequest.setInstanceState(
                new SpotInstanceState(instanceInfo.getInstanceStateCode(),
                                      instanceInfo.getInstanceStateName().getStateName()));
        if (!StringUtils.isEmpty(instanceInfo.getErrorLog())) {
            HealthInfo healthInfo = HealthInfo.builder()
                    .errorLog(instanceInfo.getErrorLog())
                    .build();
            instanceRequest.setHealthInfo(healthInfo);
        }
        if (instanceInfo.getInstanceIps() != null && !instanceInfo.getInstanceIps()
                .isEmpty()) {
            instanceRequest.setInstanceIps(instanceInfo.getInstanceIps());
        }

        if (instanceRequestEntity.getDeploymentId() != null) {
            instanceRequest.setDeploymentId(instanceRequestEntity.getDeploymentId().toString());
        }

        if (instanceRequestEntity.getGpuSpecificationId() != null) {
            instanceRequest.setGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId().toString());
        }

        return Optional.of(instanceRequest);
    }

    private boolean isRequestIdIncludedBasedOnInstanceState(
            Set<String> providedStateFilter,
            Set<String> requestStates) {
        if (providedStateFilter == null || providedStateFilter.isEmpty()) {
            return true;
        }
        // Check if any of the request states are in the provided filter
        return requestStates.stream().anyMatch(providedStateFilter::contains);
    }

    private SpotInstanceRequest getCustomRequestData(
            InstanceRequestV2Entity instanceRequestEntity,
            SpotInstanceRequestStatus instanceRequestStatus) {
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();

        instanceRequest.setCreateTime(TimeUtils.getInstantFromUuid(instanceRequestEntity.getCreateTimeuuid()));

        instanceRequest.setState(
                SpotInstanceRequestState.valueOf(
                        instanceRequestEntity.getState().toString().toUpperCase()));

        instanceRequest.setSpotInstanceRequestId(instanceRequestEntity.getRequestId());

        instanceRequest.setStatus(instanceRequestStatus);

        instanceRequest.setLaunchedAvailabilityZone(null);
        ClientRequestDataModel.LaunchSpecification launchSpecification =
                instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest()).getLaunchSpecification();

        // If the request is canceled then set the health info with appropriate user visible msg
        if (Objects.equals(instanceRequestEntity.getStatusCode(),
                           SpotRequestStatusCode.SCHEDULE_EXPIRED.toString())) {
            HealthInfo healthInfo = new HealthInfo(instanceRequestEntity.getStatusMessage());
            instanceRequest.setHealthInfo(healthInfo);
        }

        instanceRequest.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(launchSpecification.getInstanceType(),
                                                    launchSpecification.getContainerImage(), null,
                                                    launchSpecification.getGpu(),
                                                    launchSpecification.getBackend(),
                                                    launchSpecification.getNcaId(), null));

        instanceRequest.setInstanceInterruptionBehavior("terminate");

        return instanceRequest;
    }


    private Optional<ZoneInfo> populateZoneInfoForInstanceEntity(
            InstanceV2Entity instanceEntity,
            Map<String, ClusterEntity> clustersCache) {

        ResourceProvider resourceProvider = instanceEntity.getResourceProvider();
        if (resourceProvider != null) {
            if (computePlatformService.isComputePlatformProvider(resourceProvider)) {
                return instanceDescriptionHelper.resolveZoneInfo(instanceEntity);
            } else if (resourceProvider == ResourceProvider.OCI) {
                return byocDescribeHelper.resolveOciZoneInfo(instanceEntity);
            } else if (resourceProvider == ResourceProvider.BYOC) {
                return byocDescribeHelper.resolveByocZoneInfo(instanceEntity, clustersCache);
            } else {
                String errMsg = String.format("%s resource provider mapping doesn't exist", resourceProvider);
                log.error("error: {}", errMsg);
                throw new IcmsInternalServerException(errMsg);
            }
        }

        return Optional.empty();
    }

    private List<InstanceV2Entity> removeDuplicateInstances(
            List<InstanceV2Entity> instanceEntities) {
        if (instanceEntities == null || instanceEntities.isEmpty()) {
            return instanceEntities;
        }
        Map<String, InstanceV2Entity> instanceIdToEntityMap = new HashMap<>();
        for (InstanceV2Entity instanceEntity : instanceEntities) {
            String instanceId = instanceEntity.getInstanceId();
            if (instanceIdToEntityMap.containsKey(instanceId)) {
                instanceIdToEntityMap.put(instanceId, getLatestInstanceEntry(instanceEntity,
                                                                                 instanceIdToEntityMap.get(
                                                                                         instanceId)));
            } else {
                instanceIdToEntityMap.put(instanceId, instanceEntity);
            }
        }
        return new ArrayList<>(instanceIdToEntityMap.values());
    }

    private InstanceV2Entity getLatestInstanceEntry(
            InstanceV2Entity entity1,
            InstanceV2Entity entity2) {
        if (entity1.getRequestStatusUpdateTime()
                .isAfter(entity2.getRequestStatusUpdateTime())) {
            return getTerminalOrLatestEntity(entity1, entity2);
        }

        return getTerminalOrLatestEntity(entity2, entity1);

    }

    private InstanceV2Entity getTerminalOrLatestEntity(
            InstanceV2Entity latest,
            InstanceV2Entity older) {
        if (latest.getInstanceStateName() == TERMINATED) {
            return latest;
        }
        if (older.getInstanceStateName() == TERMINATED) {
            return older;
        }
        if (latest.getInstanceStateName() == SHUTTING_DOWN) {
            return latest;
        }
        if (older.getInstanceStateName() == SHUTTING_DOWN) {
            return older;
        }
        return latest;
    }

    /*
     Avoid acked instance population in below case:
     1. if acked instance population is not enabled
     2. If all requested instances are created
     3. If instances are acked but cloud is unhealthy
   */
    private void populateAcknowledgedInstances(
            InstanceRequestV2Entity instanceRequestEntity,
            Set<String> stateFilter,
            List<InstanceV2Entity> instancesInfo,
            Set<String> healthyZones,
            List<SpotInstanceRequest> instanceRequests) {

        if (considerRequestForAckedInstancePopulation(instanceRequestEntity, stateFilter)) {
            ClientRequestDataModel clientData = instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest());
            int requestedInstancesCount = clientData.getInstanceCount();
            int createdInstancesCount = instancesInfo == null ? 0 : instancesInfo.size();
            int nonByocCreatedInstances = 0;
            int byocCreatedInstances = 0;

            // Get created instances count for non-BYOC vs BYOC
            if (createdInstancesCount != 0) {
                // In instance entity resourceProvider is same as cloudProvider
                nonByocCreatedInstances = instancesInfo.stream()
                        .filter(instance -> computePlatformService.isComputePlatformProvider(instance.getResourceProvider()))
                        .toList().size();
                byocCreatedInstances = instancesInfo.stream()
                        .filter(instance -> !computePlatformService.isComputePlatformProvider(instance.getResourceProvider()))
                        .toList().size();
            }

            if (createdInstancesCount != requestedInstancesCount) {

                // Get ack instances counts for non-BYOC vs BYOC
                Map<String, Integer> ackedInstancesMap =
                        getAcknowledgedInstances(healthyZones, instanceRequestEntity, clientData);
                int nonByocAckedInstances = ackedInstancesMap.getOrDefault(NON_BYOC_ACKED_INSTANCES_KEY, 0);
                int byocAckInstances = ackedInstancesMap.getOrDefault(BYOC_ACKED_INSTANCES_KEY, 0);
                int totalAckedInstances = nonByocAckedInstances + byocAckInstances;

                int expectedInstances = Math.max(nonByocAckedInstances - nonByocCreatedInstances, 0) +
                        Math.max(byocAckInstances - byocCreatedInstances, 0);

                // Adding "pending-evaluation" entry when created=0 and acked=0 (either cloud is offline or not fulfilled by backed)
                if (totalAckedInstances == 0 && createdInstancesCount == 0) {
                    instanceRequests.add(getCustomDataForPendingEvaluation(instanceRequestEntity));
                    return;
                }

                // Adding "pending-fulfillment" entry when acked instances exists
                if (expectedInstances > 0) {
                    addAcknowledgedInstances(instanceRequests, instanceRequestEntity,
                                             expectedInstances);
                }
            }
        }
    }

    /**
     * Orchestrator for synthetic placeholder population on the deployment GET path.
     *   1. master gate (config + batch-info)
     *   2. canceled + schedule-expired short-circuit (parent statusMessage path)
     *   3. still-active gate (state + status filter)
     *   4. classify batches and emit STARTING placeholders for healthy ACK gap
     *   5. emit TERMINATED placeholders for failed batches, capped at requested count
     */
    private void populateAcknowledgedInstances(
            InstanceRequestV2Entity instanceRequestEntity,
            List<InstanceV2Entity> instancesInfo,
            Set<String> healthyZones,
            List<SpotInstance> instances,
            boolean expiredAckedInstances) {

        if (!isPlaceholderPopulationEnabled(instanceRequestEntity)) {
            log.debug("Skipping placeholder population for request-id {}: "
                            + "populateAcknowledgedInstances={}, checkBatchwiseInfo={}",
                    instanceRequestEntity.getRequestId(),
                    icmsConfigurationProperties.isPopulateAcknowledgedInstances(),
                    isBatchWiseCheckEnabled(instanceRequestEntity));
            return;
        }

        ClientRequestDataModel clientData = instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest());
        int requestedInstancesCount = clientData.getInstanceCount();
        int createdInstancesCount = instancesInfo == null ? 0 : instancesInfo.size();

        if (tryEmitCanceledScheduleExpiredPlaceholders(instanceRequestEntity, clientData,
                requestedInstancesCount, createdInstancesCount, instances,
                expiredAckedInstances)) {
            return;
        }

        // "Still-active" path. Reaches here only for requests that are NOT
        // canceled+schedule-expired. From the remainder, we only continue for
        // requests that can still make progress (open/active + pending-fulfillment/fulfilled)
        if (!considerRequestForAckedInstancePopulation(instanceRequestEntity, Collections.emptySet())) {
            return;
        }

        if (createdInstancesCount == requestedInstancesCount) {
            return;
        }

        CreatedInstanceCounts created =
                countCreatedInstancesByProvider(instancesInfo, createdInstancesCount);

        BatchClassification classification =
                classifyBatchesForRequest(instanceRequestEntity, clientData, healthyZones);

        int startingEmitted = emitStartingPlaceholders(instanceRequestEntity, clientData,
                classification, created, instances);

        if (!expiredAckedInstances) {
            return;
        }
        int remainingCapacity = Math.max(
                requestedInstancesCount - createdInstancesCount - startingEmitted, 0);
        emitTerminatedPlaceholders(instanceRequestEntity, clientData,
                classification.terminatedBatchReasons(), remainingCapacity, instances);
    }

    /**
     * Short-circuit branch for a request that the cancel cron
     * ({@code CancelRequestEventService}) has moved to CANCELED + SCHEDULE_EXPIRED.
     * The cron only flips the parent when no real instances exist for the request,
     * so in the common case {@code createdInstancesCount} is 0 and we emit
     * {@code requestedInstancesCount} TERMINATED placeholders. The subtraction
     * guards the rare race window where an in-flight Cluster Agent launch lands on the
     * request between the cron's "no instances" check and the cancel write - in
     * that case we still cap the total response at the requested count by only
     * filling the gap. Each placeholder carries the parent's statusMessage so the
     * caller knows why. Returns true when the branch fires (caller should stop).
     */
    private boolean tryEmitCanceledScheduleExpiredPlaceholders(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData,
            int requestedInstancesCount,
            int createdInstancesCount,
            List<SpotInstance> instances,
            boolean expiredAckedInstances) {
        if (!expiredAckedInstances
                || instanceRequestEntity.getState() != SpotInstanceRequestState.CANCELED
                || !SpotRequestStatusCode.SCHEDULE_EXPIRED.toString()
                        .equals(instanceRequestEntity.getStatusCode())) {
            return false;
        }
        int missingCount = Math.max(requestedInstancesCount - createdInstancesCount, 0);
        for (int i = 0; i < missingCount; i++) {
            instances.add(buildTerminatedPlaceholder(instanceRequestEntity, clientData,
                                                          instanceRequestEntity.getStatusMessage()));
        }
        return true;
    }

    /**
     * Splits the real DB instances we already have for the request into non-BYOC vs
     * BYOC buckets. Used to compute the healthy-ack gap when deciding how many
     * STARTING placeholders to add for each provider.
     */
    private CreatedInstanceCounts countCreatedInstancesByProvider(
            List<InstanceV2Entity> instancesInfo,
            int createdInstancesCount) {
        if (createdInstancesCount == 0) {
            return new CreatedInstanceCounts(0, 0);
        }
        int nonByoc = (int) instancesInfo.stream()
                .filter(instance -> computePlatformService.isComputePlatformProvider(instance.getResourceProvider()))
                .count();
        return new CreatedInstanceCounts(nonByoc, createdInstancesCount - nonByoc);
    }

    /**
     * Each batch contributes either to 
     * the healthy ACK gap (STARTING) or to the list of terminated reasons,
     * per the table below. SQS Message Batches are sorted by {@code creationTime} ASC so the
     * earliest failed batch fills the requested-count cap first.
     *
     * <pre>
     *   PENDING_FULFILLMENT, healthy zone, not expired -> healthyAckNonByoc / healthyAckByoc
     *   PENDING_FULFILLMENT, unhealthy zone            -> CLUSTER_UNHEALTHY_ERROR_LOG
     *   PENDING_FULFILLMENT, batch expired             -> ACK_EXPIRED_ERROR_LOG
     *   CANNOT_FULFILL                                 -> CANNOT_FULFILL_ERROR_LOG
     *   SCHEDULE_EXPIRED                               -> SCHEDULE_EXPIRED_BATCH_ERROR_LOG
     * </pre>
     */
    private BatchClassification classifyBatchesForRequest(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData,
            Set<String> healthyZones) {

        int requestCancelDurationInMinForByoc = isModelCacheEnabled(
                clientData.getLaunchSpecification().getModelCacheEnabled())
                ? byocDescribeHelper.getByocValidationDurationWithModel()
                : byocDescribeHelper.getByocValidationDurationWithoutModel();

        List<SqsMessageEntity> sortedBatches =
                sqsMessageRepository.findByRequestId(instanceRequestEntity.getRequestId())
                        .stream()
                        .sorted(Comparator.comparing(SqsMessageEntity::getCreationTime,
                                Comparator.nullsLast(Comparator.naturalOrder())))
                        .toList();

        int healthyAckNonByoc = 0;
        int healthyAckByoc = 0;
        List<BatchTerminatedReason> terminatedBatchReasons = new ArrayList<>();

        for (SqsMessageEntity sqsMessageEntity : sortedBatches) {
            int ackedOnBatch = sqsMessageEntity.getAcknowledgedInstances() == null
                    ? 0 : sqsMessageEntity.getAcknowledgedInstances();
            if (ackedOnBatch == 0) {
                continue;
            }

            Optional<BatchTerminatedReason> terminatedReason =
                    resolveTerminatedReason(sqsMessageEntity, ackedOnBatch, healthyZones,
                            instanceRequestEntity, requestCancelDurationInMinForByoc);
            if (terminatedReason.isPresent()) {
                terminatedBatchReasons.add(terminatedReason.get());
                continue;
            }

            if (sqsMessageEntity.getStatus() != PENDING_FULFILLMENT) {
                continue;
            }

            if (computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(),
                              instanceRequestEntity.getResourceProvider())) {
                healthyAckNonByoc += ackedOnBatch;
            } else {
                healthyAckByoc += ackedOnBatch;
            }
        }

        return new BatchClassification(healthyAckNonByoc, healthyAckByoc, terminatedBatchReasons);
    }

    /**
     * Returns the per-batch TERMINATED reason when one applies, or empty when the
     * batch is healthy and should contribute to STARTING placeholders. Non-recognized
     * statuses (e.g. PENDING_EVALUATION) also return empty - the caller then skips
     * the batch because it is not in PENDING_FULFILLMENT either.
     */
    private Optional<BatchTerminatedReason> resolveTerminatedReason(
            SqsMessageEntity sqsMessageEntity,
            int ackedOnBatch,
            Set<String> healthyZones,
            InstanceRequestV2Entity instanceRequestEntity,
            int requestCancelDurationInMinForByoc) {
        SpotRequestStatusCode status = sqsMessageEntity.getStatus();
        if (status == CANNOT_FULFILL) {
            return Optional.of(new BatchTerminatedReason(ackedOnBatch, CANNOT_FULFILL_ERROR_LOG));
        }
        if (status == SCHEDULE_EXPIRED) {
            return Optional.of(
                    new BatchTerminatedReason(ackedOnBatch, SCHEDULE_EXPIRED_BATCH_ERROR_LOG));
        }
        if (status != PENDING_FULFILLMENT) {
            return Optional.empty();
        }

        if (!healthyZones.contains(sqsMessageEntity.getZone())) {
            log.debug("For request-id {} message-batch-id {} zone {} is unhealthy. "
                            + "Surfacing {} acked instances as TERMINATED",
                    sqsMessageEntity.getKey().getRequestId(),
                    sqsMessageEntity.getKey().getMessageBatchId(),
                    sqsMessageEntity.getZone(),
                    ackedOnBatch);
            return Optional.of(new BatchTerminatedReason(ackedOnBatch, CLUSTER_UNHEALTHY_ERROR_LOG));
        }

        boolean isNonByoc = computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(),
                                      instanceRequestEntity.getResourceProvider());
        boolean batchExpired = isNonByoc
                ? instanceDescriptionHelper.isNonByocBatchExpired(instanceRequestEntity.getResourceProvider(),
                                                      sqsMessageEntity)
                : byocDescribeHelper.isByocBatchExpired(instanceRequestEntity.getResourceProvider(),
                                                           sqsMessageEntity,
                                                           requestCancelDurationInMinForByoc);
        if (batchExpired) {
            log.debug("For request-id {} message-batch-id {} zone {} waiting time for {} cloud "
                            + "provider passed. Surfacing {} acked instances as TERMINATED",
                    sqsMessageEntity.getKey().getRequestId(),
                    sqsMessageEntity.getKey().getMessageBatchId(),
                    sqsMessageEntity.getZone(),
                    sqsMessageEntity.getCloudProvider(),
                    ackedOnBatch);
            return Optional.of(new BatchTerminatedReason(ackedOnBatch, ACK_EXPIRED_ERROR_LOG));
        }

        return Optional.empty();
    }

    /**
     * Emits one STARTING placeholder per missing healthy instance (separately per
     * provider, so a non-BYOC-only deficit is not filled by a BYOC ACK). Returns the
     * total number of STARTING placeholders added.
     */
    private int emitStartingPlaceholders(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData,
            BatchClassification classification,
            CreatedInstanceCounts created,
            List<SpotInstance> instances) {
        int expectedInstances =
                Math.max(classification.healthyAckNonByoc() - created.nonByoc(), 0)
                        + Math.max(classification.healthyAckByoc() - created.byoc(), 0);
        for (int i = 0; i < expectedInstances; i++) {
            instances.add(buildPendingInstance(instanceRequestEntity, clientData));
        }
        return expectedInstances;
    }

    /**
     * Emits TERMINATED placeholders for failed batches in the order they were
     * collected (earliest creationTime first), capped at {@code remainingCapacity}
     * so total response size never exceeds the requested instance count.
     */
    private void emitTerminatedPlaceholders(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData,
            List<BatchTerminatedReason> terminatedBatchReasons,
            int remainingCapacity,
            List<SpotInstance> instances) {
        for (BatchTerminatedReason reason : terminatedBatchReasons) {
            if (remainingCapacity == 0) {
                break;
            }
            int emitCount = Math.min(reason.ack(), remainingCapacity);
            for (int i = 0; i < emitCount; i++) {
                instances.add(buildTerminatedPlaceholder(instanceRequestEntity, clientData,
                                                              reason.errorLog()));
            }
            remainingCapacity -= emitCount;
        }
    }

    /**
     * Lightweight per-batch carrier used while walking sorted SQS rows: the original
     * batch ack count and the reason string that should be attached to each emitted
     * TERMINATED placeholder.
     */
    private record BatchTerminatedReason(int ack, String errorLog) { }

    /**
     * Per-provider real-instance counts already in the DB for the request.
     */
    private record CreatedInstanceCounts(int nonByoc, int byoc) { }

    /**
     * Output of {@link #classifyBatchesForRequest}: the healthy ACK totals per
     * provider (feed STARTING placeholders) and the ordered terminated reasons
     * (feed TERMINATED placeholders, capped at request size).
     */
    private record BatchClassification(
            int healthyAckNonByoc,
            int healthyAckByoc,
            List<BatchTerminatedReason> terminatedBatchReasons) { }

    /**
     * Master gate for emitting any placeholder rows on the deployment GET path. Matches the
     * config / batch-info gating used by {@link #considerRequestForAckedInstancePopulation},
     * but is independent of request state/status so the canceled flow can also populate
     * terminated placeholders.
     */
    private boolean isPlaceholderPopulationEnabled(InstanceRequestV2Entity instanceRequestEntity) {
        return icmsConfigurationProperties.isPopulateAcknowledgedInstances()
                && isBatchWiseCheckEnabled(instanceRequestEntity);
    }

    private SpotInstance buildPendingInstance(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData) {
        SpotInstance instance = buildPlaceholderInstance(instanceRequestEntity, clientData);
        instance.setState(new SpotInstanceState(
                SpotInstanceInternalState.getStateCode(STARTING),
                SpotInstanceInternalState.STARTING.getStateName()));
        return instance;
    }

    private SpotInstance buildTerminatedPlaceholder(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData,
            String errorLog) {
        SpotInstance instance = buildPlaceholderInstance(instanceRequestEntity, clientData);
        instance.setState(new SpotInstanceState(
                SpotInstanceInternalState.getStateCode(TERMINATED),
                SpotInstanceInternalState.TERMINATED.getStateName()));
        if (!StringUtils.isEmpty(errorLog)) {
            instance.setHealthInfo(HealthInfo.builder().errorLog(errorLog).build());
        }
        return instance;
    }

    private SpotInstance buildPlaceholderInstance(
            InstanceRequestV2Entity instanceRequestEntity,
            ClientRequestDataModel clientData) {
        SpotInstance instance = new SpotInstance();
        instance.setLaunchRequestId(instanceRequestEntity.getRequestId());
        instance.setInstanceType(clientData.getLaunchSpecification().getInstanceType());
        instance.setContainerImage(clientData.getLaunchSpecification().getContainerImage());
        instance.setRequestId(instanceRequestEntity.getRequestId());
        instance.setUpdateTime(instanceRequestEntity.getStatusUpdateTime());
        instance.setGpu(clientData.getLaunchSpecification().getGpu());
        if (clientData.getLaunchSpecification().getDeploymentId() != null) {
            instance.setDeploymentId(clientData.getLaunchSpecification().getDeploymentId().toString());
        }
        if (clientData.getLaunchSpecification().getGpuSpecificationId() != null) {
            instance.setGpuSpecificationId(
                    clientData.getLaunchSpecification().getGpuSpecificationId().toString());
        }
        return instance;
    }

    /*
       To populate acked instances below condition should meet:
       1. Config control flag must be set
       2. batchWise check must be enabled to request
       3. Request state must be "open" or "active" (with feature flag) and 
          request-status must be "pending-fulfillment" or "fulfilled"
       4. stateFilter either be empty or contain the request's current state
     */
    private boolean considerRequestForAckedInstancePopulation(
            InstanceRequestV2Entity instanceRequestEntity, Set<String> stateFilter) {
        boolean isValidState = InstanceServiceHelper.isRequestInOpenOrActiveState(
                instanceRequestEntity, icmsConfigurationProperties);

        boolean isValidStatus = instanceRequestEntity.getStatusCode()
                .equals(SpotRequestStatusCode.PENDING_FULFILLMENT.toString()) ||
                instanceRequestEntity.getStatusCode()
                        .equals(SpotRequestStatusCode.FULFILLED.toString());

        boolean isInStateFilter = isRequestIdIncludedBasedOnInstanceState(stateFilter,
                Set.of(SpotInstanceRequestState.OPEN.toString(),
                        SpotInstanceRequestState.ACTIVE.toString()));

        return icmsConfigurationProperties.isPopulateAcknowledgedInstances() &&
                isBatchWiseCheckEnabled(instanceRequestEntity) &&
                isValidState &&
                isValidStatus &&
                isInStateFilter;
    }

    private void addDefaultResponse(
            Set<String> stateFilter, InstanceRequestV2Entity instanceRequestEntity,
            List<SpotInstanceRequest> instanceRequests) {
        if (isRequestIdIncludedBasedOnInstanceState(stateFilter,
                Set.of(instanceRequestEntity.getState().toString()))) {

            // If acked instances are added then don't need to add default entry
            if (considerRequestForAckedInstancePopulation(instanceRequestEntity, stateFilter)) {
                return;
            }

            // Adding default customData response for requests WITHOUT instances:
            // Uses the request's actual state/status (not instance state):
            //
            // 1. request-state: open & request-status: pending-evaluation (newly created request)
            // 2. request-state: open & request-status: pending-fulfillment (when batchWiseCheck not enabled)
            // 3. request-state: active & request-status: fulfilled (when batchWiseCheck not enabled, feature flag enabled,
            //    and at least one instance reported elsewhere)
            // 4. request-state: canceled & request-status: canceled-before-fulfillment (request canceled by user)
            // 5. request-state: closed & request-status: request-closed-by-user (when no instances created and request terminated)
            //
            // Note: This differs from acknowledged instances which always show OPEN + PENDING_FULFILLMENT.
            instanceRequests.add(getCustomDataFromInstanceRequestEntity(instanceRequestEntity));
        }
    }

    private boolean isBatchWiseCheckEnabled(InstanceRequestV2Entity instanceRequestEntity) {
        return instanceRequestEntity.getCheckBatchwiseInfo() != null &&
                instanceRequestEntity.getCheckBatchwiseInfo();
    }

    private void addAcknowledgedInstances(
            List<SpotInstanceRequest> instanceRequests,
            InstanceRequestV2Entity instanceRequestEntity,
            Integer expectedInstanceCount) {
        for (int i = 0; i < expectedInstanceCount; i++) {
            // Acknowledged instances (zone acknowledged but not yet created) always show:
            // State: OPEN, Status: PENDING_FULFILLMENT
            // This is true regardless of the request's actual state (which may be
            // ACTIVE + FULFILLED if the first instance has already reported and transitioned
            // the request state)
            instanceRequests.add(getCustomDataForPendingFulfillment(instanceRequestEntity));
        }
    }

    private Set<String> getHealthyZones() {
        return cloudHealthRepository.finalAllHealthyZones();
    }

    /*
    A request can be served my multiple zones so to find acked instances we will consider zoneHealth
    1. Health of zone which served that SQS batch must be healthy
    2. Status of SQS batch must be pending-fulfillment
     */
    private Map<String, Integer> getAcknowledgedInstances(Set<String> healthyZones,
                                                           InstanceRequestV2Entity instanceRequestEntity,
                                                           ClientRequestDataModel clientData) {
        List<SqsMessageEntity> sqsMessageEntities =
                sqsMessageRepository.findByRequestId(instanceRequestEntity.getRequestId());
        int nonByocAckedInstances = 0;
        int byocAckInstances = 0;

        boolean isModelCacheEnabled = isModelCacheEnabled(clientData.getLaunchSpecification().getModelCacheEnabled());
        int requestCancelDurationInMinForByoc = byocDescribeHelper.getByocValidationDurationWithoutModel();
        if (isModelCacheEnabled) {
            requestCancelDurationInMinForByoc = byocDescribeHelper.getByocValidationDurationWithModel();
        }

        for (SqsMessageEntity sqsMessageEntity : sqsMessageEntities) {
            if (sqsMessageEntity.getStatus() == PENDING_FULFILLMENT) {
                if (healthyZones.contains(sqsMessageEntity.getZone())) {

                    if (computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(), instanceRequestEntity.getResourceProvider()) &&
                            !instanceDescriptionHelper.isNonByocBatchExpired(instanceRequestEntity.getResourceProvider(),
                                                                  sqsMessageEntity)) {
                        nonByocAckedInstances += sqsMessageEntity.getAcknowledgedInstances();
                    } else if (!computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(), instanceRequestEntity.getResourceProvider()) &&
                            !byocDescribeHelper.isByocBatchExpired(instanceRequestEntity.getResourceProvider(),
                                                                      sqsMessageEntity, requestCancelDurationInMinForByoc)) {
                        byocAckInstances += sqsMessageEntity.getAcknowledgedInstances();
                    } else {
                        log.info(
                                "For request-id {} message-batch-id {} zone {} waiting time for {} cloud provider passed. " +
                                        "Avoiding populating {} acked instances",
                                sqsMessageEntity.getKey().getRequestId(),
                                sqsMessageEntity.getKey().getMessageBatchId(), sqsMessageEntity.getZone(),
                                sqsMessageEntity.getCloudProvider(),
                                sqsMessageEntity.getAcknowledgedInstances());
                    }

                } else {
                    log.debug("For request-id {} message-batch-id {} zone {} is unhealthy. " +
                                     "Avoiding populating {} acked instances",
                             sqsMessageEntity.getKey().getRequestId(),
                             sqsMessageEntity.getKey().getMessageBatchId(), sqsMessageEntity.getZone(),
                             sqsMessageEntity.getAcknowledgedInstances());
                }
            }
        }
        return Map.of(NON_BYOC_ACKED_INSTANCES_KEY, nonByocAckedInstances,
                      BYOC_ACKED_INSTANCES_KEY, byocAckInstances);
    }

    private SpotInstanceRequest getCustomDataFromInstanceRequestEntity(
            InstanceRequestV2Entity instanceRequestEntity) {
        SpotInstanceRequestStatus instanceRequestStatus =
                new SpotInstanceRequestStatus(instanceRequestEntity.getStatusCode(),
                                              instanceRequestEntity.getStatusMessage(),
                                              instanceRequestEntity.getStatusUpdateTime());
        return getCustomRequestData(instanceRequestEntity, instanceRequestStatus);
    }

    private SpotInstanceRequest getCustomDataForPendingEvaluation(
            InstanceRequestV2Entity instanceRequestEntity) {
        SpotInstanceRequestStatus instanceRequestStatus =
                new SpotInstanceRequestStatus(PENDING_EVALUATION.toString(),
                                              "open",
                                              TimeUtils.getInstantFromUuid(instanceRequestEntity.getCreateTimeuuid()));
        return getCustomRequestData(instanceRequestEntity, instanceRequestStatus);
    }

    private SpotInstanceRequest getCustomDataForPendingFulfillment(
            InstanceRequestV2Entity instanceRequestEntity) {
        SpotInstanceRequestStatus instanceRequestStatus =
                new SpotInstanceRequestStatus(PENDING_FULFILLMENT.toString(),
                        instanceRequestEntity.getStatusMessage(),
                        instanceRequestEntity.getStatusUpdateTime());
        SpotInstanceRequest instanceRequest =
                getCustomRequestData(instanceRequestEntity, instanceRequestStatus);
        // Override state to OPEN for acknowledged instances (not yet created)
        // even if request has transitioned to ACTIVE
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        return instanceRequest;
    }

    private Map<String, List<InstanceV2Entity>> getMapOfRequestIdVsInstances(
            String customer,
            Set<String> requestIds) {
        // This will make 4 C* call per request-id
        return instanceV2Repository.findAllInstancesByCustomerAndRequestIds(customer, requestIds, false);
    }

    private Map<String, List<InstanceV2Entity>> getMapOfRequestIdVsInstances(
            List<InstanceRequestV2Entity> instanceRequestV2Entities) {

        Set<String> requestIds = new HashSet<>();
        if (instanceRequestV2Entities != null) {
            instanceRequestV2Entities.forEach(r -> requestIds.add(r.getRequestId()));
        }

        return instanceV2Repository.findAllInstancesByCustomerAndRequestIds(null, requestIds, false);
    }

    private List<InstanceRequestV2Entity> findByRequestIds(
            Set<String> requestIds) {
        return instanceRequestV2Repository.findRequestsByIds(requestIds);
    }

    private List<InstanceRequestV2Entity> findByCustomerAndRequestIds(
            String customer,
            Set<String> requestIds) {
        return instanceRequestV2Repository.findRequestsByIdsAndCustomer(requestIds, customer);
    }

    private ClientRequestDataModel getClientRequestDataModelForRequest(
            @NotNull InstanceRequestV2Entity instanceRequestEntity) {
        try {
            return objectMapper.readValue(instanceRequestEntity.getRequest(), ClientRequestDataModel.class);
        } catch (Exception e) {
            String errMsg = String.format("Failed to get request information, error: %s", e.getMessage());
            log.error("error: {}", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

    private SpotInstance generateInstance(String instanceType,
                                              @NotNull ZoneInfo zoneInfo,
                                              @NotNull ClientRequestDataModel requestData,
                                              @NotNull InstanceV2Entity instanceEntity) {
        SpotInstance instance = new SpotInstance();
        instance.setInstanceType(instanceType);
        instance.setSpotCloudProvider(zoneInfo.getCloudProvider());
        instance.setRequestId(instanceEntity.getRequestId());
        instance.setUpdateTime(instanceEntity.getRequestStatusUpdateTime());
        instance.setGpu(requestData.getLaunchSpecification().getGpu());
        instance.setContainerImage(
                requestData.getLaunchSpecification().getContainerImage());

        instance.setInstanceId(instanceEntity.getInstanceId());

        instance.setState(new SpotInstanceState(instanceEntity.getInstanceStateCode(),
                instanceEntity.getInstanceStateName()
                        .getStateName()));

        instance.setPlacement(
                new SpotInstanceLaunchSpecification.Placement(zoneInfo.getZoneName()));

        instance.setLaunchRequestId(instanceEntity.getRequestId());
        instance.setImageId(instanceEntity.getImageId());

        // Set Capacity type for the instance
        instance.setCapacityType(getCapacityType(instanceEntity));

        if (!StringUtils.isEmpty(instanceEntity.getErrorLog())) {
            HealthInfo healthInfo = HealthInfo.builder()
                    .errorLog(instanceEntity.getErrorLog())
                    .build();
            instance.setHealthInfo(healthInfo);
        }
        if (instanceEntity.getInstanceIps() != null &&
                !instanceEntity.getInstanceIps().isEmpty()) {
            instance.setInstanceIps(instanceEntity.getInstanceIps());
        } else {
            // Sent empty instanceIps if ips are not present in entity
            instance.setInstanceIps(new HashSet<>());
        }
        if (requestData.getLaunchSpecification().getDeploymentId() != null) {
            instance.setDeploymentId(requestData.getLaunchSpecification().getDeploymentId().toString());
        }

        if (requestData.getLaunchSpecification().getGpuSpecificationId() != null) {
            instance.setGpuSpecificationId(requestData.getLaunchSpecification().getGpuSpecificationId().toString());
        }
        instance.setCreateTime(TimeUtils.getInstantFromUuid(instanceEntity.getCreateTimeuuid()));

        return instance;
    }

    private CapacityType getCapacityType(@NotNull InstanceV2Entity instanceEntity) {
        if (!StringUtils.isEmpty(instanceEntity.getCapacityType())) {
            try {
                return CapacityType.valueOf(instanceEntity.getCapacityType());
            } catch (IllegalArgumentException e) {
                log.warn("Invalid capacity type: {}, defaulting to SPOT", instanceEntity.getCapacityType());
                return CapacityType.SPOT;
            }
        } else {
            return CapacityType.SPOT;
        }
    }
}
