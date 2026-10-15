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
package com.nvidia.ess.controller;

import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.ENTITY_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.NAMESPACE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.REQUEST_ID_KEY;
import static com.nvidia.ess.util.ControllerTestCases.buildUrl;
import static com.nvidia.ess.util.ControllerTestCases.invalidHeadersArguments;
import static com.nvidia.ess.util.ControllerTestCases.invalidNamespaceOrEntityTypeOrEntityNameArguments;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.config.BeanConfig;
import com.nvidia.ess.config.SecurityConfiguration;
import com.nvidia.ess.config.WebConfiguration;
import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.facade.EntityFacade;
import com.nvidia.ess.filter.NamespaceHeaderWebFilter;
import com.nvidia.ess.filter.RequestIdWebFilter;
import com.nvidia.ess.filter.UriPathValidationWebFilter;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.validator.NotBlankAndUriSafeValidationHelper;
import java.net.URLEncoder;
import java.nio.charset.Charset;
import java.util.stream.Stream;
import org.apache.commons.lang3.StringUtils;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webclient.autoconfigure.WebClientAutoConfiguration;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.boot.webflux.test.autoconfigure.WebFluxTest;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
@ContextConfiguration(classes = {EntityController.class, CustomExceptionHandler.class,
        SecurityConfiguration.class,
        NamespaceHeaderWebFilter.class, UriPathValidationWebFilter.class,
        NotBlankAndUriSafeValidationHelper.class, CustomExceptionHandler.class,
        BeanConfig.class, RequestIdWebFilter.class,
        WebClientAutoConfiguration.class, LoggingProperties.class,
        WebConfiguration.class})
@WebFluxTest(value = EntityController.class, properties = {
        "management.tracing.enabled=false",
        "nv-boot.reloadable-properties.enabled=false",
        "spring.profiles.active=dummy",
        "spring.application.name=ess-core-test",
        "spring.application.version=0.0.1-test"})
@AutoConfigureWebTestClient(timeout = "60000")
class EntityControllerTest {

    @Autowired
    private WebTestClient webTestClient;

    @MockitoBean
    private CustomMetricsRegistry customMetricsRegistry;

    @MockitoBean
    private EntityFacade entityFacade;

    @MockitoBean
    private AuthChecker authChecker;

    @MockitoBean(name = TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    private static Stream<Arguments> invalidGetAndExistsEntityArguments() {

        return Stream.concat(
            Stream.concat(
                invalidHeadersArguments()
                        .map(arguments -> Arguments.of(arguments.get()[0], arguments.get()[1], "entityTypeA/entityId1")),
                invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .map(invalidNsName -> Arguments.of("auth", invalidNsName, "entityTypeA/entityId1"))
            ),
            Stream.concat(
                invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .filter(invalidEntityType -> !StringUtils.isBlank(invalidEntityType))
                        .map(invalidEntityType ->
                                Arguments.of("auth", "namespace",
                                        URLEncoder.encode(invalidEntityType, Charset.defaultCharset()) + "/entityId1")
                        ),
                invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .filter(invalidEntityId -> !StringUtils.isBlank(invalidEntityId))
                        .map(invalidEntityId ->
                                Arguments.of("auth", "namespace",
                                        "entityTypeA/" + URLEncoder.encode(invalidEntityId, Charset.defaultCharset()))
                        )
            )
        );
    }

    @ParameterizedTest
    @MethodSource("invalidGetAndExistsEntityArguments")
    void deleteEntity_onInvalidInput_400(String authorization, String namespace,
            String entity) {
        webTestClient.delete()
                .uri(buildUrl("/v1/" + entity))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents, never())
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), anyString());
    }

    @Test
    void deleteEntity_validInput_204() {

        var namespace = "namespace";
        var authHeader = "Bearer tokenString";
        var entityType = "entityType";
        var entityId = "entityId";

        when(authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_ENTITIES_ADMIN}))
                .thenReturn(Mono.just(true));

        when(entityFacade.deleteEntity(namespace, entityType, entityId))
                .thenReturn(Mono.empty());

        webTestClient.delete()
                .uri(buildUrl(String.format("/v1/%s/%s", entityType, entityId)))
                .header(HttpHeaders.AUTHORIZATION, authHeader)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.NO_CONTENT)
                .expectBody()
                .isEmpty();

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(namespace));
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
    }

    @ParameterizedTest
    @MethodSource("invalidGetAndExistsEntityArguments")
    void existsEntity_onInvalidInput_400(String authorization, String namespace,
            String entity) {
        webTestClient.head()
                .uri(buildUrl("/v1/" + entity))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents, never())
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), anyString());
    }

    @Test
    void existsEntity_validInput_200() {

        var namespace = "namespace";
        var authHeader = "Bearer tokenString";
        var entityType = "entityType";
        var entityId = "entityId";

        when(authChecker.authTenant(namespace, authHeader, new String[]{
                        AuthScope.ESS_SECRETS_CONSUMER,
                        AuthScope.ESS_SECRETS_ADMIN,
                        AuthScope.ESS_ENTITIES_ADMIN}))
                .thenReturn(Mono.just(true));

        when(entityFacade.entityExists(namespace, entityType, entityId))
                .thenReturn(Mono.empty());
            

        webTestClient.head()
                .uri(buildUrl(String.format("/v1/%s/%s", entityType, entityId)))
                .header(HttpHeaders.AUTHORIZATION, authHeader)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectBody()
                .isEmpty();

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(namespace));
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
    }
}
