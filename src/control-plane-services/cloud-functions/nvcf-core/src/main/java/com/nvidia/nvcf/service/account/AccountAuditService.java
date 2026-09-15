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
package com.nvidia.nvcf.service.account;

import static com.nvidia.nvcf.util.NvcfConstants.ACCOUNT_NAME;
import static com.nvidia.nvcf.util.NvcfConstants.ACCOUNT_OBJECT_LOCATION;
import static com.nvidia.nvcf.util.NvcfConstants.GRP_TYPE_ACCOUNT_MANAGEMENT;
import static com.nvidia.nvcf.util.NvcfConstants.NCA_ID;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_CREATE_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_DELETE_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.OPER_UPDATE_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_CREATED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_DELETED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_UPDATED;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_CREATE_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DELETE_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_UPDATE_ACCOUNT;
import static java.lang.String.format;

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
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
public class AccountAuditService {
    private final AuditService auditService;
    private final JsonMapper jsonMapper;

    public AuditEventPayload.Builder auditEventPayloadBuilder(
            Authentication authentication,
            Map<String, String> customProperties) {
        return auditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    public void auditAccountCreate(
            AuditEventPayload.Builder payloadBuilder,
            AccountEntity accountEntity) {
        var ncaId = accountEntity.getNcaId();
        var name = accountEntity.getName();
        var summary = format(SUMMARY_CREATE_ACCOUNT, ncaId, name);
        payloadBuilder.operation(OPER_CREATE_ACCOUNT)
                .type(AccountEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_ACCOUNT_MANAGEMENT)
                .objectId(ncaId)
                .objectLocation(ACCOUNT_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.createObjectNode()) // empty
                .jsonAfter(jsonMapper.valueToTree(accountEntity))
                .state(STATE_CREATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(ACCOUNT_NAME, name);
        auditService.audit(payloadBuilder);
    }

    public void auditAccountUpdate(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode accountJsonBefore,
            AccountEntity accountEntity) {
        var ncaId = accountEntity.getNcaId();
        var name = accountEntity.getName();
        var summary = format(SUMMARY_UPDATE_ACCOUNT, ncaId, name);
        payloadBuilder.operation(OPER_UPDATE_ACCOUNT)
                .type(AccountEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_ACCOUNT_MANAGEMENT)
                .objectId(ncaId)
                .objectLocation(ACCOUNT_OBJECT_LOCATION)
                .jsonBefore(accountJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(accountEntity))
                .state(STATE_UPDATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(ACCOUNT_NAME, name);
        auditService.audit(payloadBuilder);
    }

    public void auditAccountDelete(
            AuditEventPayload.Builder payloadBuilder,
            AccountEntity accountEntity) {
        var ncaId = accountEntity.getNcaId();
        var name = accountEntity.getName();
        var summary = format(SUMMARY_DELETE_ACCOUNT, ncaId, name);
        payloadBuilder.operation(OPER_DELETE_ACCOUNT)
                .type(FunctionEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_ACCOUNT_MANAGEMENT)
                .objectId(ncaId)
                .objectLocation(ACCOUNT_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(accountEntity))
                .jsonAfter(jsonMapper.createObjectNode()) // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(ACCOUNT_NAME, name);
        auditService.audit(payloadBuilder);
    }
}
