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
package com.nvidia.icms.service.scheduled.instance;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceStatus.INSTANCE_TERMINATED_CLOUD_OFFLINE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;

import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import org.springframework.stereotype.Component;

import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;

@Component
@AllArgsConstructor
public class UnhealthyInstanceServiceHelper {

    private static final String TASK_NAME = "ProcessUnhealthyInstanceTask";

    private final InstanceServiceHelper instanceServiceHelper;
    private final AppAuditService auditService;

    public GenericMetric buildCloudOfflineInstanceTerminationMetric(
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull CloudProvider cloudProvider,
            @NotNull ResourceProvider resourceProvider,
            @NotNull String terminationQueue,
            @NotNull String clusterName) {
        Duration instanceLifeTime = Duration.between(
                instanceEntity.getInstanceUpdateTime(), Instant.now());

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                instanceServiceHelper.getLaunchSpecificationForTelemetry(instanceEntity.getRequestRawData());

        return new GenericMetric()
                .withEventName(Events.INSTANCE_FAILED_CLOUD_OFFLINE.toString())
                .withCustomer(instanceEntity.getCustomer())
                .withInstanceId(instanceEntity.getInstanceId())
                .withCloudProvider(cloudProvider)
                .withMetadata(Map.of(EventMetaData.TERMINATION_QUEUE.getName(), terminationQueue))
                .withResourceProvider(resourceProvider)
                .withZoneName(instanceEntity.getZone())
                .withClusterName(clusterName)
                .withRequestId(instanceEntity.getRequestId())
                .withInstanceState(instanceEntity.getInstanceStateName().getStateName())
                .withInstanceLifeTime(instanceLifeTime.toSeconds())
                .withNcaId(launchSpecification.getNcaId())
                .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withInstanceType(launchSpecification.getInstanceType())
                .withReasonForTermination(INSTANCE_TERMINATED_CLOUD_OFFLINE.toString())
                .withDeploymentId(launchSpecification.getDeploymentId())
                .withCapacityType(instanceEntity.getCapacityType())
                .withReservationId(instanceEntity.getReservationId())
                .withGpuSpecificationId(launchSpecification.getGpuSpecificationId());
    }

    public void sendAuditForUnhealthyInstance(
            @NotNull InstanceV2Entity updatedInstance,
            @NotNull InstanceV2Entity instanceBeforeUpdates) {
        Map<String, Object> auditProps = new HashMap<>();
        auditProps.put(AUDIT_ACTOR_ID_KEY, TASK_NAME);
        auditProps.put(AUDIT_SUBJECT_ID_KEY, TASK_NAME);
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.UPDATE_INSTANCE_HEALTH.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, updatedInstance.getInstanceId());
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_INSTANCE_HEALTH.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Updated instance state to unhealthy "
                + updatedInstance.getInstanceId());
        auditService.sendAuditEventForInstance(auditProps, instanceBeforeUpdates, updatedInstance);
    }
}
