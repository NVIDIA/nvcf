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

import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_AUTHENTICATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_HTTP_METHOD;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_JSON_AFTER_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_JSON_BEFORE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_REMOTE_ADDR;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_REQUEST_URI;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_USER_AGENT;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.tenantregistration.entity.TenantRegistrationEntity;
import com.nvidia.icms.util.CopyUtil;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Collections;
import java.util.Map;

import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class AppAuditService {

    private final AuditService auditService;

    private final ObjectMapper objectMapper;

    public void sendAuditEventForInstanceRequest(
            @NotNull Map<String, Object> auditProps,
            @Nullable InstanceRequestV2Entity entityBefore,
            @Nullable InstanceRequestV2Entity entityAfter) {
        try {
            JsonNode jsonBefore = objectMapper.valueToTree(entityBefore);
            JsonNode jsonAfter = objectMapper.valueToTree(entityAfter);
            auditProps.put(AUDIT_JSON_BEFORE_KEY, jsonBefore);
            auditProps.put(AUDIT_JSON_AFTER_KEY, jsonAfter);
            audit(auditProps);
        } catch (Exception e) {
            log.warn("Failed to send audit event, error - {}", e.getMessage());
        }
    }

    public void sendAuditEventForInstance(
            @NotNull Map<String, Object> auditProps,
            @NotNull InstanceV2Entity entityBefore,
            @NotNull InstanceV2Entity entityAfter) {
        try {
            InstanceV2Entity entityBeforeDeepCopy = CopyUtil.deepCopy(entityBefore);
            InstanceV2Entity entityAfterDeepCopy = CopyUtil.deepCopy(entityAfter);

            entityBeforeDeepCopy.setErrorLog(null);
            entityAfterDeepCopy.setErrorLog(null);
            JsonNode jsonBefore = objectMapper.valueToTree(entityBeforeDeepCopy);
            JsonNode jsonAfter = objectMapper.valueToTree(entityAfterDeepCopy);
            auditProps.put(AUDIT_JSON_BEFORE_KEY, jsonBefore);
            auditProps.put(AUDIT_JSON_AFTER_KEY, jsonAfter);
            audit(auditProps);
        } catch (Exception e) {
            log.warn("Failed to send audit event, error - {}", e.getMessage());
        }
    }


    public void sendAuditEventForClusterEntity(
            @NotNull Map<String, Object> auditProps,
            ClusterEntity entityBefore,
            ClusterEntity entityAfter) {
        try {
            JsonNode jsonBefore = objectMapper.valueToTree(entityBefore);
            JsonNode jsonAfter = objectMapper.valueToTree(entityAfter);
            auditProps.put(AUDIT_JSON_BEFORE_KEY, jsonBefore);
            auditProps.put(AUDIT_JSON_AFTER_KEY, jsonAfter);
            audit(auditProps);
        } catch (Exception e) {
            log.warn("Failed to send audit event, error - {}", e.getMessage());
        }
    }

    public void sendAuditEventForTenantRegistrationEntity(
            @NotNull Map<String, Object> auditProps,
            @Nullable TenantRegistrationEntity entityBefore,
            @Nullable TenantRegistrationEntity entityAfter) {
        try {
            JsonNode jsonBefore = objectMapper.valueToTree(entityBefore);
            JsonNode jsonAfter = objectMapper.valueToTree(entityAfter);
            auditProps.put(AUDIT_JSON_BEFORE_KEY, jsonBefore);
            auditProps.put(AUDIT_JSON_AFTER_KEY, jsonAfter);
            audit(auditProps);
        } catch (Exception e) {
            log.warn("Failed to send audit event for tenant registration, error - {}", e.getMessage());
        }
    }

    private void audit(@NotNull Map<String, Object> props) {
        var builder = baseBuilder(props)
                .operation((String) props.get(AUDIT_OPERATION_KEY))
                .type((String) props.get(AUDIT_TYPE_KEY))
                .actorLocation((String) props.get(AUDIT_ACTOR_LOCATION_KEY))
                .subjectLocation((String) props.get(AUDIT_SUBJECT_LOCATION_KEY))
                .objectId((String) props.get(AUDIT_OBJECT_ID_KEY))
                .objectLocation((String) props.get(AUDIT_OBJECT_LOCATION_KEY))
                .state((String) props.get(AUDIT_STATE_KEY))
                .summary((String) props.get(AUDIT_SUMMARY_KEY))
                .jsonBefore((JsonNode) props.get(AUDIT_JSON_BEFORE_KEY))
                .jsonAfter((JsonNode) props.get(AUDIT_JSON_AFTER_KEY))
                .custom(AUDIT_REQUEST_URI, props.get(AUDIT_REQUEST_URI))
                .custom(AUDIT_REMOTE_ADDR, props.get(AUDIT_REMOTE_ADDR))
                .custom(AUDIT_HTTP_METHOD, props.get(AUDIT_HTTP_METHOD))
                .custom(AUDIT_USER_AGENT, props.get(AUDIT_USER_AGENT));
        auditService.audit(builder);
    }

    /**
     * Builds the payload with actor/subject identity resolved. Request-driven audits carry an
     * {@link Authentication}, so the core derives the actor/subject ids the same way for every
     * service. Async/scheduled audits have no authentication and instead supply explicit
     * actor/subject ids (e.g. the job name).
     */
    private AuditEventPayload.Builder baseBuilder(@NotNull Map<String, Object> props) {
        Authentication authentication = (Authentication) props.get(AUDIT_AUTHENTICATION_KEY);
        if (authentication != null) {
            return auditService.auditEventPayloadBuilder(authentication, Collections.emptyMap());
        }
        return auditService.auditEventPayloadBuilder()
                .actorId((String) props.get(AUDIT_ACTOR_ID_KEY))
                .subjectId((String) props.get(AUDIT_SUBJECT_ID_KEY));
    }

}
