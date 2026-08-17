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

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.registry.entity.ProvisionedBy;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.rest.registry.dto.UpdateRegistryCredentialRequest;
import java.time.Instant;
import java.util.Base64;
import java.util.Collections;
import java.util.EnumSet;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.NotImplementedException;
import org.apache.commons.lang3.StringUtils;
import org.apache.logging.log4j.util.Strings;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

@Slf4j
@Service
@RefreshScope
public class RegistryCredentialService {

    private static final String MESG_REGISTRY_CREDENTIAL_OPER =
            "Account '{}', Registry credential '{}': {} registry credential for hostname '{}'";
    private static final String MESG_REGISTRY_CREDENTIAL_ALREADY_EXISTS =
            "Account '%s': Registry credential '%s' for hostname '%s' already exists " +
                    "in the account - Registry credentials must have unique hostname " +
                    "and credential name within an account";
    private static final String MESG_REGISTRY_CREDENTIAL_UPDATE_CONFLICTED =
            "Account '%s': Registry credential '%s' with name '%s' for hostname '%s' exists " +
                    "- Registry credentials must have unique hostname and credential name " +
                    "within an account ";
    private static final String MESG_REGISTRY_NOT_RECOGNIZED =
            "Invalid request: %s registry with hostname %s is not yet recognized";
    private static final String MESG_INVALID_SECRET_FORMAT =
            "Invalid request: Registry credential secret must be base64 encoded " +
                    "username:password format";
    private static final String MESG_MISSING_OAUTHTOKEN_USERNAME =
            "Invalid request: Missing $oauthtoken username in NGC registry credential";
    private static final String MESG_MISSING_BOTH_SECRET_AND_ARTIFACT_TYPES =
            "Invalid request: Either secret or artifact types must be specified";
    private static final String MESG_NOT_RECOGNIZED_FOR_ARTIFACT_TYPE =
            "Invalid request: '%s' with hostname '%s' not recognized for artifact type '%s'";
    private static final String MESG_ARTIFACT_TYPE_ALREADY_SPECIFIED =
            "Registry credential '%s': Artifact type '%s' is already specified";
    private static final String MESG_HOSTNAME_WITH_MULTIPLE_REGISTRY_NAMES =
            "Registry hostname '%s' has different names in recognized registries map";
    private static final String MESG_REGISTR_CREDENTIAL_LIMIT_EXCEEDED =
            "Account '%s': Cannot add registry credential - maximum allowed registry "
                    + "credentials (%d) exceeded. Current count: %d";
    private static final String MESG_MISSING_ARTIFACT_ENUM_IMPLEMENTATION =
            "Missing artifact type enum %s implementation";

    public static final Set<ArtifactTypeEnum> ARTIFACT_TYPE_ENUMS =
            Collections.unmodifiableSet(EnumSet.allOf(ArtifactTypeEnum.class));
    public static final String MESG_SYSTEM_PROVISIONED_REGISTRY_CRED =
            "Invalid request: Cannot delete system provisioned registry credential";

    private final RegistryCredentialAuditService registryCredentialAuditService;
    private final RegistryCredentialLookupService registryCredentialLookupService;
    private final RegistryMapperService registryMapperService;
    private final RegistryLookupService registryLookupService;
    private final RegistryCredentialFunctionService registryCredentialFunctionService;
    private final RegistryCredentialEssService registryCredentialEssService;
    private final RegistryCredentialValidationService registryCredentialValidationService;
    private final RegistryFunctionMapperService registryFunctionMapperService;
    private final JsonMapper jsonMapper;

    public RegistryCredentialService(
            RegistryCredentialAuditService registryCredentialAuditService,
            RegistryCredentialLookupService registryCredentialLookupService,
            RegistryLookupService registryLookupService,
            RegistryMapperService registryMapperService,
            RegistryCredentialFunctionService registryCredentialFunctionService,
            RegistryCredentialEssService registryCredentialEssService,
            RegistryCredentialValidationService registryCredentialValidationService,
            RegistryFunctionMapperService registryFunctionMapperService,
            JsonMapper jsonMapper) {
        this.registryCredentialAuditService = registryCredentialAuditService;
        this.registryLookupService = registryLookupService;
        this.registryMapperService = registryMapperService;
        this.registryCredentialLookupService = registryCredentialLookupService;
        this.registryCredentialFunctionService = registryCredentialFunctionService;
        this.registryCredentialEssService = registryCredentialEssService;
        this.registryCredentialValidationService = registryCredentialValidationService;
        this.registryFunctionMapperService = registryFunctionMapperService;
        this.jsonMapper = jsonMapper;
    }

    public void addRegistryCredentials(
            String ncaId,
            AccountEntity accountEntity,
            List<RegistryCredentialDto> registryCredentials,
            AuditEventPayload.Builder auditEventPayloadBuilder) {
        try {
            registryCredentials.stream()
                    .map(RegistryFunctionMapperService::toAddRegistryCredentialRequest)
                    .forEach(addRegistryRequest ->
                                     addRegistryCredential(ncaId,
                                                           accountEntity,
                                                           addRegistryRequest,
                                                           ProvisionedByEnum.SYSTEM,
                                                           auditEventPayloadBuilder));
        } catch (Exception ex) {
            // Delete registry credentials that were successfully created so far as we are
            // not going to create the account.
            deleteAllRegistryCredentials(ncaId, auditEventPayloadBuilder);

            log.error(ex.getMessage());
            throw (ex instanceof BootResponseException bootResponseException) ?
                    bootResponseException : new BadRequestException(ex.getMessage(), ex);
        }
    }

    public RegistryCredentialByAccountEntity addRegistryCredential(
            String ncaId,
            AccountEntity accountEntity,
            AddRegistryCredentialRequest request,
            ProvisionedByEnum provisionedByEnum,
            AuditEventPayload.Builder payloadBuilder) {
        validateAddRegistryCredentialRequest(ncaId, accountEntity, request);

        var registryCredentialId = UUID.randomUUID();
        var hostname = request.registryHostname();
        var artifactTypeEnums = request.artifactTypes();
        var registryName = validateRegistryCredentialIsForRecognizedRegistry(artifactTypeEnums,
                                                                             hostname);
        var entity = RegistryFunctionMapperService
                .toRegistryCredentialByAccountEntity(ncaId, registryCredentialId, registryName,
                                                     provisionedByEnum, request);
        registryCredentialLookupService.saveRegistryCredential(ncaId, entity);
        registryCredentialEssService.saveRegistryCredentialSecret(ncaId, registryCredentialId,
                                                                  request.secret());
        registryCredentialAuditService.auditRegistryCredentialCreate(payloadBuilder, entity);
        log.info(MESG_REGISTRY_CREDENTIAL_OPER, ncaId, registryCredentialId, "Added", hostname);
        return entity;
    }

    public RegistryCredentialDetailsDto updateRegistryCredential(
            String ncaId,
            UUID registryCredentialId,
            UpdateRegistryCredentialRequest request,
            AuditEventPayload.Builder payloadBuilder) {
        var entity = registryCredentialLookupService
                .lookupRegistryCredentialByAccountAndIdOrThrow(ncaId, registryCredentialId);
        var jsonBefore = jsonMapper.valueToTree(entity);
        validateUpdateRegistryCredentialRequest(request, entity);

        if (request.secret() != null) {
            entity.setRegistryCredentialName(request.secret().name());
            registryCredentialEssService.saveRegistryCredentialSecret(ncaId, registryCredentialId,
                                                                      request.secret());
        }

        if (!CollectionUtils.isEmpty(request.artifactTypeEnums())) {
            var artifactTypeEnums = request.artifactTypeEnums();
            var newArtifactTypes = RegistryFunctionMapperService.toArtifactTypes(artifactTypeEnums);
            entity.getArtifactTypes().addAll(newArtifactTypes);
        }

        entity.setLastUpdatedAt(Instant.now());
        registryCredentialLookupService.saveRegistryCredential(ncaId, entity);

        var hostname = entity.getRegistryHostname();
        var dto = registryFunctionMapperService.toRegistryCredentialDetailsDto(entity);
        registryCredentialAuditService
                .auditRegistryCredentialUpdate(payloadBuilder, jsonBefore, entity);
        log.info(MESG_REGISTRY_CREDENTIAL_OPER, ncaId, registryCredentialId, "Updated", hostname);
        return dto;
    }

    public void deleteRegistryCredential(
            String ncaId,
            UUID registryCredentialId,
            AuditEventPayload.Builder payloadBuilder,
            boolean deleteSystemProvisioned) {
        var entity = registryCredentialLookupService
                .lookupRegistryCredentialByAccountAndIdOrThrow(ncaId, registryCredentialId);
        if (!deleteSystemProvisioned && entity.getProvisionedBy() == ProvisionedBy.SYSTEM) {
            log.error(MESG_SYSTEM_PROVISIONED_REGISTRY_CRED);
            throw new BadRequestException(MESG_SYSTEM_PROVISIONED_REGISTRY_CRED);
        }

        var messages = registryCredentialFunctionService
                .validateRegistryCredentialDeletion(ncaId, entity);
        if (!CollectionUtils.isEmpty(messages)) {
            var mesg = messages.toString();
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        registryCredentialEssService.deleteRegistryCredentialSecret(ncaId, registryCredentialId);
        registryCredentialLookupService.deleteRegistryCredential(ncaId, entity);

        registryCredentialAuditService.auditRegistryCredentialDelete(payloadBuilder, entity);
        var hostname = entity.getRegistryHostname();
        log.info(MESG_REGISTRY_CREDENTIAL_OPER, ncaId, registryCredentialId, "Deleted", hostname);
    }

    public void deleteAllRegistryCredentials(
            String ncaId,
            AuditEventPayload.Builder auditEventPayloadBuilder) {
        registryCredentialLookupService.lookupRegistryCredentialByAccount(ncaId)
                .forEach(rc -> deleteRegistryCredential(ncaId,
                                                        rc.getKey().getRegistryCredentialId(),
                                                        auditEventPayloadBuilder,
                                                        true)); // Delete system provisioned
    }

    private void validateAddRegistryCredentialRequest(
            String ncaId,
            AccountEntity accountEntity,
            AddRegistryCredentialRequest request) {

        var secretName = request.secret().name();
        var hostname = request.registryHostname();

        validateSecretFormat(hostname, request.secret());
        validateRegistryCredentialIsForRecognizedRegistry(request.artifactTypes(), hostname);
        validateMaxRegistryCredentialLimit(accountEntity);

        var registryCreds = registryCredentialLookupService
                                .lookupRegistryCredentialByAccount(ncaId);
        registryCreds.forEach(entity -> {
            // Registry Credentials must have unique combination of hostname and credential name
            // within a NVIDIA Cloud Account. Validate whether a registry credential with the
            // specified hostname and credential name already exists for the specified account.
            if (entity.getRegistryHostname().equals(hostname)
                    && entity.getRegistryCredentialName().equals(secretName)) {
                var mesg = MESG_REGISTRY_CREDENTIAL_ALREADY_EXISTS
                        .formatted(ncaId, secretName, hostname);
                log.error(mesg);
                throw new ConflictException(mesg);
            }
        });

        registryCredentialValidationService.validateRegistryCredentials(hostname,
                request.artifactTypes(), List.of(request.secret().value().asString()));
    }

    // Only recognized registries should be added to an account. Returns the display name
    // of the recognized registry.
    private String validateRegistryCredentialIsForRecognizedRegistry(
            Set<ArtifactTypeEnum> artifactTypes,
            String hostname) {
        var recognizedRegistryNames = artifactTypes.stream()
                .map(artifactType ->
                             getRecognizedRegistryNameUsingHostname(artifactType, hostname)
                                     .orElseThrow(() -> {
                                         var mesg = MESG_REGISTRY_NOT_RECOGNIZED.formatted(
                                                 artifactType,
                                                 hostname);
                                         log.error(mesg);
                                         return new BadRequestException(mesg);
                                     }))
                .collect(Collectors.toSet());

        // This can only happen if the recognized registries are configured erroneously. For
        // example, the same hostname is used for more than one container registries in the
        // recognized registries configuration.
        if (recognizedRegistryNames.size() > 1) {
            var mesg = MESG_HOSTNAME_WITH_MULTIPLE_REGISTRY_NAMES.formatted(hostname);
            log.error(mesg);
            throw new IllegalStateException(mesg);
        }

        return recognizedRegistryNames.iterator().next();
    }

    public static void validateSecretFormat(String registryHostname, SecretDto secret) {
        var secretValue = secret.value();
        if (!(secretValue instanceof StringNode)) {
            log.error(MESG_INVALID_SECRET_FORMAT);
            throw new BadRequestException(MESG_INVALID_SECRET_FORMAT);
        }

        String secretRaw;
        try {
            secretRaw = new String(Base64.getDecoder().decode(secretValue.asString()));
        } catch (Exception ex) {
            log.error(MESG_INVALID_SECRET_FORMAT);
            throw new BadRequestException(MESG_INVALID_SECRET_FORMAT);
        }

        var usernameAndPassword = secretRaw.split(":");
        if (usernameAndPassword.length != 2 || Strings.isBlank(usernameAndPassword[0]) ||
                Strings.isBlank(usernameAndPassword[1])) {
            log.error(MESG_INVALID_SECRET_FORMAT);
            throw new BadRequestException(MESG_INVALID_SECRET_FORMAT);
        }

        if ((registryHostname.endsWith("nvcr.io") || registryHostname.endsWith("ngc.nvidia.com"))
                && !secretRaw.contains("$oauthtoken")) {
            log.error(MESG_MISSING_OAUTHTOKEN_USERNAME);
            throw new BadRequestException(MESG_MISSING_OAUTHTOKEN_USERNAME);
        }
    }

    private void validateMaxRegistryCredentialLimit(AccountEntity accountEntity) {
        var ncaId = accountEntity.getNcaId();
        var maxRegistryCredentialsAllowed = accountEntity.getMaxRegistryCredentialsAllowed();
        var currentRegistryCredentialsCount =
                registryCredentialLookupService.getRegistryCredentialCountByAccount(ncaId);

        if (maxRegistryCredentialsAllowed != null
                && currentRegistryCredentialsCount >= maxRegistryCredentialsAllowed) {
            var mesg = MESG_REGISTR_CREDENTIAL_LIMIT_EXCEEDED.formatted(
                    ncaId,
                    maxRegistryCredentialsAllowed,
                    currentRegistryCredentialsCount);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private void validateUpdateRegistryCredentialRequest(
            UpdateRegistryCredentialRequest request,
            RegistryCredentialByAccountEntity entity) {
        var hostname = entity.getRegistryHostname();
        var registryName = entity.getRegistryName();
        var artifactTypes = entity.getArtifactTypes();
        var artifactTypeEnums = RegistryFunctionMapperService.toArtifactTypeEnums(artifactTypes);

        if (CollectionUtils.isEmpty(request.artifactTypeEnums()) && request.secret() == null) {
            log.error(MESG_MISSING_BOTH_SECRET_AND_ARTIFACT_TYPES);
            throw new BadRequestException(MESG_MISSING_BOTH_SECRET_AND_ARTIFACT_TYPES);
        }

        if (request.secret() != null) {
            validateSecretFormat(hostname, request.secret());
        }

        if (!CollectionUtils.isEmpty(request.artifactTypeEnums())) {
            for (ArtifactTypeEnum artifactTypeEnum : request.artifactTypeEnums()) {
                if (artifactTypeEnums.contains(artifactTypeEnum)) {
                    var regCredId = entity.getKey().getRegistryCredentialId();
                    var mesg = MESG_ARTIFACT_TYPE_ALREADY_SPECIFIED
                            .formatted(regCredId, artifactTypeEnum);
                    log.warn(mesg);
                }
                getRecognizedRegistryNameUsingHostname(artifactTypeEnum, hostname)
                        .orElseThrow(() -> {
                            var mesg = MESG_NOT_RECOGNIZED_FOR_ARTIFACT_TYPE
                                    .formatted(registryName, hostname, artifactTypeEnum);
                            log.error(mesg);
                            return new BadRequestException(mesg);
                        });
            }
        }

        var ncaId = entity.getKey().getNcaId();
        var registryCredentialId = entity.getKey().getRegistryCredentialId();
        var otherRegistryCreds = registryCredentialLookupService
                .lookupRegistryCredentialByAccount(ncaId)
                .filter(credEntity ->
                                !credEntity.getKey().getRegistryCredentialId()
                                        .equals(registryCredentialId));
        otherRegistryCreds
                .forEach(credEntity -> {
                    if (request.secret() == null) {
                        return;
                    }

                    var secretName = request.secret().name();
                    // Registry Credentials must have unique combination of hostname and credential
                    // name within an NVIDIA Cloud Account. Validate whether a registry credential
                    // with the specified hostname and credential name already exists for the
                    // specified account when updating secret name
                    if (StringUtils.isNotBlank(secretName)
                            && credEntity.getRegistryHostname().equals(hostname)
                            && credEntity.getRegistryCredentialName().equals(secretName)) {
                        var mesg = MESG_REGISTRY_CREDENTIAL_UPDATE_CONFLICTED
                                .formatted(ncaId, registryCredentialId, secretName, hostname);
                        log.error(mesg);
                        throw new ConflictException(mesg);
                    }
                });

        // If secret is being updated, validate it against existing (and new if provided)
        // artifact types. If only artifact types are being added, re-validate existing secret
        // for the new artifact types.
        if (request.secret() != null) {
            Set<ArtifactTypeEnum> artifactTypesToValidate = artifactTypeEnums;
            if (!CollectionUtils.isEmpty(request.artifactTypeEnums())) {
                artifactTypesToValidate = new HashSet<>(artifactTypeEnums);
                artifactTypesToValidate.addAll(request.artifactTypeEnums());
            }
            registryCredentialValidationService.validateRegistryCredentials(hostname,
                    artifactTypesToValidate, List.of(request.secret().value().asString()));
        } else if (!CollectionUtils.isEmpty(request.artifactTypeEnums())) {
            var existingSecret = registryCredentialEssService
                    .getRegistryCredentialSecret(ncaId, registryCredentialId)
                    .orElseThrow(() -> {
                        var mesg = "Account '%s': Registry credential '%s' missing secret"
                                .formatted(ncaId, registryCredentialId);
                        log.error(mesg);
                        return new IllegalStateException(mesg);
                    });

            registryCredentialValidationService.validateRegistryCredentials(hostname,
                    request.artifactTypeEnums(), List.of(existingSecret.value().asString()));
        }
    }

    private Optional<String> getRecognizedRegistryNameUsingHostname(
            ArtifactTypeEnum artifactTypeEnum,
            String hostname) {
        var normalizedHostname =
                registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        switch (artifactTypeEnum) {
            case ArtifactTypeEnum.CONTAINER -> {
                return Optional.ofNullable(
                        registryLookupService.getContainerRegistryNameByHostName(
                                normalizedHostname));
            }
            case ArtifactTypeEnum.HELM -> {
                return Optional.ofNullable(
                        registryLookupService.getHelmRegistryNameByHostName(normalizedHostname));
            }
            case ArtifactTypeEnum.MODEL -> {
                return Optional.ofNullable(
                        registryLookupService.getModelRegistryNameByHostName(normalizedHostname));
            }
            case ArtifactTypeEnum.RESOURCE -> {
                return Optional.ofNullable(
                        registryLookupService.getResourceRegistryNameByHostName(
                                normalizedHostname));
            }
            default -> {
                var errMsg = MESG_MISSING_ARTIFACT_ENUM_IMPLEMENTATION.formatted(artifactTypeEnum);
                log.error(errMsg);
                throw new NotImplementedException(errMsg);
            }
        }
    }

}
