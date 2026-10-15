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
package com.nvidia.icms.util.audit;

import static com.amazonaws.http.AmazonHttpClient.HEADER_USER_AGENT;
import static com.nvidia.icms.scheduled.ByocClusterHealthMonitorTaskController.CLUSTER_HEALTH_MONITOR_TASK_NAME;
import static com.nvidia.icms.scheduled.DatabaseCleanupTaskController.DATABASE_CLEANUP_JOB_NAME;
import static com.nvidia.icms.service.scheduled.GpusV5PopulationTask.GPUS_V5_POPULATION_TASK_NAME;

import com.nvidia.icms.errors.IcmsInternalServerException;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import jakarta.annotation.Nullable;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.constraints.NotNull;
import java.util.HashMap;
import java.util.Map;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;

@UtilityClass
@Slf4j
public class AuditUtils {

    public static final String AUDIT_ACTOR_ID_KEY = "actorId";
    public static final String AUDIT_SUBJECT_ID_KEY = "subjectId";
    public static final String AUDIT_ACTOR_LOCATION_KEY = "actorLocation";
    public static final String AUDIT_SUBJECT_LOCATION_KEY = "subjectLocation";
    public static final String AUDIT_OPERATION_KEY = "operation";
    public static final String AUDIT_TYPE_KEY = "type";
    public static final String AUDIT_INSTANCE_REQUEST_TYPE = "INSTANCE_REQUEST";
    public static final String AUDIT_INSTANCE_TYPE = "INSTANCE";

    public static final String AUDIT_CLUSTER_TYPE = "CLUSTER";
    public static final String AUDIT_OBJECT_ID_KEY = "objectId";
    public static final String AUDIT_OBJECT_LOCATION_KEY = "objectLocation";

    public static final String AUDIT_HTTP_METHOD = "httpMethod";
    public static final String AUDIT_REQUEST_URI = "requestUri";
    public static final String AUDIT_REMOTE_ADDR = "remoteAddr";
    public static final String AUDIT_USER_AGENT = "userAgent";

    // Carries the request's Authentication so the actor/subject id can be derived by
    // AuditService.auditEventPayloadBuilder(authentication, ...) instead of being computed here.
    public static final String AUDIT_AUTHENTICATION_KEY = "authentication";

    // Setting value for object location as the string "DATABASE-<table_name_for_the_object>" so
    // that we are aware where the object we are generating the audit logs for resides.
    public static final String AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST =
            "DATABASE-requests_by_customer_and_timestamp";
    public static final String AUDIT_OBJECT_LOCATION_INSTANCE =
            "DATABASE-instances_by_customer_and_timestamp";
    public static final String AUDIT_OBJECT_LOCATION_CREDENTIAL =
            "DATABASE-credentials_by_customer_and_timestamp";

    public static final String AUDIT_OBJECT_LOCATION_CLUSTER =
            "DATABASE-cluster_by_id";
    public static final String AUDIT_TENANT_REGISTRATION_TYPE = "TENANT_REGISTRATION";
    public static final String AUDIT_OBJECT_LOCATION_TENANT_REGISTRATION =
            "DATABASE-tenant_registration_by_registration_id";
    public static final String AUDIT_STATE_KEY = "state";
    public static final String AUDIT_SUMMARY_KEY = "summary";
    public static final String AUDIT_JSON_BEFORE_KEY = "jsonBefore";
    public static final String AUDIT_JSON_AFTER_KEY = "jsonAfter";

    public static final String AUDIT_ACTOR_LOCATION_ASYNC_OPERATION = "LOCAL_ASYNC_OPERATION";
    public static final String AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION = "LOCAL_ASYNC_OPERATION";

    private static final ObjectMapper objectMapper =
            JsonMapper.builder().disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                    .build();

    public static Map<String, Object> getAuditPropertiesFromRequest(@NotNull HttpServletRequest request) {
        Map<String, Object> auditProps = new HashMap<>();
        try {
            Authentication authentication =
                    SecurityContextHolder.getContext().getAuthentication();

            auditProps.put(AUDIT_ACTOR_LOCATION_KEY, request.getRemoteAddr());
            auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, request.getRemoteAddr());
            auditProps.put(AUDIT_HTTP_METHOD, request.getMethod());
            auditProps.put(AUDIT_REQUEST_URI, request.getRequestURI());
            auditProps.put(AUDIT_REMOTE_ADDR, request.getRemoteAddr());
            auditProps.put(AUDIT_USER_AGENT, request.getHeader(HEADER_USER_AGENT));
            auditProps.put(AUDIT_AUTHENTICATION_KEY, authentication);
        } catch (Exception e) {
            log.warn("Failed to generate audit properties map from request, error - {}",
                     e.getMessage());
        }
        return auditProps;
    }

    public static InstanceRequestV2Entity deepCopyInstanceRequestEntity(InstanceRequestV2Entity input) {
        try {
            return objectMapper
                    .readValue(objectMapper.writeValueAsString(input), InstanceRequestV2Entity.class);
        } catch (JacksonException e) {
            String errorMsg = String.format("Failed to create deep copy of request object for audit logs, error: %s", e.getMessage());
            log.error(errorMsg);
            throw new IcmsInternalServerException(errorMsg, e);
        }
    }


    public static InstanceV2Entity deepCopyInstanceEntity(InstanceV2Entity input) {
        try {
            return objectMapper
                    .readValue(objectMapper.writeValueAsString(input), InstanceV2Entity.class);
        } catch (JacksonException e) {
            String errorMsg = String.format("Failed to create deep copy of instance object for audit logs, error: %s", e.getMessage());
            log.error(errorMsg);
            throw new IcmsInternalServerException(errorMsg, e);
        }
    }

    public static void populateAuditValuesForShuttingInstance(
            @NotNull Map<String, Object> auditProps,
            @Nullable String instanceId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.SHUTDOWN_INSTANCE.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, instanceId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.SHUTDOWN_INSTANCE.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Shutting down instance with id " + instanceId);
    }

    public static void populateAuditValuesForTerminateInstanceRequest(
            @NotNull Map<String, Object> auditProps,
            @Nullable String requestId) {

        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.TERMINATE_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.TERMINATED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Closed instance request with id " + requestId);
    }

    public static void populateAuditValuesForCreateInstanceRequest(
            @NotNull Map<String, Object> auditProps,
            String requestId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.CREATE_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CREATED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Created new instance request with id " + requestId);
    }

    public static void populateAuditValuesForCleanupInstanceRequest(
            @NotNull Map<String, Object> auditProps,
            @Nullable String requestId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.DB_CLEANUP_JOB.toString());
        auditProps.put(AUDIT_ACTOR_ID_KEY, DATABASE_CLEANUP_JOB_NAME);
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        //auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.TERMINATED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Terminated instance request by DB cleanup job " + requestId);
    }

    public static void populateAuditValuesForCreateInstance(
            @NotNull Map<String, Object> auditProps,
            @Nullable String instanceId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.CREATE_INSTANCE.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, instanceId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CREATED_INSTANCE.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Created new instance with id " + instanceId);
    }


    public static void populateAuditValuesForTerminateInstance(
            @NotNull Map<String, Object> auditProps,
            @Nullable String instanceId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.TERMINATE_INSTANCE.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, instanceId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.TERMINATED_INSTANCE.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Terminated instance with id " + instanceId);
    }

    public static void populateAuditValuesForRegisteringNewCluster(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.REGISTER_NEW_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.REGISTERED_NEW_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Registered new cluster with id " + clusterId);
    }

    public static void populateAuditValuesForReconfigurationOfCluster(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.RECONFIGURED_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Reconfigured cluster with id " + clusterId);
    }

    public static void populateAuditValuesForDeletingCluster(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.DELETE_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.DELETED_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Deleted cluster with id " + clusterId);
    }

    public static void populateAuditValuesForCreateTenantRegistration(
            @NotNull Map<String, Object> auditProps,
            @Nullable String registrationId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.CREATE_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_TENANT_REGISTRATION_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, registrationId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_TENANT_REGISTRATION);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CREATED_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Created tenant registration with id " + registrationId);
    }

    public static void populateAuditValuesForUpdateTenantRegistration(
            @NotNull Map<String, Object> auditProps,
            @Nullable String registrationId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.UPDATE_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_TENANT_REGISTRATION_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, registrationId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_TENANT_REGISTRATION);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Updated tenant registration with id " + registrationId);
    }

    public static void populateAuditValuesForDeleteTenantRegistration(
            @NotNull Map<String, Object> auditProps,
            @Nullable String registrationId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.DELETE_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_TENANT_REGISTRATION_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, registrationId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_TENANT_REGISTRATION);
        auditProps.put(AUDIT_STATE_KEY, AuditState.DELETED_TENANT_REGISTRATION.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Deleted tenant registration with id " + registrationId);
    }

    public static void populateAuditValuesForUpdateCluster(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.UPDATE_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Updated cluster with id " + clusterId);
    }

    public static void populateAuditValuesForGpuV4PopulationTask(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_ACTOR_ID_KEY, GPUS_V5_POPULATION_TASK_NAME );
        auditProps.put(AUDIT_SUBJECT_ID_KEY, GPUS_V5_POPULATION_TASK_NAME );
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);

        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.UPDATE_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Populated GpuV5 for the cluster with id " + clusterId);

    }

    public static void populateAuditValuesForClusterHealthMonitorTask(
            @NotNull Map<String, Object> auditProps,
            @Nullable String clusterId) {

        auditProps.put(AUDIT_ACTOR_ID_KEY, CLUSTER_HEALTH_MONITOR_TASK_NAME);
        auditProps.put(AUDIT_SUBJECT_ID_KEY, CLUSTER_HEALTH_MONITOR_TASK_NAME);
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);

        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.UPDATE_CLUSTER.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_CLUSTER_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, clusterId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_CLUSTER);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_CLUSTER.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Abandoned cluster with id " + clusterId);
    }

    public static void populateAuditValuesForRunningInstance(
            @NotNull Map<String, Object> auditProps,
            @Nullable String instanceId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.RUNNING_INSTANCE.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, instanceId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.RAN_INSTANCE.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Running instance with id " + instanceId);
    }

    public static void populateAuditValuesForStartingInstance(
            @NotNull Map<String, Object> auditProps,
            @Nullable String instanceId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.STARTING_INSTANCE.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, instanceId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE);
        auditProps.put(AUDIT_STATE_KEY, AuditState.STARTED_INSTANCE.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Starting instance with id " + instanceId);
    }
}
