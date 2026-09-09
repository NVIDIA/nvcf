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
package com.nvidia.icms.outbound.fnds;

import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;
import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.wireMockConfig;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientFndsProperties;
import com.nvidia.icms.outbound.fnds.model.FndsMessageDetailModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageV2Model;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import java.time.Instant;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Integration test: {@link FunctionDeploymentStagesClient}'s {@code @Autowired} constructor picks
 * an auth filter based on whether a static Vault-signed token is configured. Verifies the outbound
 * request against a local mock HTTP server, mirroring {@code OAuth2BearerFilterIntegrationTest} in
 * nv-boot-starter-telemetry.
 */
class FunctionDeploymentStagesClientIntegrationTest {

    private static final String CLIENT_ID = "fnds-client";
    private static final String CLIENT_SECRET = "fnds-secret";
    private static final String SCOPE = "fnds";

    private WireMockServer fndsServer;
    private ManagedHttpResources httpResources;

    @BeforeEach
    void startServer() {
        fndsServer = new WireMockServer(wireMockConfig().dynamicPort());
        fndsServer.start();
        httpResources = OAuth2ClientUtils.getClientHttpConnectorManaged("fnds-it");
    }

    @AfterEach
    void stopServer() {
        RequestContextHolder.resetRequestAttributes();
        if (httpResources != null) {
            httpResources.close();
        }
        if (fndsServer != null) {
            fndsServer.stop();
        }
    }

    @Test
    void staticTokenConfigured_attachesFixedBearerHeader() {
        String staticToken = "test-static-event-ledger-token";
        fndsServer.stubFor(post(urlPathEqualTo("/v3/ledger/cloudevents"))
                .willReturn(aResponse().withStatus(202)));

        var staticProperties = new StaticClientFndsProperties();
        staticProperties.setToken(staticToken);

        var client = buildClient(Optional.of(staticProperties), fndsServer.baseUrl() + "/oauth/token");

        client.sendFunctionDeploymentStage(createFndsMessage());

        fndsServer.verify(postRequestedFor(urlPathEqualTo("/v3/ledger/cloudevents"))
                .withHeader("Authorization", equalTo("Bearer " + staticToken)));
    }

    // ServletOAuth2AuthorizedClientExchangeFilterFunction only attempts a token exchange if it can
    // resolve the current HttpServletRequest/HttpServletResponse (via Spring Security's global
    // Reactor-context hook, wired through RequestContextHolder). Registering mock servlet
    // attributes here reproduces that request-scoped context so the OAuth2 client-credentials
    // fallback actually runs, letting us stub /oauth/token and assert the returned bearer token is
    // attached - while still proving the static token is not used.
    @Test
    void noStaticTokenConfigured_fallsBackToOAuth2BearerToken() {
        RequestContextHolder.setRequestAttributes(
                new ServletRequestAttributes(new MockHttpServletRequest(), new MockHttpServletResponse()));

        String oauthToken = "test-oauth2-access-token";
        fndsServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("{"
                                + "\"access_token\":\"" + oauthToken + "\","
                                + "\"token_type\":\"Bearer\","
                                + "\"expires_in\":3600"
                                + "}")));
        fndsServer.stubFor(post(urlPathEqualTo("/v3/ledger/cloudevents"))
                .willReturn(aResponse().withStatus(202)));

        var client = buildClient(Optional.empty(), fndsServer.baseUrl() + "/oauth/token");

        Integer responseCode = client.sendFunctionDeploymentStage(createFndsMessage());

        assertEquals(202, responseCode);
        fndsServer.verify(postRequestedFor(urlPathEqualTo("/v3/ledger/cloudevents"))
                .withHeader("Authorization", equalTo("Bearer " + oauthToken)));
    }

    private FunctionDeploymentStagesClient buildClient(
            Optional<StaticClientFndsProperties> staticClientFndsProperties, String tokenUri) {
        var icmsConfigurationProperties = mock(IcmsConfigurationProperties.class);
        when(icmsConfigurationProperties.isFndsMessagesEnabled()).thenReturn(true);
        when(icmsConfigurationProperties.isFndsMessagesV1Enabled()).thenReturn(false);
        when(icmsConfigurationProperties.isFndsMessagesV2Enabled()).thenReturn(false);
        when(icmsConfigurationProperties.isFndsMessagesV3Enabled()).thenReturn(true);

        var telemetryEventClient = mock(TelemetryEventClient.class);
        var eventFormat = EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);

        return new FunctionDeploymentStagesClient(
                fndsServer.baseUrl(),
                CLIENT_ID,
                CLIENT_SECRET,
                SCOPE,
                tokenUri,
                staticClientFndsProperties,
                icmsConfigurationProperties,
                telemetryEventClient,
                eventFormat,
                httpResources,
                WebClient.builder());
    }

    private FndsMessageV2Model createFndsMessage() {
        return FndsMessageV2Model.builder()
                .ncaId(UUID.randomUUID().toString())
                .functionId(UUID.randomUUID().toString())
                .functionVersionId("version-1")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .instanceId("instance-1")
                .event(FndsStages.STAGE_READY.toString())
                .eventType("SIS")
                .timestamp(Instant.now().toString())
                .details(new FndsMessageDetailModel())
                .build();
    }
}
