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
package com.nvidia.nvct.service.token.client;

import static com.nvidia.nvct.util.NvctConstants.ESS_NAMESPACE;
import static java.lang.String.format;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientNotaryProperties;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.token.client.NotaryStubService.SecretPathsAssertion;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignResponse;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignSecretPathsRequest;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignWorkerAccessRequest;
import com.nvidia.nvct.service.token.client.NotaryStubService.WorkerAccessAssertion;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Service
@RefreshScope
@Slf4j
public class NotaryClient {

    public enum Audience {
        ESS,
        NVCT;
    }

    private static final String TELEMETRY_SECRETS_PATH_TEMPLATE = "accounts/%s/telemetries/%s";
    private static final String TASKS_SECRETS_PATH_TEMPLATE = "tasks/%s/secrets";
    private static final String MESG_ASSERTION_MISSING_MESSAGE =
            "Assertion missing from notary response";
    public static final String CLIENT_REGISTRATION_ID = "notary";

    private final NotaryStubService notaryStubService;
    private final Map<Audience, String> audiences;
    private final EssService essService;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public NotaryClient(
            @Value("${nvct.notary.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.notary.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.notary.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.notary.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.notary.token-uri}") String tokenUri,
            NotaryAudiencesConfiguration audiencesConfiguration,
            EssService essService,
            ManagedHttpResources notaryHttpResources,
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            Optional<StaticClientNotaryProperties> staticClientNotaryProperties) {
        this.audiences = audiencesConfiguration.getAudiences();
        this.essService = essService;
        var authFilter = oauthFilter(staticClientNotaryProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(notaryHttpResources.connector())
                .filter(NvctOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(authFilter)
                .filter(NvctOAuth2ClientUtils.getResponseFilterProcessor("Notary"))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.notaryStubService = factory.createClient(NotaryStubService.class);
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientNotaryProperties> staticClientNotaryProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientNotaryProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvctOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public String issueSecretPathsAssertionToken(String ncaId, TelemetriesUdt telemetries) {
        var paths = getTelemetrySecretPaths(ncaId, telemetries);
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueSecretPathsAssertionToken(UUID taskId) {
        var paths = Set.of(String.format(TASKS_SECRETS_PATH_TEMPLATE, taskId));
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueSecretPathsAssertionToken(
            String ncaId,
            UUID taskId,
            TelemetriesUdt telemetries) {
        var paths = getTelemetrySecretPaths(ncaId, telemetries);
        paths.add(String.format(TASKS_SECRETS_PATH_TEMPLATE, taskId));
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueWorkerAccessAssertionToken(String ncaId, UUID taskId) {
        var assertion = new WorkerAccessAssertion(ncaId, taskId);
        var audience = audiences.get(Audience.NVCT);
        var request = new SignWorkerAccessRequest(List.of(audience), assertion);
        var response = notaryStubService.signWorkerAccess(request);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    private String issueSecretPathsAssertionTokenInternal(Set<String> paths) {
        var assertions = new SecretPathsAssertion(ESS_NAMESPACE, paths.stream().toList());
        var audience = audiences.get(Audience.ESS);
        var secretPathsRequest = new SignSecretPathsRequest(List.of(audience), assertions);
        var response = notaryStubService.signSecretPaths(secretPathsRequest);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    private Set<String> getTelemetrySecretPaths(
            String ncaId,
            TelemetriesUdt telemetries) {
        var telemetrySecretPaths = new HashSet<String>();
        if (telemetries == null) {
            return telemetrySecretPaths; // Return an updatable/modifiable set.
        }

        var logsTelemetryId = telemetries.getLogsTelemetryId();
        if (logsTelemetryId != null
                && essService.telemetrySecretExist(ncaId, logsTelemetryId)) {
            var path = format(TELEMETRY_SECRETS_PATH_TEMPLATE, ncaId, logsTelemetryId);
            telemetrySecretPaths.add(path);
        }

        var metricsTelemetryId = telemetries.getMetricsTelemetryId();
        if (metricsTelemetryId != null
                && essService.telemetrySecretExist(ncaId, metricsTelemetryId)) {
            var path = format(TELEMETRY_SECRETS_PATH_TEMPLATE, ncaId, metricsTelemetryId);
            telemetrySecretPaths.add(path);
        }

        var tracesTelemetryId = telemetries.getTracesTelemetryId();
        if (tracesTelemetryId != null
                && essService.telemetrySecretExist(ncaId, tracesTelemetryId)) {
            var path = format(TELEMETRY_SECRETS_PATH_TEMPLATE, ncaId, tracesTelemetryId);
            telemetrySecretPaths.add(path);
        }
        return telemetrySecretPaths;
    }

}
