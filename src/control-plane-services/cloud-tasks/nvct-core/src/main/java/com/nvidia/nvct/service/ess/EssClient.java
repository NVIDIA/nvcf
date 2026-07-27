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
package com.nvidia.nvct.service.ess;

import static com.nvidia.nvct.util.NvctConstants.ESS_NAMESPACE;

import tools.jackson.databind.JsonNode;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientEssProperties;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.service.ess.EssStubService.SaveSecretsRequest;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import jakarta.annotation.Nonnull;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
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

@Slf4j
@Service
@RefreshScope
public class EssClient {

    private static final String MESG_NO_SECRETS_TO_SAVE =
            "Task '%s': No user secrets specified to save";
    private static final String MESG_MISSING_RESPONSE_BODY =
            "Task '%s': ESS '%s' - Response body cannot be null";
    private static final String MESG_MISSING_FETCH_SECRETS_RESPONSE_BODY =
            "Secret Path '%s': ESS '%s' - Response body cannot be null";
    private static final String MESG_ESS_DISABLED = "ESS interaction is disabled";

    public static final String CLIENT_REGISTRATION_ID = "ess";
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
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources essHttpResources,
            @Value("${nvct.ess.base-url}") String baseUrl,
            @Value("${nvct.ess.enabled:true}") boolean enabled,
            @Value("${spring.security.oauth2.client.registration.ess.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.ess.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.ess.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.ess.token-uri}") String tokenUri,
            Optional<StaticClientEssProperties> staticClientEssProperties) {
        var authFilter = oauthFilter(staticClientEssProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(essHttpResources.connector())
                .filter(NvctOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(authFilter)
                .filter(NvctOAuth2ClientUtils.getResponseFilterProcessor("ESS"))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.essStubService = factory.createClient(EssStubService.class);
        this.enabled = enabled;
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
                .orElseGet(() -> NvctOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    @Nonnull
    public UUID saveSecrets(UUID taskId, Set<SecretDto> secrets) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return UUID.randomUUID();
        }

        if (CollectionUtils.isEmpty(secrets)) {
            // Shouldn't have reached here if there are no secrets in the request payload.
            var mesg = MESG_NO_SECRETS_TO_SAVE.formatted(taskId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var data = secrets.stream().collect(Collectors.toMap(SecretDto::name, SecretDto::value));
        var response = essStubService.saveSecrets(taskId.toString(), ESS_NAMESPACE,
                                                  new SaveSecretsRequest(data));
        return Optional.ofNullable(response)
                .map(body -> body.getData().getVersion())
                .orElseThrow(() -> {
                    var mesg = MESG_MISSING_RESPONSE_BODY
                            .formatted(taskId, "Save Secrets");
                    log.error(mesg);
                    return new UpstreamException(mesg);
                });
    }

    public Optional<Set<String>> getSecretNames(UUID taskId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        return fetchSecrets(taskId).map(Map::keySet);
    }

    public void deleteSecrets(UUID taskId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }

        essStubService.deleteSecrets(taskId.toString(), ESS_NAMESPACE);
    }

    public Optional<Map<String, JsonNode>> fetchSecrets(UUID taskId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        try {
            var response = essStubService.fetchSecrets(taskId.toString(),
                                                       "fetch_secret",
                                                       ESS_NAMESPACE);
            return Optional.ofNullable(response)
                    .map(body -> Optional.ofNullable(body.getData().getData()))
                    .orElseThrow(() -> {
                        var mesg = MESG_MISSING_RESPONSE_BODY
                                .formatted(taskId, "Fetch Secrets");
                        log.error(mesg);
                        return new UpstreamException(mesg);
                    });
        } catch (NotFoundException ex) {
            return Optional.empty();
        }
    }

    public void deleteSecretsPath(UUID taskId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return;
        }
        essStubService.deleteSecretsPath(taskId.toString(), ESS_NAMESPACE);
    }

    public Optional<Map<String, JsonNode>> fetchTelemetrySecret(String ncaId, UUID telemetryId) {
        if (!enabled) {
            log.debug(MESG_ESS_DISABLED);
            return Optional.empty();
        }

        try {
            var response = essStubService.fetchTelemetrySecret(ncaId,
                                                               telemetryId.toString(),
                                                               "fetch_secret",
                                                               ESS_NAMESPACE);
            return Optional.ofNullable(response)
                    .map(body -> Optional.ofNullable(body.getData().getData()))
                    .orElseThrow(() -> {
                        var path = "accounts/%s/telemetries/%s".formatted(ncaId, telemetryId);
                        var mesg = MESG_MISSING_FETCH_SECRETS_RESPONSE_BODY
                                .formatted(path, "Fetch Secrets");
                        log.error(mesg);
                        return new UpstreamException(mesg);
                    });
        } catch (NotFoundException ex) {
            return Optional.empty();
        }
    }

}
