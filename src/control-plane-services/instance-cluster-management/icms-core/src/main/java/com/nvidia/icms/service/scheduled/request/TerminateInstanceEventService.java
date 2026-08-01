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
package com.nvidia.icms.service.scheduled.request;

import static com.nvidia.icms.scheduled.TerminateExpiredInstanceEventController.TERMINATE_EXPIRED_INSTANCES_JOB_NAME;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_REQUEST_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.ExpiredInstanceTerminateService;
import com.nvidia.icms.service.TerminateInstanceService;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class TerminateInstanceEventService {

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final InstanceV2Repository instanceV2Repository;

    private final TerminateInstanceService terminateInstanceService;

    private final ExpiredInstanceTerminateService expiredInstanceTerminateService;

    public void execute() {
        try {
            if (!icmsConfigurationProperties.isTerminateExpiredInstancesEnabled()) {
                log.debug("Terminate expired instances task is not enabled");
                return;
            }
            log.info("Starting expired instance termination task");
            Instant instanceValidityTime = Instant.now().minus(
                    icmsConfigurationProperties.getInstanceLifetimeValidityInDays(),
                    ChronoUnit.DAYS);
            List<InstanceRequestV2Entity> expiredFilteredInstanceRequestEntities =
                    getExpiredInstanceRequestEntities(instanceValidityTime);
            Map<String, List<InstanceV2Entity>> requestIdToInstanceEntitiesMap =
                    getRequestIdToInstanceEntityMap(expiredFilteredInstanceRequestEntities);
            for (InstanceRequestV2Entity expiredRequestEntity : expiredFilteredInstanceRequestEntities) {
                boolean closeInstanceRequest =
                        terminateExpiredInstancesOfInstanceRequest(instanceValidityTime,
                                                               requestIdToInstanceEntitiesMap.get(
                                                                       expiredRequestEntity.getRequestId()));

                // Close the request if all instances are deleted
                // or if there are no running instances for this requestId.
                if (closeInstanceRequest) {
                    closeExpiredInstanceRequest(expiredRequestEntity);
                }
            }
        } catch (Exception e) {
            log.error("Exception occurred while terminating expired instances task {}",
                      e.getMessage(), e);
        }
    }

    private List<InstanceV2Entity> getRunningInstancesForCustomers(
            List<String> uniqueCustomers) {
        List<InstanceV2Entity> allRunningInstances = new ArrayList<>();
        List<Integer> expectedInstanceState = List.of(
                SpotInstanceInternalState.getStateCode(SpotInstanceInternalState.RUNNING)
        );
        for (String customer : uniqueCustomers) {
            List<InstanceV2Entity> instanceEntities =
                    instanceV2Repository.findAllByCustomer(customer)
                            .stream()
                            .filter(instance -> expectedInstanceState
                                    .contains(instance.getInstanceStateCode()))
                            .toList();
            allRunningInstances.addAll(instanceEntities);
        }
        return allRunningInstances;
    }

    private List<String> getUniqueListOfCustomers(List<InstanceRequestV2Entity> instanceRequestEntities) {
        List<String> uniqueCustomers = new ArrayList<>();
        for (InstanceRequestV2Entity instanceRequestEntity : instanceRequestEntities) {
            if (!uniqueCustomers.contains(instanceRequestEntity.getCustomer())) {
                uniqueCustomers.add(instanceRequestEntity.getCustomer());
            }
        }
        return uniqueCustomers;
    }

    private Map<String, List<InstanceV2Entity>> getRequestIdToInstanceEntityMap(
            List<InstanceRequestV2Entity> expiredInstanceRequestEntities) {
        List<String> uniqueCustomers = getUniqueListOfCustomers(expiredInstanceRequestEntities);
        List<InstanceV2Entity> allRunningInstances =
                getRunningInstancesForCustomers(uniqueCustomers);
        Map<String, List<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        for (InstanceV2Entity runningInstance : allRunningInstances) {
            // If requestId is already present in map then add this instance to the list of instances
            if (requestIdToInstanceEntitiesMap.containsKey(runningInstance.getRequestId())) {
                requestIdToInstanceEntitiesMap.get(runningInstance.getRequestId())
                        .add(runningInstance);
            } else {
                List<InstanceV2Entity> instanceEntities = new ArrayList<>();
                instanceEntities.add(runningInstance);
                // If requestId is not present in Map then add the entry freshly with this instance
                requestIdToInstanceEntitiesMap.put(runningInstance.getRequestId(),
                                                     instanceEntities);
            }
        }
        return requestIdToInstanceEntitiesMap;
    }

    private boolean terminateExpiredInstancesOfInstanceRequest(
            Instant instanceValidityTime,
            List<InstanceV2Entity> runningInstances) {
        // No running instances for this request-id, so return true for closing the request.
        if (runningInstances == null) {
            return true;
        }

        boolean allInstancesDeleted = true;
        List<InstanceV2Entity> instanceEntitiesToTerminate = new ArrayList<>();
        for (InstanceV2Entity instance : runningInstances) {
            if (instance.getInstanceUpdateTime().isBefore(instanceValidityTime)) {
                instanceEntitiesToTerminate.add(instance);
            } else {
                // There are instances which went in running within the valid 90 days time period
                // These instances will be deleted once their running time is over 90 days
                allInstancesDeleted = false;
            }
        }
        if (instanceEntitiesToTerminate.isEmpty()) {
            // No running instances for an expired request, so return true for closing it.
            return true;
        }
        // Delete expired running instances for this request-id.
        expiredInstanceTerminateService
                .terminateExpiredInstances(instanceEntitiesToTerminate);
        return allInstancesDeleted;
    }

    private List<InstanceRequestV2Entity> getExpiredInstanceRequestEntities(Instant instanceValidityTime) {
        Set<SpotInstanceRequestState> validRequestStates =
                Set.of(SpotInstanceRequestState.ACTIVE,
                       SpotInstanceRequestState.OPEN);
        // Find expired requests that need to be evaluated.
        // Filter requests that are open or active and were created before 90 days.
        return instanceRequestV2Repository
                .findRequestsInLastMonths(
                        icmsConfigurationProperties.getTerminateExpiredRequestFromPastMonths())
                .stream()
                .filter(requestEntity -> validRequestStates.contains(
                        requestEntity.getState()))
                .filter(requestEntity -> instanceValidityTime.isAfter(
                        TimeUtils.getInstantFromUuid(requestEntity.getCreateTimeuuid())))
                .toList();
    }

    private void closeExpiredInstanceRequest(
            InstanceRequestV2Entity expiredRequestEntity) {
        Map<String, Object> auditProps = new HashMap<>();
        populateAuditValuesForTerminateInstanceRequest(auditProps,
                                                   expiredRequestEntity.getRequestId());
        log.debug("Closing expired instance request");
        terminateInstanceService
                .updateRequestStateToClosedFromAsyncTerminateTask(expiredRequestEntity);
    }

    private void populateAuditValuesForTerminateInstanceRequest(
            Map<String, Object> auditProps,
            String requestId) {

        auditProps.put(AUDIT_ACTOR_ID_KEY, TERMINATE_EXPIRED_INSTANCES_JOB_NAME);
        auditProps.put(AUDIT_SUBJECT_ID_KEY, TERMINATE_EXPIRED_INSTANCES_JOB_NAME);
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.CLOSE_EXPIRED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CLOSED_EXPIRED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Closing expired instance request with id " + requestId);
    }
}
