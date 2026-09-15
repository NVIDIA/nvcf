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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.TERMINATE_INSTANCES;
import static com.nvidia.icms.service.telemetry.model.Events.CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForShuttingInstance;

import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.extensions.api.InstanceTerminationService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.audit.AuditUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ByocTerminateService {

    public static final String TERMINATION_MESSAGE_PREFIX = "byoc-terminate";

    public static final String MISSING_CLUSTER_INFO_ERROR_LOG = "Instance terminated due to "
            + "unavailability of cluster";

    public static final String ICMS_ERROR_SOURCE = "sis";

    private final InstanceV2Repository instanceV2Repository;

    private final AppAuditService auditService;

    private final TelemetryEventClient telemetryEventClient;

    private final ClusterRepository clusterRepository;

    private final InstanceTerminationService instanceTerminationService;

    private final InstanceServiceHelper instanceServiceHelper;

    private final FunctionDeploymentStagesService functionDeploymentStagesService;

    private final ComputePlatformService computePlatformService;

    @Observed
    public TerminateInstancesResponse terminateInstances(
            @NotNull Set<InstanceV2Entity> instanceEntities,
            @NotNull Map<String, Object> auditProps) {

        // TODO: SIS-1036 Create a utility to generate requestIdToClusterEntitiesMap, requestIdToInstanceEntitiesMap
        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        List<TerminateInstancesResponse.TerminatingInstance> terminatedInstances =
                new ArrayList<>();
        List<InstanceV2Entity> instanceEntitiesToUpdateInDb = new ArrayList<>();
        List<GenericMetric> genericMetricList = new ArrayList<>();
        Map<String, InstanceV2Entity> entitiesBefore = new HashMap<>();

        for (InstanceV2Entity entity : instanceEntities) {

            if (entity.getInstanceStateName() ==
                    SpotInstanceInternalState.RUNNING ||
                    entity.getInstanceStateName() ==
                            SpotInstanceInternalState.STARTING) {

                // Adding entries in requestIdToClusterEntititiesMap
                // For BYOC zone name will be cluster-id
                if (!requestIdToClusterEntitiesMap.containsKey(entity.getRequestId())) {
                    // If cluster info is missing due to un-registration then terminate instance
                    ClusterEntity clusterEntity =
                            getClusterAndTerminateInstancesForUnregisteredCluster(entity, auditProps);
                    if (clusterEntity == null) {
                        continue;
                    }

                    // First time adding request entity in requestId to clusterEntities Map
                    Set<ClusterEntity> clusterEntities = new HashSet<>();
                    clusterEntities.add(clusterEntity);
                    requestIdToClusterEntitiesMap.put(entity.getRequestId(), clusterEntities);

                    requestIdToClusterEntitiesMap.computeIfAbsent(
                            entity.getRequestId(), key -> new HashSet<>(Set.of(clusterEntity)))
                            .add(clusterEntity);
                } else {

                    // If requestIdToClusterEntitiesMap doesn't contain clusterEntity for clusterId of instance
                    // Then add to the set of clusterEntity for this requestId
                    Optional<ClusterEntity> optionalClusterEntity =
                            requestIdToClusterEntitiesMap.get(entity.getRequestId())
                            .stream()
                            .filter(cluster-> cluster.getClusterId().equals(entity.getZone()))
                            .findFirst();

                    if (optionalClusterEntity.isEmpty()) {
                        // If cluster info is missing due to un-registration then terminate instance
                        ClusterEntity clusterEntity =
                                getClusterAndTerminateInstancesForUnregisteredCluster(entity, auditProps);
                        if (clusterEntity == null) {
                            continue;
                        }

                        // Adding new clusterEntity in requestId to clusterEntities Map
                        requestIdToClusterEntitiesMap.computeIfAbsent(
                                entity.getRequestId(), key -> new HashSet<>(Set.of(clusterEntity)))
                                .add(clusterEntity);
                    }
                }

                // Adding entries in requestIdToInstanceIdsMap
                if (requestIdToInstanceEntitiesMap.containsKey(entity.getRequestId())) {
                    requestIdToInstanceEntitiesMap.get(entity.getRequestId())
                            .add(entity);
                } else {
                    Set<InstanceV2Entity> instanceIds = new HashSet<>();
                    instanceIds.add(entity);
                    requestIdToInstanceEntitiesMap.put(entity.getRequestId(), instanceIds);
                }

                // Generating termination instance response
                entitiesBefore.put(entity.getInstanceId(),
                                   AuditUtils.deepCopyInstanceEntity(entity));

                terminatedInstances.add(generateTerminateInstanceResponse(entity));

                // Generating metrics response
                Optional<ClusterEntity> clusterEntity = requestIdToClusterEntitiesMap
                        .get(entity.getRequestId()).stream()
                        .filter(cluster -> cluster.getClusterId().equals(entity.getZone()))
                        .findFirst();
                // Cluster entity always be present as we are ensuring it while adding it
                // One telemetry event per instance entity is being sent
                // ClusterEntity is used for cloudProvider and termination Queue URL
                clusterEntity.ifPresent(cluster -> genericMetricList.add(
                        getTelemetryMetricForInstanceTermination(entity,
                                                                 cluster)));

                // Updating instance entity
                entity.setInstanceStateCode(
                        SpotInstanceInternalState.getStateCode(
                                SpotInstanceInternalState.SHUTTING_DOWN));
                entity.setInstanceStateName(
                        SpotInstanceInternalState.SHUTTING_DOWN);
                instanceEntitiesToUpdateInDb.add(entity);
            }
        }

        // 1. Putting message for termination in SQS
        sendSqsMessageForInstanceTermination(requestIdToClusterEntitiesMap,
                                             requestIdToInstanceEntitiesMap);

        // 2. Updating C* with terminating instances
        updateInstancesForNewState(instanceEntitiesToUpdateInDb, auditProps, entitiesBefore);

        // 2a. Send messages to FnDS
        sendDeploymentStageMessageForShuttingDown(instanceEntitiesToUpdateInDb);

        // 3. Sending telemetry event
        telemetryEventClient.triggerEvent(genericMetricList);
        // 3a. Send latest instance state event for all instances transitioned to SHUTTING_DOWN
        for (InstanceV2Entity instanceEntity : instanceEntitiesToUpdateInDb) {
            instanceServiceHelper.sendLatestInstanceStateEvent(instanceEntity);
        }

        // 4. Sending termination response
        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();
        terminateInstancesResponse.setTerminatingInstances(terminatedInstances);

        return terminateInstancesResponse;
    }

    private ClusterEntity getClusterAndTerminateInstancesForUnregisteredCluster(InstanceV2Entity entity,
                                                          Map<String, Object> auditProps) {

        // If cluster info is missing due to un-registration then terminate instance
        Optional<ClusterEntity> clusterEntity =
                getClusterEntityFromClusterId(entity.getZone());

        if (clusterEntity.isEmpty()) {
            // Terminate instance state and update in database
            InstanceV2Entity instanceEntityBefore = CopyUtil.deepCopy(entity);
            handleInstancesWithMissingClusterInfo(entity);
            AuditUtils.populateAuditValuesForTerminateInstance(
                    auditProps, entity.getInstanceId());
            auditService.sendAuditEventForInstance(
                    auditProps, instanceEntityBefore, entity);

            // 4. Sending GPU usage event for terminated instance
            instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);
            instanceServiceHelper.sendLatestInstanceStateEvent(entity);

            return null;
        }
        return clusterEntity.get();
    }

    public void handleInstancesWithMissingClusterInfo(InstanceV2Entity instanceEntity) {
        try {
            updateInstanceEntityState(instanceEntity, MISSING_CLUSTER_INFO_ERROR_LOG);
            instanceV2Repository.update(instanceEntity);

            String errMsg = String.format(
                    "%s instance having BYOC resource provider but can "
                            + "not find cluster info for %s cluster-id",
                    instanceEntity.getInstanceId(), instanceEntity.getZone());
            log.warn(errMsg);
            telemetryEventClient.triggerEvent(List.of(
                    new GenericMetric().withEventName(
                                    CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID.toString())
                            .withError(errMsg)
                            .withZoneName(instanceEntity.getZone())
                            .withResourceProvider(ResourceProvider.BYOC)
                            .withRequestId(instanceEntity.getRequestId())
                            .withInstanceId(instanceEntity.getInstanceId())));
        } catch (Exception exception) {
            String errMsg = String.format("Exception occurred while handling missing cluster info "
                                                  + "for %s instance id and %s request id, error: %s ",
                                          instanceEntity.getInstanceId(),
                                          instanceEntity.getRequestId(),
                                          exception.getMessage());
            log.error(errMsg, exception);
            java.util.Map<String, Object> metaData = new HashMap<>();
            metaData.put(TelemetryEventClient.EventMetaData.ERROR_ORIGIN_INFO.getName(),
                         "handleInstancesWithMissingClusterInfo");
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(
                                                                       Events.UNEXPECTED_ERROR_OCCURRED.toString())
                                                               .withMetadata(metaData)
                                                               .withError(errMsg)));
        }
    }

    // TODO: Move this method to InstanceServiceHelper
    public InstanceV2Entity updateInstanceEntityState(
            InstanceV2Entity instanceEntity,
            String errorLog) {
        instanceEntity.setInstanceStateCode(SpotInstanceInternalState.getStateCode(
                SpotInstanceInternalState.TERMINATED));
        instanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        instanceEntity.setRequestStatusCode(SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
        instanceEntity.setRequestStatusMessage(String.format("Instance status updated to %s",
                                                                 SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE));
        instanceEntity.setRequestStatusUpdateTime(Instant.now());
        instanceEntity.setInstanceUpdateTime(Instant.now());
        instanceEntity.setRequestState(SpotInstanceRequestState.CLOSED);
        instanceEntity.setErrorLog(errorLog);
        // Instance is terminated by SIS either for unhealthy cloud, missing cluster info, or expired instance
        instanceEntity.setErrorSource(ICMS_ERROR_SOURCE);
        return instanceEntity;
    }

    @Observed
    public TerminateInstancesResponse terminateInstanceRequests(
            Map<String, InstanceV2Entity> runningInstancesEntityMap,
            Map<String, Object> auditProps) {
        return terminateInstances(new HashSet<>(runningInstancesEntityMap.values()),
                                       auditProps);
    }

    /**
     * Puts messages in SQS queue for instance termination
     *
     * @param requestIdToClusterEntitiesMap request-id to {@link Set<ClusterEntity>} map
     * @param requestIdToInstanceEntitiessMap request-id to instance-entities map
     */
    public void sendSqsMessageForInstanceTermination(
            Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap,
            Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiessMap) {
        Map<String, List<ByocTerminatePodMessageModel>> queueToTerminateMessageMap =
                new HashMap<>();
        Map<String, String> queueToClusterIdMap = new HashMap<>();

        // For each requestId process the termination
        for (Map.Entry<String, Set<InstanceV2Entity>> entry : requestIdToInstanceEntitiessMap.entrySet()) {
            String requestId = entry.getKey();
            Set<InstanceV2Entity> instanceEntities = entry.getValue();

            // If instances for a request is empty then skip it
            if (instanceEntities.isEmpty()) {
                continue;
            }

            // For each clusterId associated with requestId process the termination
            for (ClusterEntity clusterEntity : requestIdToClusterEntitiesMap.get(requestId)) {

                // Filter instances for current cluster/zone
                Set<String> instanceIds = getInstanceIdsForCluster(instanceEntities, clusterEntity);

                // If cluster belongs to a first-party compute platform then process the platform termination flow
                if (computePlatformService.isPlatformCluster(clusterEntity.getClusterGroupName())) {
                    // Get the customer value from the first instance entity as for request it will be same
                    String customer = instanceEntities.iterator().next().getCustomer();

                    // Send sns message for non-BYOC zone for instances of a requestId
                    instanceTerminationService.sendSnsTerminationMessage(requestId, customer,
                                                                         clusterEntity.getClusterId(),
                                                                         instanceIds);
                    continue;
                }

                String queueUrl = clusterEntity.getTerminationQueueUrl();

                if (!queueToClusterIdMap.containsKey(queueUrl)) {
                    queueToClusterIdMap.put(queueUrl, clusterEntity.getClusterId());
                }

                // SIS has to provide NCA_ID of the task/function owner, it is recorded in the instance
                // Since all instances belongs to the same reqeust, they have the same NCA_ID
                ByocTerminatePodMessageModel byocTerminatePodMessageModel =
                        getByocTerminatePodMessageModel(clusterEntity.getClusterName(),
                                                        requestId,
                                                        instanceIds,
                                                        instanceEntities.iterator().next().getNcaId());

                if (queueToTerminateMessageMap.containsKey(queueUrl)) {
                    queueToTerminateMessageMap.get(queueUrl)
                            .add(byocTerminatePodMessageModel);
                } else {
                    List<ByocTerminatePodMessageModel> byocTerminatePodMessageModels =
                            new ArrayList<>();
                    byocTerminatePodMessageModels.add(byocTerminatePodMessageModel);
                    queueToTerminateMessageMap.put(queueUrl, byocTerminatePodMessageModels);
                }
            }
        }

        for (Map.Entry<String, List<ByocTerminatePodMessageModel>> entry
                : queueToTerminateMessageMap.entrySet()) {
            try {
                instanceServiceHelper.sendTerminateMessage(entry.getKey(), entry.getValue(),
                                                       TERMINATION_MESSAGE_PREFIX,
                                                       queueToClusterIdMap.get(entry.getKey()));

            } catch (Exception exception) {
                log.error("Cloud not publish termination message in {} queue with {}" +
                                  " pod message. Exception - {}", entry.getKey(),
                          entry.getValue(), exception.getMessage(), exception);

                // rethrowing the exception
                throw exception;
            }
        }
    }

    private Set<String> getInstanceIdsForCluster(Set<InstanceV2Entity> instanceEntities,
                                                 ClusterEntity clusterEntity) {
        return instanceEntities
                .stream()
                .filter(instanceEntity -> instanceEntity.getZone().equals(clusterEntity.getClusterId()))
                .map(InstanceV2Entity::getInstanceId)
                .collect(Collectors.toSet());
    }

    /**
     * @param clusterId clusterId to fetch {@link ClusterEntity}
     * @return returns {@link ClusterEntity} associated to clusterId
     */
    public Optional<ClusterEntity> getClusterEntityFromClusterId(String clusterId) {
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, true);

        // Adding entries in requestIdToClusterEntityMap
        if (optionalClusterEntity.isEmpty()) {
            String errMsg =
                    String.format("Could not find cluster info for '%s' cluster id",
                                  clusterId);
            log.error(errMsg);
        }
        return optionalClusterEntity;
    }

    private GenericMetric getTelemetryMetricForInstanceTermination(
            InstanceV2Entity entity,
            ClusterEntity clusterEntity) {
        ClusterProviderEnum clusterProviderEnum = clusterEntity.getClusterProvider();
        CloudProvider cloudProvider = CloudProvider
                .getCloudProviderFromClusterProvider(clusterProviderEnum);

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(TelemetryEventClient.EventMetaData.TERMINATION_QUEUE.getName(),
                     clusterEntity.getTerminationQueueUrl());

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                instanceServiceHelper.getLaunchSpecificationForTelemetry(entity.getRequestRawData());

        return new GenericMetric()
                .withEventName(Events.SHUTTING_DOWN_INSTANCE.toString())
                .withCloudProvider(cloudProvider)
                .withResourceProvider(ResourceProvider.BYOC)
                .withMetadata(metadata)
                .withCustomer(entity.getCustomer())
                .withInstanceId(entity.getInstanceId())
                .withZoneName(entity.getZone())
                .withRequestId(entity.getRequestId())
                .withNcaId(launchSpecification.getNcaId())
                .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withInstanceType(launchSpecification.getInstanceType())
                .withInstanceState(
                        SpotInstanceInternalState.SHUTTING_DOWN.getStateName())
                .withDeploymentId(launchSpecification.getDeploymentId())
                .withGpuSpecificationId(launchSpecification.getGpuSpecificationId());
    }

    private TerminateInstancesResponse.TerminatingInstance generateTerminateInstanceResponse(
            InstanceV2Entity entity) {
        TerminateInstancesResponse.TerminatingInstance terminateInstance =
                new TerminateInstancesResponse.TerminatingInstance();
        terminateInstance.setInstanceId(entity.getInstanceId());
        terminateInstance.setRequestId(entity.getRequestId());
        terminateInstance.setPreviousState(new SpotInstanceState(
                entity.getInstanceStateCode(),
                entity.getInstanceStateName().getStateName()));
        // use code and message for state directly until we define them
        terminateInstance.setCurrentState(
                new SpotInstanceState(SpotInstanceInternalState.getStateCode(
                        SpotInstanceInternalState.SHUTTING_DOWN),
                                      SpotInstanceInternalState.SHUTTING_DOWN.getStateName()));
        return terminateInstance;
    }

    private void updateInstancesForNewState(
            List<InstanceV2Entity> instanceEntities,
            Map<String, Object> auditProps,
            Map<String, InstanceV2Entity> entitiesBefore) {
        for (InstanceV2Entity instanceEntity : instanceEntities) {
            try {
                instanceV2Repository.update(instanceEntity);
                populateAuditValuesForShuttingInstance(auditProps,
                                                           instanceEntity.getInstanceId());
                auditService.sendAuditEventForInstance(auditProps,
                                                           entitiesBefore.get(
                                                                   instanceEntity.getInstanceId()),
                                                           instanceEntity);
            } catch (Exception e) {
                // TODO(SIS-298): Sent telemetry event to capture this error
                log.error("Could not update instance state when terminating in the database for the"
                                  + " following instanceIds - {}, error - {}",
                          String.join(",", instanceEntity.getInstanceId()), e.getMessage(), e);
            }
        }
    }

    private void sendDeploymentStageMessageForShuttingDown(
            List<InstanceV2Entity> instanceEntities) {
        for (InstanceV2Entity instanceEntity : instanceEntities) {
           functionDeploymentStagesService.sendFunctionDeploymentStage(instanceEntity,
                                                                       FndsStages.REQUESTING_TERMINATION.toString());
        }
    }

    private ByocTerminatePodMessageModel getByocTerminatePodMessageModel(
            String clusterName,
            String requestId,
            Set<String> instanceIds,
            String nacId) {
        return ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction())
                .availabilityZone(clusterName)
                .requestId(requestId)
                .instanceIds(instanceIds)
                .ncaId(nacId)
                .traceParent(instanceServiceHelper.getTraceParent())
                .traceState(instanceServiceHelper.getTraceStateMap())
                .build();
    }
}
