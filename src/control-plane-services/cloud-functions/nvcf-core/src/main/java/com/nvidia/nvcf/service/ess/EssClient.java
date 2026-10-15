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
package com.nvidia.nvcf.service.ess;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientEssProperties;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.service.ess.EssStubService.SaveSecretsRequest;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import jakarta.annotation.Nonnull;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;
import tools.jackson.databind.JsonNode;

@Slf4j
@Service
@RefreshScope
@SuppressWarnings("java:S1075")
public class EssClient {

    public static final String CLIENT_REGISTRATION_ID = "ess";

    private static final String NVCF_NAMESPACE = "nvcf";

    private static final String MESG_ESS_DISABLED = "ESS interaction is disabled";

    private static final String MESG_NO_SECRETS_TO_SAVE =
            "Function '%s', version '%s': No user secrets specified to save";
    private static final String MESG_MISSING_RESPONSE_BODY_FUNCTION_SECRETS =
            "Function '%s', version '%s': %s - ESS response body cannot be null";
    private static final String MESG_MISSING_RESPONSE_BODY_REGISTRY_SECRET =
            "Account '%s', Registry Credential '%s': %s - ESS response body cannot be null";
    private static final String MESG_MISSING_RESPONSE_BODY_TELEMETRY_SECRET =
            "Account '%s', Telemetry '%s': %s - ESS response body cannot be null";

    private final EssStubService essStubService;
    private final boolean enabled;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public EssClient(
            @Value("${nvcf.ess.base-url}") String baseUrl,
            @Value("${nvcf.ess.enabled:true}") boolean enabled,
            @Value("${spring.security.oauth2.client.registration.ess.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.ess.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.ess.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.ess.token-uri}") String tokenUri,
            Optional<StaticClientEssProperties> staticClientEssProperties,
            WebClient.Builder webClientBuilder,
            ManagedHttpResources essHttpResources) {
        this.enabled = enabled;
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(essHttpResources.connector())
                .filter(NvcfOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(oauthFilter(staticClientEssProperties, webClientBuilder,
                                    clientId, clientSecret, scope, tokenUri))
                .filter(NvcfOAuth2ClientUtils.getResponseFilterProcessor("ESS"))
                .build();
        this.essStubService = HttpServiceProxyFactory
                .builderFor(WebClientAdapter.create(webClient))
                .build()
                .createClient(EssStubService.class);
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientEssProperties> staticClientEssProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientEssProperties
                .map(p -> (ExchangeFilterFunction)
                            new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    @Nonnull
    public UUID saveFunctionVersionSecrets(
            UUID functionId, UUID versionId, Set<SecretDto> secrets) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return UUID.randomUUID();
        }

        if (CollectionUtils.isEmpty(secrets)) {
            // Shouldn't have reached here if there are no secrets in the request payload.
            var mesg = MESG_NO_SECRETS_TO_SAVE.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_FUNCTION_SECRETS
                        .formatted(functionId, versionId, "Save Function Secrets");
        return saveSecretsInternal(
                () -> essStubService.saveFunctionVersionSecrets(
                        functionId, versionId, NVCF_NAMESPACE, toSaveRequest(secrets)),
                errorContext);
    }

    @Nonnull
    public UUID saveRegistryCredentialSecret(
            String ncaId,
            UUID registryCredentialId,
            SecretDto secret) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return UUID.randomUUID();
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_REGISTRY_SECRET
                        .formatted(ncaId, registryCredentialId, "Save Registry Secret");
        return saveSecretsInternal(
                () -> essStubService.saveRegistryCredentialSecret(
                        ncaId, registryCredentialId, NVCF_NAMESPACE, toSaveRequest(Set.of(secret))),
                errorContext);
    }

    @Nonnull
    public UUID saveTelemetrySecret(String ncaId, UUID telemetryId, SecretDto secret) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return UUID.randomUUID();
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_TELEMETRY_SECRET
                        .formatted(ncaId, telemetryId, "Save Telemetry Secret");
        return saveSecretsInternal(
                () -> essStubService.saveTelemetrySecret(
                        ncaId, telemetryId, NVCF_NAMESPACE, toSaveRequest(Set.of(secret))),
                errorContext);
    }

    public Optional<Set<String>> getFunctionVersionSecretNames(UUID functionId, UUID versionId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        return fetchFunctionVersionSecrets(functionId, versionId).map(Map::keySet);
    }

    public void deleteFunctionVersionSecrets(UUID functionId, UUID versionId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }

        essStubService.deleteFunctionVersionSecrets(functionId, versionId, NVCF_NAMESPACE);
    }

    public void deleteFunctionSecrets(UUID functionId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }

        essStubService.deleteFunctionSecrets(functionId, NVCF_NAMESPACE);
    }

    public void deleteTelemetrySecret(String ncaId, UUID telemetryId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }

        essStubService.deleteTelemetrySecret(ncaId, telemetryId, NVCF_NAMESPACE);
    }

    public void deleteRegistryCredentialSecret(String ncaId, UUID registryCredentialId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }

        essStubService.deleteRegistryCredentialSecret(
                ncaId, registryCredentialId, NVCF_NAMESPACE);
    }

    public Optional<Map<String, JsonNode>> fetchFunctionVersionSecrets(
            UUID functionId,
            UUID versionId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_FUNCTION_SECRETS
                        .formatted(functionId, versionId, "Fetch Function Secrets");
        return fetchSecretsInternal(
                () -> essStubService.fetchFunctionVersionSecrets(
                        functionId, versionId, "fetch_secret", NVCF_NAMESPACE),
                errorContext);
    }

    public Optional<Map<String, JsonNode>> fetchTelemetrySecret(String ncaId, UUID telemetryId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_TELEMETRY_SECRET
                        .formatted(ncaId, telemetryId, "Fetch Telemetry Secret");
        return fetchSecretsInternal(
                () -> essStubService.fetchTelemetrySecret(
                        ncaId, telemetryId, "fetch_secret", NVCF_NAMESPACE),
                errorContext);
    }

    public Optional<Map<String, JsonNode>> fetchRegistryCredentialSecret(
            String ncaId,
            UUID registryCredentialId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        Supplier<String> errorContext =
                () -> MESG_MISSING_RESPONSE_BODY_REGISTRY_SECRET
                        .formatted(ncaId, registryCredentialId, "Fetch Registry Secrets");
        return fetchSecretsInternal(
                () -> essStubService.fetchRegistryCredentialSecret(
                        ncaId, registryCredentialId, "fetch_secret", NVCF_NAMESPACE),
                errorContext);
    }

    private SaveSecretsRequest toSaveRequest(Set<SecretDto> secrets) {
        var data = secrets.stream()
                .collect(Collectors.toMap(SecretDto::name, SecretDto::value));
        return new SaveSecretsRequest(data);
    }

    private UUID saveSecretsInternal(
            Supplier<EssStubService.SaveSecretsResponse> call,
            Supplier<String> errorContext) {
        var response = call.get();
        return Optional.ofNullable(response)
                .map(body -> body.getData().getVersion())
                .orElseThrow(() -> {
                    String message = errorContext.get();
                    log.error(message);
                    return new UpstreamException(message);
                });
    }

    private Optional<Map<String, JsonNode>> fetchSecretsInternal(
            Supplier<EssStubService.FetchSecretsResponse> call,
            Supplier<String> errorContext) {
        try {
            var response = call.get();
            return Optional.ofNullable(response)
                    .map(body -> Optional.ofNullable(body.getData().getData()))
                    .orElseThrow(() -> {
                        var mesg = errorContext.get();
                        log.error(mesg);
                        return new UpstreamException(mesg);
                    });
        } catch (NotFoundException ex) {
            return Optional.empty();
        }
    }

}
