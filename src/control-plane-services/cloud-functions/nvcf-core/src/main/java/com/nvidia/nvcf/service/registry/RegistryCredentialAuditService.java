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
package com.nvidia.nvcf.service.registry;

import static com.nvidia.nvcf.util.NvcfConstants.GRP_TYPE_REGISTRY_CREDENTIAL_MANAGEMENT;
import static com.nvidia.nvcf.util.NvcfConstants.NCA_ID;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_CREATE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_DELETE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_UPDATE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.NvcfConstants.REGISTRY_CREDENTIAL_ARTIFACT_TYPES;
import static com.nvidia.nvcf.util.NvcfConstants.REGISTRY_CREDENTIAL_HOSTNAME;
import static com.nvidia.nvcf.util.NvcfConstants.REGISTRY_CREDENTIAL_ID;
import static com.nvidia.nvcf.util.NvcfConstants.REGISTRY_CREDENTIAL_OBJECT_LOCATION;
import static com.nvidia.nvcf.util.NvcfConstants.REGISTRY_CREDENTIAL_PROVISIONED_BY;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_CREATED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_DELETED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_UPDATED;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_CREATE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DELETE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_UPDATE_REGISTRY_CREDENTIAL;
import static java.lang.String.format;

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
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
public class RegistryCredentialAuditService {
    private final AuditService auditService;
    private final JsonMapper jsonMapper;

    public AuditEventPayload.Builder auditEventPayloadBuilder(
            Authentication authentication,
            Map<String, String> customProperties) {
        return auditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    public void auditRegistryCredentialCreate(
            AuditEventPayload.Builder payloadBuilder,
            RegistryCredentialByAccountEntity registryCredential) {
        var registryCredentialId = registryCredential.getKey().getRegistryCredentialId();
        var ncaId = registryCredential.getKey().getNcaId();
        var artifactTypes = registryCredential.getArtifactTypes();
        var hostname = registryCredential.getRegistryHostname();
        var provisionedBy = registryCredential.getProvisionedBy();
        var summary = format(SUMMARY_CREATE_REGISTRY_CREDENTIAL, registryCredentialId, ncaId);
        payloadBuilder.operation(OPER_CREATE_REGISTRY_CREDENTIAL)
                .type(RegistryCredentialByAccountEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_REGISTRY_CREDENTIAL_MANAGEMENT)
                .objectId(registryCredentialId.toString())
                .objectLocation(REGISTRY_CREDENTIAL_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.createObjectNode()) // empty
                .jsonAfter(jsonMapper.valueToTree(registryCredential))
                .state(STATE_CREATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(REGISTRY_CREDENTIAL_ID, registryCredentialId)
                .custom(REGISTRY_CREDENTIAL_ARTIFACT_TYPES, artifactTypes.toString())
                .custom(REGISTRY_CREDENTIAL_PROVISIONED_BY, provisionedBy)
                .custom(REGISTRY_CREDENTIAL_HOSTNAME, hostname);
        auditService.audit(payloadBuilder);
    }

    public void auditRegistryCredentialUpdate(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode registryCredentialJsonBefore,
            RegistryCredentialByAccountEntity registryCredential) {
        var registryCredentialId = registryCredential.getKey().getRegistryCredentialId();
        var ncaId = registryCredential.getKey().getNcaId();
        var artifactTypes = registryCredential.getArtifactTypes();
        var summary = format(SUMMARY_UPDATE_REGISTRY_CREDENTIAL, registryCredentialId, ncaId);
        payloadBuilder.operation(OPER_UPDATE_REGISTRY_CREDENTIAL)
                .type(RegistryCredentialByAccountEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_REGISTRY_CREDENTIAL_MANAGEMENT)
                .objectId(registryCredentialId.toString())
                .objectLocation(REGISTRY_CREDENTIAL_OBJECT_LOCATION)
                .jsonBefore(registryCredentialJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(registryCredential))
                .state(STATE_UPDATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(REGISTRY_CREDENTIAL_ID, registryCredentialId)
                .custom(REGISTRY_CREDENTIAL_ARTIFACT_TYPES, artifactTypes.toString());
        auditService.audit(payloadBuilder);
    }

    public void auditRegistryCredentialDelete(
            AuditEventPayload.Builder payloadBuilder,
            RegistryCredentialByAccountEntity registryCredential) {
        var registryCredentialId = registryCredential.getKey().getRegistryCredentialId();
        var ncaId = registryCredential.getKey().getNcaId();
        var summary = format(SUMMARY_DELETE_REGISTRY_CREDENTIAL, registryCredentialId, ncaId);
        payloadBuilder.operation(OPER_DELETE_REGISTRY_CREDENTIAL)
                .type(RegistryCredentialByAccountEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_REGISTRY_CREDENTIAL_MANAGEMENT)
                .objectId(registryCredentialId.toString())
                .objectLocation(REGISTRY_CREDENTIAL_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(registryCredential))
                .jsonAfter(jsonMapper.createObjectNode()) // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(REGISTRY_CREDENTIAL_ID, registryCredentialId);
        auditService.audit(payloadBuilder);
    }
}
