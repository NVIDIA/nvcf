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

import static com.nvidia.nvcf.service.account.AccountMapperService.toAccountDto;
import static com.nvidia.nvcf.service.account.AccountMapperService.toAccountEntity;
import static com.nvidia.nvcf.service.account.AccountMapperService.toClientEntity;
import static com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE;
import static java.lang.String.format;

import com.google.common.collect.Sets;
import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.configuration.account.AccountLimitsProperties;
import com.nvidia.nvcf.configuration.notary.NotaryServiceAuthenticationToken;
import com.nvidia.nvcf.persistence.account.AccountsRepository;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsDto;
import com.nvidia.nvcf.rest.account.dto.AccountDto;
import com.nvidia.nvcf.rest.account.dto.AccountUpdateRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import com.nvidia.nvcf.service.telemetry.TelemetryLookupService;
import com.nvidia.nvcf.service.telemetry.TelemetryService;
import java.time.Instant;
import java.util.Collection;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
public class AccountService {

    private static final String MESG_CREATE_ACCOUNT =
            "{} creating account with ncaId '{}' for '{}'";
    private static final String MESG_UPDATE_ACCOUNT =
            "{} updating account with ncaId '{}'";
    private static final String MESG_DELETE_ACCOUNT =
            "{} deleting account with ncaId '{}'";
    private static final String MESG_ASSOCIATE_CLIENT_ACCOUNT =
            "{} associate {} account with ncaId '{}'";
    private static final String MESG_DISASSOCIATE_CLIENT_ACCOUNT =
            "{} disassociate {} account with ncaId '{}'";
    private static final String MESG_NCA_ID_BLANK =
            "'ncaId' cannot be empty or null";
    private static final String MESG_NCA_ID_NOT_FOUND =
            "Account with ncaId '%s' not found";
    private static final String MESG_ACCOUNT_EXISTS =
            "Account with ncaId '%s' already exists";
    private static final String MESG_CLIENT_EXISTS =
            "Client with id '%s' already exists";
    private static final String MESG_INVALID_CLIENT =
            "Client '%s' is not associated with NVIDIA Cloud Account '%s'";
    private static final String MESG_NCAID_NON_ALPHANUMERIC =
            "Invalid request: ncaId should start with alphanumeric character";
    private static final String MESG_CLIENT_ASSOCIATE_EXISTS =
            "Client '%s' already associated with NVIDIA Cloud Account '%s'";
    private static final String MESG_ACCOUNT_HAS_FUNCTIONS =
            "Account '%s': Cannot delete account with functions defined";
    private static final String MESG_ACCOUNT_IS_AUTHORIZED_PARTY =
            "Account '%s': Cannot delete account with functions authorized";
    private static final String MESG_DELETED_CLIENTS =
            "Deleted Clients associated with Account '%s'";
    private static final String MESG_MISSING_REGISTRY_CREDENTIAL =
            "Invalid request: Missing registry credential(s) for expected artifact type(s) '%s'";
    private static final String MESG_MULTIPLE_REGISTRY_CREDENTIAL =
            "Invalid request: Multiple registry credential(s) with same artifact type(s) '%s' " +
                    "not allowed during account provisioning";
    private static final String MESG_INVALID_SECRET_VALUE_TYPE =
            "Invalid request: Registry secret value must be string";
    private static final String MESG_ACCOUNT_ID_MISMATCH =
            "Invalid request: nca_id '%s' in bearer token does not match the one in path '%s'";
    private static final String MESG_UNSUPPORTED_AUTH_OR_BAD_API_KEY_POLICY =
            "Invalid request: Unsupported Authentication class type '%s' or " +
                    "ApiKeyValidationResult object was not found.";

    private static final Pattern FIRST_CHAR_ALPHANUMERIC_REGEX = Pattern.compile("^[a-z0-9A-Z].*");

    private static final String MESG_MAX_FUNCTIONS_EXCEEDED =
            "Invalid request: 'maxFunctionsAllowed' must not exceed %d";
    private static final String MESG_MAX_TASKS_EXCEEDED =
            "Invalid request: 'maxTasksAllowed' must not exceed %d";

    private final AccountsRepository accountsRepository;
    private final AccountAuditService accountAuditService;
    private final JsonMapper jsonMapper;
    private final AccountMapperService accountMapperService;
    private final ClientService clientService;
    private final TelemetryService telemetryService;
    private final TelemetryLookupService telemetryLookupService;
    private final RegistryCredentialService registryCredentialService;
    private final RegistryCredentialLookupService registryCredentialLookupService;
    private final FunctionLookupService functionLookupService;
    private final AuthorizedPartiesService authorizedPartiesService;
    private final Set<ArtifactTypeEnum> artifactTypesForAccountProvisioning;
    private final AccountLimitsProperties accountLimitsProperties;

    public AccountService(
            AccountsRepository accountsRepository,
            AccountAuditService accountAuditService,
            JsonMapper jsonMapper,
            AccountMapperService accountMapperService,
            ClientService clientService,
            TelemetryLookupService telemetryLookupService,
            RegistryCredentialService registryCredentialService,
            RegistryCredentialLookupService registryCredentialLookupService,
            FunctionLookupService functionLookupService,
            AuthorizedPartiesService authorizedPartiesService,
            TelemetryService telemetryService,
            AccountLimitsProperties accountLimitsProperties,
            @Value("${nvcf.registries.account-provisioning.artifact-types: CONTAINER, HELM, MODEL, RESOURCE}")
            Set<ArtifactTypeEnum> artifactTypesForAccountProvisioning) {
        this.accountsRepository = accountsRepository;
        this.accountAuditService = accountAuditService;
        this.jsonMapper = jsonMapper;
        this.clientService = clientService;
        this.accountMapperService = accountMapperService;
        this.telemetryLookupService = telemetryLookupService;
        this.registryCredentialService = registryCredentialService;
        this.registryCredentialLookupService = registryCredentialLookupService;
        this.functionLookupService = functionLookupService;
        this.authorizedPartiesService = authorizedPartiesService;
        this.telemetryService = telemetryService;
        this.accountLimitsProperties = accountLimitsProperties;
        this.artifactTypesForAccountProvisioning = artifactTypesForAccountProvisioning;
    }

    // Since this method uses the value of the sub(aka client-id) claim in the JWT to map to
    // the corresponding NCA Id, it is typically used when Account Admin endpoints and gRPC
    // endpoints are invoked. The value of the sub claim in a JWT that is used to invoke Super
    // Admin endpoints may not be associated with any NCA Id as the JWT can be used across accounts.
    public String getNcaId(Authentication authentication) {
        // JWT
        if (authentication instanceof JwtAuthenticationToken) {
            var clientId = authentication.getName();  // Value of sub claim in the JWT.
            return clientService.lookupClient(clientId)
                    .map(ClientEntity::getNcaId)
                    .orElseThrow(() -> new NotFoundException(
                            "Unknown client_id '%s' when finding account id".formatted(clientId)));
        }

        // NotaryService
        if (authentication instanceof NotaryServiceAuthenticationToken token) {
            assertAccountExistsOrThrow(token.getNcaId());
            return token.getNcaId();
        }

        // ApiKey
        if (authentication.getPrincipal() instanceof DefaultOAuth2AuthenticatedPrincipal principal
                && principal.getAttributes() != null
                && principal.getAttributes()
                .get(POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            // Ideally, we would validate NCA Id from the ApiKey to confirm that a corresponding
            // account exists in NVCF -- just like we are doing it for other types of tokens above.
            // However, NGC's Personal Orgs do not have a corresponding NVCF account. Personal Orgs
            // get Cloud Functions enablement indirectly via NIM enablement. As a result, a user
            // belonging to a Personal Org can create an ApiKey with a policy containing NVCF-specific
            // resources and scopes. This allows them to invoke/list/check-queue-depth of public
            // functions from build.nvidia.com. Personal Orgs are temporary as the goal is to
            // allow the user to try out different technologies on a trial basis for certain amount
            // of time. If the user is satisfied with the trial, they can convert the temporary
            // Personal Org to an Enterprise Org. At that time, NGC provisions a corresponding
            // NVCF account. If we validate NCA Id to confirm that a corresponding account exists
            // in NVCF here and throw an exception, then users of the Personal Orgs will not be
            // able to invoke, list, check-queue-depth of public functions as their requests will
            // fail with a 4xx response.
            return policyResult.ncaId();
        }

        throw new UnprocessableEntityException(MESG_UNSUPPORTED_AUTH_OR_BAD_API_KEY_POLICY
                                                       .formatted(authentication.getClass()));
    }

    public AccountDto createCloudAccount(
            String ncaId,
            CreateAccountRequest request,
            AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_CREATE_ACCOUNT, "Start", ncaId, request.name());
        validateAccountLimits(request);
        validateNcaId(ncaId); // Validate nca-id
        validateRegsitryCredentialsInRequest(request);

        var adminClientId = request.adminClientId();
        if (StringUtils.isNotBlank(adminClientId)) {
            clientService.lookupClient(adminClientId)
                    .ifPresent(client -> {
                        var mesg = format(MESG_CLIENT_EXISTS, adminClientId);
                        log.error(mesg);
                        throw new ConflictException(mesg);
                    });
        }
        accountsRepository.findById(ncaId)
                .ifPresent(account -> {
                    var mesg = format(MESG_ACCOUNT_EXISTS, ncaId);
                    log.error(mesg);
                    throw new ConflictException(mesg);
                });

        var accountName = request.name();
        var accountEntity = toAccountEntity(ncaId, request, accountLimitsProperties);
        var clientEntity = toClientEntity(ncaId, adminClientId, accountName);

        var registryCredentials = request.registryCredentials();
        if (!CollectionUtils.isEmpty(registryCredentials)) {
            registryCredentialService.addRegistryCredentials(ncaId,
                                                             accountEntity,
                                                             registryCredentials,
                                                             payloadBuilder);
        }
        accountsRepository.save(accountEntity);
        clientEntity.ifPresent(clientService::saveClient);
        accountAuditService.auditAccountCreate(payloadBuilder, accountEntity);

        log.info(MESG_CREATE_ACCOUNT, "Completed", ncaId, request.name());
        return toAccountDto(accountEntity);
    }

    public AccountDto updateAccount(
            String ncaId,
            AccountUpdateRequest accountUpdateRequest,
            AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_UPDATE_ACCOUNT, "Start", ncaId);
        var accountEntity = getAccount(ncaId);
        var jsonBefore = jsonMapper.valueToTree(accountEntity);

        if (StringUtils.isNotBlank(accountUpdateRequest.name())) {
            accountEntity.setName(accountUpdateRequest.name());
        }
        if (Objects.nonNull(accountUpdateRequest.maxFunctionsAllowed())) {
            accountEntity.setMaxFunctionsAllowed(accountUpdateRequest.maxFunctionsAllowed());
        }
        if (Objects.nonNull(accountUpdateRequest.maxTasksAllowed())) {
            accountEntity.setMaxTasksAllowed(accountUpdateRequest.maxTasksAllowed());
        }
        if (Objects.nonNull(accountUpdateRequest.maxTelemetriesAllowed())) {
            accountEntity.setMaxTelemetriesAllowed(
                    accountUpdateRequest.maxTelemetriesAllowed());
        }
        if (Objects.nonNull(accountUpdateRequest.maxRegistryCredentialsAllowed())) {
            accountEntity.setMaxRegistryCredentialsAllowed(
                    accountUpdateRequest.maxRegistryCredentialsAllowed());
        }
        accountEntity.setLastUpdatedAt(Instant.now());
        saveAccount(accountEntity);

        accountAuditService. auditAccountUpdate(payloadBuilder, jsonBefore, accountEntity);
        log.info(MESG_UPDATE_ACCOUNT, "Completed", ncaId);
        return toAccountDto(accountEntity);
    }

    public AccountEntity saveAccount(AccountEntity accountEntity) {
        return accountsRepository.save(accountEntity);
    }

    public void deleteAccount(String ncaId, AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_DELETE_ACCOUNT, "Start", ncaId);
        if (StringUtils.isBlank(ncaId)) {
            log.error(MESG_NCA_ID_BLANK);
            throw new IllegalArgumentException(MESG_NCA_ID_BLANK);
        }

        assertAccountExistsOrThrow(ncaId);

        var ownFunctions = functionLookupService.lookupEntitiesUsingAccountId(ncaId).toList();
        if (CollectionUtils.isNotEmpty(ownFunctions)) {
            var mesg = MESG_ACCOUNT_HAS_FUNCTIONS.formatted(ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        List<FunctionEntity> authorizedFunctions = authorizedPartiesService
                    .lookupFunctionsByAuthorizedAccount(ncaId).toList();

        if (CollectionUtils.isNotEmpty(authorizedFunctions)) {
            var mesg = MESG_ACCOUNT_IS_AUTHORIZED_PARTY.formatted(ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // Delete telemetries and corresponding secrets associated with the account.
        telemetryService.deleteAllTelemetries(ncaId);

        // Delete registry credentials and corresponding secrets associated with the account.
        registryCredentialService.deleteAllRegistryCredentials(ncaId, payloadBuilder);

        // Delete corresponding clientIds and finally the account/ncaId.
        accountsRepository
                .findById(ncaId)
                .ifPresentOrElse(
                        account -> {
                            var clientIds = Optional.ofNullable(account.getClientIds())
                                    .orElseGet(Set::of);
                            clientIds.forEach(clientService::deleteClient);
                            log.info(MESG_DELETED_CLIENTS.formatted(ncaId));
                            accountsRepository.deleteById(ncaId);
                            accountAuditService.auditAccountDelete(payloadBuilder, account);
                        },
                        () -> log.warn(format(MESG_NCA_ID_NOT_FOUND, ncaId)));
        log.info(MESG_DELETE_ACCOUNT, "Completed", ncaId);
    }

    public AccountEntity getAccount(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            log.error(MESG_NCA_ID_BLANK);
            throw new IllegalArgumentException(MESG_NCA_ID_BLANK);
        }

        return accountsRepository.findById(ncaId)
                .orElseThrow(() -> {
                    var mesg = format(MESG_NCA_ID_NOT_FOUND, ncaId);
                    log.error(mesg);
                    return new NotFoundException(mesg);
                });
    }

    public void assertAccountExistsOrThrow(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            log.error(MESG_NCA_ID_BLANK);
            throw new IllegalArgumentException(MESG_NCA_ID_BLANK);
        }

        if (!accountsRepository.existsById(ncaId)) {
            var mesg = format(MESG_NCA_ID_NOT_FOUND, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
    }

    // Used only for super admin endpoints to ensure that the NCA Id specified in the path
    // matches the corresponding property in ApiKey or NotaryService auth token. With this check,
    // the same ApiKey with admin or account_setup scopes cannot be used across accounts. This
    // is different from JWTs with admin or account_setup scopes which can be used across
    // different accounts. The reason we chose to make ApiKeys not portable across accounts as
    // they are long-lived when compared to JWTs which are ephemeral and short-lived.
    public void assertAccountIdFromPathMatches(
            String ncaId,  // Value of the path variable in super admin endpoints
            Authentication authentication) {
        // ApiKey
        if (authentication.getPrincipal() instanceof DefaultOAuth2AuthenticatedPrincipal principal
                && principal.getAttributes() != null
                && principal.getAttributes()
                .get(POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            // Check if the nca_id in the ApiKey matches the value from the path variable.
            if (!policyResult.ncaId().equals(ncaId)) {
                var mesg = MESG_ACCOUNT_ID_MISMATCH.formatted(policyResult.ncaId(), ncaId);
                log.error(mesg);
                throw new ForbiddenException(mesg);
            }
            return;
        }

        // JWT: No-op
        if (authentication instanceof JwtAuthenticationToken) {
            // No need to further check for any match for JWTs for super admin endpoints.
            return;
        }

        // NotaryService
        if (authentication instanceof NotaryServiceAuthenticationToken token) {
            // Check if the ncaId in the token matches the value from the path variable.
            if (!token.getNcaId().equals(ncaId)) {
                var mesg = MESG_ACCOUNT_ID_MISMATCH.formatted(token.getNcaId(), ncaId);
                log.error(mesg);
                throw new ForbiddenException(mesg);
            }
            return;
        }

        throw new UnprocessableEntityException(MESG_UNSUPPORTED_AUTH_OR_BAD_API_KEY_POLICY
                                                       .formatted(authentication.getClass()));
    }

    public List<AccountEntity> getAccounts() {
        return accountsRepository.findAll();
    }

    public AccountDetailsDto getAccountDetails(String ncaId) {
        var accountEntity = getAccount(ncaId);
        var telemetries = telemetryLookupService.lookupByAccount(ncaId);
        var registryCredentials = registryCredentialLookupService.getRegistryCredentialDtos(ncaId);
        return accountMapperService.toAccountDetailsDto(accountEntity, telemetries,
                                                        registryCredentials);
    }

    public AccountDto associateClient(
            String ncaId,
            String clientId,
            AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_ASSOCIATE_CLIENT_ACCOUNT, "Start", clientId, ncaId);
        clientService.lookupClient(clientId)
                .ifPresent(client -> {
                    var mesg = format(MESG_CLIENT_ASSOCIATE_EXISTS, clientId, client.getNcaId());
                    log.error(mesg);
                    throw new ConflictException(mesg);
                });

        var accountEntity = getAccount(ncaId);
        var jsonBefore = jsonMapper.valueToTree(accountEntity);
        var clientIds = Set.of(clientId);
        if (accountEntity.getClientIds() != null) {
            clientIds = Sets.union(accountEntity.getClientIds(), clientIds);
        }
        accountEntity.setClientIds(clientIds);
        accountEntity.setLastUpdatedAt(Instant.now());
        saveAccount(accountEntity);
        accountAuditService.auditAccountUpdate(payloadBuilder, jsonBefore, accountEntity);

        var clientEntity = ClientEntity.builder()
                .clientId(clientId)
                .ncaId(ncaId)
                .name(accountEntity.getName())
                .createdAt(Instant.now())
                .build();
        clientService.saveClient(clientEntity);
        log.info(MESG_ASSOCIATE_CLIENT_ACCOUNT, "Completed", clientId, ncaId);
        return AccountDto.builder()
                .ncaId(ncaId)
                .name(accountEntity.getName())
                .adminClientIds(clientIds.stream().toList())
                .maxTasksAllowed(accountEntity.getMaxTasksAllowed())
                .maxFunctionsAllowed(accountEntity.getMaxFunctionsAllowed())
                .maxTelemetriesAllowed(accountEntity.getMaxTelemetriesAllowed())
                .maxRegistryCredentialsAllowed(accountEntity.getMaxRegistryCredentialsAllowed())
                .lastUpdatedAt(accountEntity.getLastUpdatedAt())
                .build();
    }

    public AccountDto disassociateClient(
            String ncaId,
            String clientId,
            AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_DISASSOCIATE_CLIENT_ACCOUNT, "Start", clientId, ncaId);
        clientService.lookupClientOrThrow(clientId);

        var accountEntity = getAccount(ncaId);
        var jsonBefore = jsonMapper.valueToTree(accountEntity);
        var clientIds = accountEntity.getClientIds();

        if (CollectionUtils.isEmpty(clientIds) || !clientIds.contains(clientId)) {
            var mesg = MESG_INVALID_CLIENT.formatted(clientId, ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        accountEntity.setClientIds(Sets.difference(clientIds, Set.of(clientId)));
        accountEntity.setLastUpdatedAt(Instant.now());
        saveAccount(accountEntity);
        clientService.deleteClient(clientId);

        accountAuditService.auditAccountUpdate(payloadBuilder, jsonBefore, accountEntity);
        log.info(MESG_DISASSOCIATE_CLIENT_ACCOUNT, "Completed", clientId, ncaId);
        return toAccountDto(accountEntity);
    }

    private void validateAccountLimits(CreateAccountRequest request) {
        var maxFunctions = accountLimitsProperties.getMaxFunctionsAllowed();
        if (request.maxFunctionsAllowed() != null && request.maxFunctionsAllowed() > maxFunctions) {
            var mesg = MESG_MAX_FUNCTIONS_EXCEEDED.formatted(maxFunctions);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        var maxTasks = accountLimitsProperties.getMaxTasksAllowed();
        if (request.maxTasksAllowed() != null && request.maxTasksAllowed() > maxTasks) {
            var mesg = MESG_MAX_TASKS_EXCEEDED.formatted(maxTasks);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private void validateRegsitryCredentialsInRequest(CreateAccountRequest request) {
        var registryCredentials = request.registryCredentials();
        if (CollectionUtils.isEmpty(registryCredentials)) {
            return;
        }

        // Validate whether appropriate registry credentials are specified in the request during
        // account provisioning. For accounts created via NGC in the managed NVCF env, we expect
        // registry credentials for all the artifact types -- CONTAINER, HELM, MODEL, RESOURCE --
        // to be present in the request body. For accounts created under self-hosted NVCF env, it
        // is not necessary that the request body contain registry credentials for all the artifact
        // types. Admins for the self-hosted NVCF env can configure expected artifact types during
        // account provisioning using nvcf.registries.account-provisioning.artifact-types property.
        // For the managed NVCF env, this property is configured to specify all the artifact types.
        var listOfArtifactTypesInRequest = registryCredentials.stream()
                .map(RegistryCredentialDto::artifactTypes)
                .flatMap(Collection::stream)
                .toList();
        var missingArtifactTypes = Sets.difference(artifactTypesForAccountProvisioning,
                                                   Sets.newHashSet(listOfArtifactTypesInRequest));
        if (CollectionUtils.isNotEmpty(missingArtifactTypes)) {
            var mesg = MESG_MISSING_REGISTRY_CREDENTIAL.formatted(missingArtifactTypes);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // During account provisioning, NVCF only accepts one registry credential per artifact
        // type. These registry credentials are then marked as SYSTEM provisioned registry
        // credentials that are not allowed to be deleted.
        if (listOfArtifactTypesInRequest.size() > artifactTypesForAccountProvisioning.size()) {
            // If there are multiple registry credentials for any of the artifact types, then
            // we respond with 400/BadRequest.
            var uniqueArtifactTypes = new HashSet<>();
            var duplicateArtifactTypes = listOfArtifactTypesInRequest.stream()
                    .filter(at -> !uniqueArtifactTypes.add(at))
                    .collect(Collectors.toSet());
            var mesg = MESG_MULTIPLE_REGISTRY_CREDENTIAL.formatted(duplicateArtifactTypes);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // Secret value for registry credential must be a textual value and not an ObjectNode.
        var anyCredWithObjectSecretValue = registryCredentials.stream()
                .anyMatch(registryCred -> registryCred.secret().value().isObject());
        if (anyCredWithObjectSecretValue) {
            log.error(MESG_INVALID_SECRET_VALUE_TYPE);
            throw new BadRequestException(MESG_INVALID_SECRET_VALUE_TYPE);
        }

        registryCredentials.forEach(registryCredential -> {
            var hostname = registryCredential.registryHostname();

            // For all registry credentials, secret value should be base64 encoded string
            // in username:password format in ESS. For NGC registry creds, username must be
            // $oauthtoken.
            RegistryCredentialService.validateSecretFormat(hostname, registryCredential.secret());
        });
    }

    private static void validateNcaId(String ncaId) {
        if (!FIRST_CHAR_ALPHANUMERIC_REGEX.matcher(ncaId).matches()) {
            log.error(MESG_NCAID_NON_ALPHANUMERIC);
            throw new BadRequestException(MESG_NCAID_NON_ALPHANUMERIC);
        }
    }
}
