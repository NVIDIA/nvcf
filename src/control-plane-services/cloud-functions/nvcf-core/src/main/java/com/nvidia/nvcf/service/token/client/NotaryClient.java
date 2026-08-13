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
package com.nvidia.nvcf.service.token.client;

import static java.lang.String.format;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientNotaryProperties;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import com.nvidia.nvcf.rest.function.invocation.dto.MultiFunctionsInvocationTokenRequest;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.token.client.NotaryService.FunctionMetadataAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.InstanceCredentialsAssertions;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.SecretPathsAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.SignFunctionInvocationRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignFunctionMetadataRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignInstanceCredentialsRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignResponse;
import com.nvidia.nvcf.service.token.client.NotaryService.SignSecretPathsRequest;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
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

    public static final String CLIENT_REGISTRATION_ID = "notary";

    private final EssService essService;

    public enum Audience {
        SBS,
        ESS,
        NVCF,
        TURN;
    }

    private static final String FUNCTION_SECRETS_PATH_TEMPLATE = "functions/%s/versions/%s";
    private static final String TELEMETRY_SECRETS_PATH_TEMPLATE = "accounts/%s/telemetries/%s";
    private static final String MESG_ASSERTION_MISSING_MESSAGE = "assertion missing from notary response";

    private static final String ESS_NAMESPACE = "nvcf";

    private final NotaryService service;
    private final Map<Audience, String> audiences;
    private final boolean turnEnabled;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public NotaryClient(
            EssService essService,
            @Value("${nvcf.notary.base-url}") String baseUrl,
            @Value("${spring.security.oauth2.client.registration.notary.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.notary.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.notary.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.notary.token-uri}") String tokenUri,
            @Value("${nvcf.notary.turn.enabled:false}") boolean turnEnabled,
            Optional<StaticClientNotaryProperties> staticClientNotaryProperties,
            NotaryAudiencesConfiguration audiencesConfiguration,
            WebClient.Builder webClientBuilder,
            ManagedHttpResources notaryHttpResources) {
        this.audiences = audiencesConfiguration.getAudiences();
        this.essService = essService;
        this.turnEnabled = turnEnabled;
        var webClient = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(notaryHttpResources.connector())
                .filter(NvcfOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(oauthFilter(staticClientNotaryProperties, webClientBuilder,
                                    clientId, clientSecret, scope, tokenUri))
                .filter(NvcfOAuth2ClientUtils.getResponseFilterProcessor("Notary"))
                .build();
        this.service = HttpServiceProxyFactory
                .builderFor(WebClientAdapter.create(webClient))
                .build()
                .createClient(NotaryService.class);
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
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public String issueInstanceCredentialAssertionToken(
            UUID functionId,
            UUID functionVersionId,
            String instanceId,
            List<String> instanceIps) {
        var assertions = new InstanceCredentialsAssertions(functionId,
                                                           functionVersionId,
                                                           instanceId,
                                                           instanceIps);
        var audienceServiceIds = turnEnabled
                ? List.of(audiences.get(Audience.SBS), audiences.get(Audience.TURN))
                : List.of(audiences.get(Audience.SBS));
        var request = new SignInstanceCredentialsRequest(audienceServiceIds, assertions);
        var response = service.signInstanceCredentials(request);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    public String issueSecretPathsAssertionToken(String ncaId, TelemetriesUdt telemetries) {
        var paths = getTelemetrySecretPaths(ncaId, telemetries);
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueSecretPathsAssertionToken(UUID functionId, UUID functionVersionId) {
        var path = format(FUNCTION_SECRETS_PATH_TEMPLATE, functionId, functionVersionId);
        var paths = Set.of(path);
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueSecretPathsAssertionToken(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            TelemetriesUdt telemetries) {
        var paths = getTelemetrySecretPaths(ncaId, telemetries);
        paths.add(format(FUNCTION_SECRETS_PATH_TEMPLATE, functionId, functionVersionId));
        return issueSecretPathsAssertionTokenInternal(paths);
    }

    public String issueFunctionMetadataAssertionToken(
            String ncaId,
            UUID functionId,
            UUID functionVersionId) {
        var assertions = new FunctionMetadataAssertion(ncaId, functionId, functionVersionId);
        var audience = audiences.get(Audience.NVCF);
        var request = new SignFunctionMetadataRequest(List.of(audience), assertions);
        var response = service.signFunctionMetadata(request);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    public String issueFunctionInvocationAssertionToken(
            MultiFunctionsInvocationTokenRequest request,
            String ncaId) {
        var assertions = new InvocationAssertion(ncaId,
                                                 null,
                                                 null,
                                                 request.functions(),
                                                 request.clientId());
        var audience = audiences.get(Audience.NVCF);
        var signRequest = new SignFunctionInvocationRequest(List.of(audience), assertions);
        var response = service.signFunctionInvocation(signRequest);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    private String issueSecretPathsAssertionTokenInternal(Set<String> paths) {
        var assertions = new SecretPathsAssertion(ESS_NAMESPACE, paths.stream().toList());
        var audience = audiences.get(Audience.ESS);
        var request = new SignSecretPathsRequest(List.of(audience), assertions);
        var response = service.signSecretPaths(request);
        return Optional.ofNullable(response)
                .map(SignResponse::assertion)
                .orElseThrow(() -> new UpstreamException(MESG_ASSERTION_MISSING_MESSAGE));
    }

    private Set<String> getTelemetrySecretPaths(
            String ncaId,
            TelemetriesUdt telemetries) {
        var telemetrySecretPaths = new HashSet<String>();
        if (telemetries == null) {
            return telemetrySecretPaths; // Return an updatable/modifiable list.
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
