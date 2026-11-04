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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.util.NvcfConstants.DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.NvcfConstants.DEPLOYMENT_OBJECT_LOCATION;
import static com.nvidia.nvcf.util.NvcfConstants.FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.FUNCTION_OBJECT_LOCATION;
import static com.nvidia.nvcf.util.NvcfConstants.FUNCTION_STATUS;
import static com.nvidia.nvcf.util.NvcfConstants.FUNCTION_VERSION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.GRP_TYPE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.GRP_TYPE_FUNCTION_MANAGEMENT;
import static com.nvidia.nvcf.util.NvcfConstants.NCA_ID;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_CREATE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_CREATE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_DELETE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_UPDATE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_CREATED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_DELETED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_UPDATED;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_CREATE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_CREATE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DELETE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_GRACEFUL_DELETE_FUNCTION_DEPLOYMENT;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_UPDATE_FUNCTION_DEPLOYMENT;
import static java.lang.String.format;

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionAuditService {
    private final AuditService auditService;
    private final JsonMapper jsonMapper;

    public AuditEventPayload.Builder auditEventPayloadBuilder() {
        return auditService.auditEventPayloadBuilder();
    }

    public AuditEventPayload.Builder auditEventPayloadBuilder(
            Authentication authentication,
            Map<String, String> customProperties) {
        return auditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    public void auditFunctionCreate(
            AuditEventPayload.Builder payloadBuilder,
            FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_CREATE_FUNCTION, functionId, versionId);
        payloadBuilder.operation(OPER_CREATE_FUNCTION)
                .type(FunctionEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_MANAGEMENT)
                .objectId(versionId.toString())
                .objectLocation(FUNCTION_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.createObjectNode()) // empty
                .jsonAfter(jsonMapper.valueToTree(functionEntity))
                .state(STATE_CREATED)
                .custom(FUNCTION_STATUS, status.toString())
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditFunctionUpdate(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode functionJsonBefore,
            FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_UPDATE_FUNCTION, functionId, versionId);
        payloadBuilder.operation(OPER_UPDATE_FUNCTION)
                .type(FunctionEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_MANAGEMENT)
                .objectId(versionId.toString())
                .objectLocation(FUNCTION_OBJECT_LOCATION)
                .jsonBefore(functionJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(functionEntity))
                .state(STATE_UPDATED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditFunctionUpdate(
            String summary,
            String state,
            JsonNode functionJsonBefore,
            FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var payloadBuilder = auditEventPayloadBuilder();
        payloadBuilder.operation(OPER_UPDATE_FUNCTION)
                .type(FunctionEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_MANAGEMENT)
                .objectId(versionId.toString())
                .objectLocation(FUNCTION_OBJECT_LOCATION)
                .jsonBefore(functionJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(functionEntity))
                .state(state)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditFunctionDelete(
            AuditEventPayload.Builder payloadBuilder,
            FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var summary = format(SUMMARY_DELETE_FUNCTION, functionId, versionId);
        payloadBuilder.operation(OPER_DELETE_FUNCTION)
                .type(FunctionEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_MANAGEMENT)
                .objectId(versionId.toString())
                .objectLocation(FUNCTION_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(functionEntity))
                .jsonAfter(jsonMapper.createObjectNode()) // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId);
        auditService.audit(payloadBuilder);
    }

    public void auditCreateFunctionDeployment(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode functionJsonBefore,
            FunctionEntity functionEntity,
            FunctionDeploymentEntity deploymentEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_CREATE_FUNCTION_DEPLOYMENT, functionId, versionId);

        // Create audit log for the function deployment.
        payloadBuilder.operation(OPER_CREATE_FUNCTION_DEPLOYMENT)
                .type(FunctionDeploymentEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_DEPLOYMENT)
                .objectId(versionId.toString())
                .objectLocation(DEPLOYMENT_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.createObjectNode())  // empty
                .jsonAfter(jsonMapper.valueToTree(deploymentEntity))
                .state(STATE_CREATED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status)
                .custom(DEPLOYMENT_ID, deploymentEntity.getDeploymentId());
        auditService.audit(payloadBuilder);

        // Create audit log for the update to the function itself.
        auditFunctionUpdate(payloadBuilder, functionJsonBefore, functionEntity);
    }

    public void auditUpdateFunctionDeployment(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode functionJsonBefore,
            JsonNode deploymentJsonBefore,
            FunctionEntity functionEntity,
            FunctionDeploymentEntity deploymentEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_UPDATE_FUNCTION_DEPLOYMENT, functionId, versionId);

        // Create audit log for the function deployment.
        payloadBuilder.operation(OPER_UPDATE_FUNCTION_DEPLOYMENT)
                .type(FunctionDeploymentEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_DEPLOYMENT)
                .objectId(versionId.toString())
                .objectLocation(DEPLOYMENT_OBJECT_LOCATION)
                .jsonBefore(deploymentJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(deploymentEntity))
                .state(STATE_UPDATED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status)
                .custom(DEPLOYMENT_ID, deploymentEntity.getDeploymentId());
        auditService.audit(payloadBuilder);

        // Create audit log for the update to the function itself.
        auditFunctionUpdate(payloadBuilder, functionJsonBefore, functionEntity);
    }

    public void auditDeleteFunctionDeployment(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode functionJsonBefore,
            FunctionEntity functionEntity,
            FunctionDeploymentEntity deploymentEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_DELETE_FUNCTION_DEPLOYMENT, functionId, versionId);

        // Create audit log for the function deployment.
        payloadBuilder.operation(OPER_DELETE_FUNCTION_DEPLOYMENT)
                .type(FunctionDeploymentEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_DEPLOYMENT)
                .objectId(versionId.toString())
                .objectLocation(DEPLOYMENT_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(deploymentEntity))
                .jsonAfter(jsonMapper.createObjectNode())  // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status)
                .custom(DEPLOYMENT_ID, deploymentEntity.getDeploymentId());
        auditService.audit(payloadBuilder);

        // Create audit log for the update to the function itself.
        auditFunctionUpdate(payloadBuilder, functionJsonBefore, functionEntity);
    }

    // Called when the deployment is deleted gracefully once the queue is drained.
    public void auditDeleteFunctionDeployment(
            FunctionEntity functionEntity,
            FunctionDeploymentEntity deploymentEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var status = functionEntity.getFunctionStatus();
        var summary = format(SUMMARY_GRACEFUL_DELETE_FUNCTION_DEPLOYMENT, functionId, versionId);
        var payloadBuilder = auditEventPayloadBuilder();

        // Create audit log for the function deployment.
        payloadBuilder.operation(OPER_DELETE_FUNCTION_DEPLOYMENT)
                .type(FunctionDeploymentEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_FUNCTION_DEPLOYMENT)
                .objectId(versionId.toString())
                .objectLocation(DEPLOYMENT_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(deploymentEntity))
                .jsonAfter(jsonMapper.createObjectNode())  // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, functionEntity.getNcaId())
                .custom(FUNCTION_ID, functionId)
                .custom(FUNCTION_VERSION_ID, versionId)
                .custom(FUNCTION_STATUS, status)
                .custom(DEPLOYMENT_ID, deploymentEntity.getDeploymentId());
        auditService.audit(payloadBuilder);

        // Function has already been updated with INACTIVE status and the audit log created.
    }
}
