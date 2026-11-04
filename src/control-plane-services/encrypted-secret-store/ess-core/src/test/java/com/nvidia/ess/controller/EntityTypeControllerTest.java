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
import static com.nvidia.ess.util.ControllerTestCases.validNamespaceOrEntityTypeOrEntityNameArguments;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.config.BeanConfig;
import com.nvidia.ess.config.SecurityConfiguration;
import com.nvidia.ess.config.WebConfiguration;
import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.CreateEntityTypeRequest;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.facade.NamespaceFacade;
import com.nvidia.ess.filter.NamespaceHeaderWebFilter;
import com.nvidia.ess.filter.RequestIdWebFilter;
import com.nvidia.ess.filter.UriPathValidationWebFilter;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.validator.NotBlankAndUriSafeValidationHelper;
import java.net.URLEncoder;
import java.nio.charset.Charset;
import java.util.Objects;
import java.util.stream.Stream;
import org.apache.commons.lang3.StringUtils;
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
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
@ContextConfiguration(classes = {EntityTypeController.class, CustomExceptionHandler.class,
        SecurityConfiguration.class,
        NamespaceHeaderWebFilter.class, UriPathValidationWebFilter.class,
        NotBlankAndUriSafeValidationHelper.class, CustomExceptionHandler.class,
        BeanConfig.class, RequestIdWebFilter.class,
        WebClientAutoConfiguration.class, LoggingProperties.class,
        WebConfiguration.class})
@WebFluxTest(value = EntityTypeController.class, properties = {
        "management.tracing.enabled=false",
        "nv-boot.reloadable-properties.enabled=false",
        "spring.profiles.active=dummy",
        "spring.application.name=ess-core-test",
        "spring.application.version=0.0.1-test"})
@AutoConfigureWebTestClient(timeout = "60000")
class EntityTypeControllerTest {

    @Autowired
    private WebTestClient webTestClient;

    @MockitoBean
    private CustomMetricsRegistry customMetricsRegistry;

    @MockitoBean
    private NamespaceFacade namespaceFacade;

    @MockitoBean
    private AuthChecker authChecker;

    @MockitoBean(name = TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    private static Stream<Arguments> invalidCreateEntityTypeArguments() {
        return Stream.concat(
                Stream.concat(
                        invalidHeadersArguments().map(
                                arguments -> Arguments.of(arguments.get()[0], arguments.get()[1],
                                        buildCreateRequest("entityTypeA"))),
                        invalidNamespaceOrEntityTypeOrEntityNameArguments()
                                .filter(invalidNsName -> !Objects.isNull(invalidNsName))
                                .map(invalidNsName -> Arguments.of(
                                    "auth", invalidNsName, buildCreateRequest("entityTypeA")
                                ))
                ),
                invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .map(invalidEntityTypeName -> Arguments.of(
                            "auth", "namespace", buildCreateRequest(invalidEntityTypeName)
                        ))
        );
    }

    private static Stream<Arguments> validCreateEntityTypeArguments() {
        return validNamespaceOrEntityTypeOrEntityNameArguments()
                .map(validEntityTypeName -> Arguments.of(
                    "auth", "namespace", buildCreateRequest(validEntityTypeName)
                ));
    }

    static CreateEntityTypeRequest buildCreateRequest(String entityType) {
        return CreateEntityTypeRequest.builder()
                .name(entityType)
                .build();
    }

    @ParameterizedTest
    @MethodSource("invalidCreateEntityTypeArguments")
    void createEntityType_onInvalidInput_400(String authorization, String namespace,
            CreateEntityTypeRequest request) {
        webTestClient.post()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    @ParameterizedTest
    @MethodSource("validCreateEntityTypeArguments")
    void createEntityType_onValidInput_200(String authorization, String namespace,
            CreateEntityTypeRequest request) {

        when(authChecker.authOperatorOrTenant(eq(namespace), eq(authorization), any(), any()))
                .thenReturn(Mono.just(true));

        when(namespaceFacade.createEntityType(namespace, request.getName()))
                .thenReturn(Mono.just(
                        EntityTypeInfo.builder()
                                .name(request.getName())
                                .build()
                ));

        webTestClient.post()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header("X-ESS-NAMESPACE", namespace)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(EntityTypeInfo.class)
                .returnResult();

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(namespace));
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(request.getName()));
    }

    private static Stream<Arguments> invalidGetAndDeleteEntityTypeArguments() {
        return Stream.concat(
                Stream.concat(
                        invalidHeadersArguments().map(
                                arguments -> Arguments.of(arguments.get()[0], arguments.get()[1], "entityTypeA")),
                        invalidNamespaceOrEntityTypeOrEntityNameArguments()
                                .map(invalidNsName -> Arguments.of(
                                    "auth", invalidNsName, "entityTypeA"
                                ))
                ),
                invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .filter(invalidEntityTypeName -> !StringUtils.isBlank(invalidEntityTypeName))
                        .map(invalidEntityTypeName -> Arguments.of(
                            "auth", "namespace", URLEncoder.encode(invalidEntityTypeName, Charset.defaultCharset())
                        ))
        );
    }

    private static Stream<Arguments> validGetAndDeleteEntityTypeArguments() {
        return validCreateEntityTypeArguments().map(args -> Arguments.of(
                args.get()[0], args.get()[1], ((CreateEntityTypeRequest) args.get()[2]).getName()
        ));
    }

    @ParameterizedTest
    @MethodSource("invalidGetAndDeleteEntityTypeArguments")
    void deleteEntityType_onInvalidInput_400(String authorization, String namespace,
            String entityType) {
        webTestClient.delete()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    @ParameterizedTest
    @MethodSource("validGetAndDeleteEntityTypeArguments")
    void deleteEntityType_validInput_204(String authorization, String namespace, String entityType) {

        when(authChecker.authTenant(namespace, authorization, new String[]{AuthScope.ESS_NAMESPACE_ADMIN}))
                .thenReturn(Mono.just(true));

        when(namespaceFacade.removeEntityType(namespace, entityType)).thenReturn(Mono.empty());

        webTestClient.delete()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, authorization)
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
    @MethodSource("invalidGetAndDeleteEntityTypeArguments")
    void getEntityType_onInvalidInput_400(String authorization, String namespace,
            String entityType) {
        webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();
    }

    @ParameterizedTest
    @MethodSource("validGetAndDeleteEntityTypeArguments")
    void getEntityType_validInput_200(String authorization, String namespace, String entityType) {

        var result = EntityTypeInfo.builder()
                .name(entityType)
                .build();

        when(authChecker.authOperatorOrTenant(namespace, authorization, new String[]{AuthScope.ESS_OPERATOR},
                        new String[]{AuthScope.ESS_NAMESPACE_ADMIN}))
                .thenReturn(Mono.just(true));

        when(namespaceFacade.getEntityType(namespace, entityType))
                .thenReturn(Mono.just(result));

        webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types/" + entityType))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.OK)
                .expectBody(EntityTypeInfo.class)
                .isEqualTo(result);

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(namespace));
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
    }

    private static Stream<Arguments> invalidListEntityTypesArguments() {
        return invalidHeadersArguments();
    }


    @ParameterizedTest
    @MethodSource("invalidListEntityTypesArguments")
    void listEntityTypes_onInvalidInput_400(String authorization, String namespace) {
        webTestClient.get()
                .uri(buildUrl("/v1/sys/entity-types"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();
    }
}
