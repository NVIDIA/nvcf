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

import static com.nvidia.ess.constants.OpenTelemetryAttributes.NAMESPACE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.REQUEST_ID_KEY;
import static com.nvidia.ess.util.ControllerTestCases.buildUrl;
import static com.nvidia.ess.util.ControllerTestCases.invalidHeadersArguments;
import static com.nvidia.ess.util.ControllerTestCases.invalidNamespaceOrEntityTypeOrEntityNameArguments;
import static com.nvidia.ess.util.ControllerTestCases.validNamespaceOrEntityTypeOrEntityNameArguments;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.inOrder;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.config.BeanConfig;
import com.nvidia.ess.config.SecurityConfiguration;
import com.nvidia.ess.config.WebConfiguration;
import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.controller.request.CreateNamespaceRequest;
import com.nvidia.ess.controller.response.NamespaceInfo;
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
import java.time.Instant;
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
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
@ContextConfiguration(classes = {NamespaceController.class, CustomExceptionHandler.class,
        SecurityConfiguration.class,
        NamespaceHeaderWebFilter.class, UriPathValidationWebFilter.class,
        NotBlankAndUriSafeValidationHelper.class, CustomExceptionHandler.class,
        BeanConfig.class, RequestIdWebFilter.class,
        WebClientAutoConfiguration.class, LoggingProperties.class,
        WebConfiguration.class})
@WebFluxTest(value = NamespaceController.class, properties = {
        "management.tracing.enabled=false",
        "nv-boot.reloadable-properties.enabled=false",
        "spring.profiles.active=dummy",
        "spring.application.name=ess-core-test",
        "spring.application.version=0.0.1-test"})
@AutoConfigureWebTestClient(timeout = "60000")
class NamespaceControllerTest {

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

    private static Stream<Arguments> invalidCreateNamespaceArguments() {
        return
            Stream.concat(
                invalidHeadersArguments().map(
                    arguments -> Arguments.of(arguments.get()[0], buildCreateRequest(
                            (String) arguments.get()[1]))),
                invalidNamespaceOrEntityTypeOrEntityNameArguments().map(
                    invalidNamespaceName -> Arguments.of("auth header", buildCreateRequest(invalidNamespaceName))
                )
            );
    }

    private static Stream<Arguments> validCreateNamespaceArguments() {
        return validNamespaceOrEntityTypeOrEntityNameArguments().map(
            validNamespaceName -> Arguments.of("auth header", buildCreateRequest(validNamespaceName))
        );
    }

    static CreateNamespaceRequest buildCreateRequest(String namespace) {
        return CreateNamespaceRequest.builder()
                .namespace(namespace)
                .build();
    }

    @ParameterizedTest
    @MethodSource("invalidCreateNamespaceArguments")
    void createNamespace_onInvalidInput_400(String authorization, CreateNamespaceRequest request) {
        webTestClient.post()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
        verify(telemetryComponents)
                .recordExceptionWithoutErrorStatus(any(ServerWebExchange.class), any(Exception.class));
    }

    @ParameterizedTest
    @MethodSource("validCreateNamespaceArguments")
    void createNamespace_onValidInput_200(String authorization, CreateNamespaceRequest request) {

        when(authChecker.authOperator(eq(authorization), any())).thenReturn(Mono.just(true));

        when(namespaceFacade.createNamespace(request))
                .thenReturn(
                    Mono.just(NamespaceInfo.builder()
                            .namespace(request.getNamespace())
                            .createdAt(Instant.now())
                            .updatedAt(Instant.now())
                            .build())
                );

        webTestClient.post()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(NamespaceInfo.class)
                .returnResult();

        var invocationOrder = inOrder(telemetryComponents);

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(request.getNamespace()));
    }

    private static Stream<Arguments> invalidGetAndDeleteNamespaceArguments() {
        return Stream.concat(
            invalidHeadersArguments()
                .filter(arguments -> arguments.get()[1] instanceof String s && StringUtils.isNotBlank(s)),
            invalidNamespaceOrEntityTypeOrEntityNameArguments()
                .filter(invalidNsName -> !StringUtils.isBlank(invalidNsName))
                .map(invalidNsName -> Arguments.of("auth header",
                        URLEncoder.encode(invalidNsName, Charset.defaultCharset())))
        );
    }


    @ParameterizedTest
    @MethodSource("invalidGetAndDeleteNamespaceArguments")
    void deleteNamespace_onInvalidInput_400(String authorization, String namespace) {
        webTestClient.delete()
                .uri(buildUrl("/v1/sys/namespaces/" + namespace))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();

        if (StringUtils.isBlank(authorization)) {
            // Bad auth-header.
            verify(telemetryComponents)
                    .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents)
                    .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
            verify(telemetryComponents)
                    .recordExceptionWithoutErrorStatus(any(ServerWebExchange.class), any(Exception.class));
        }
    }

    @Test
    void deleteNamespace_onSuccess_200() {
        when(authChecker.authOperator(anyString(), any()))
                .thenReturn(Mono.just(true));
        when(namespaceFacade.removeNamespace(anyString()))
                .thenReturn(Mono.empty());
        webTestClient.delete()
                .uri(buildUrl("/v1/sys/namespaces/" + TEST_NAMESPACE))
                .header(HttpHeaders.AUTHORIZATION, "some auth")
                .exchange()
                .expectStatus()
                .isNoContent();

        var invocationOrder = inOrder(telemetryComponents);

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
    }

    @ParameterizedTest
    @MethodSource("invalidGetAndDeleteNamespaceArguments")
    void getNamespace_onInvalidInput_400(String authorization, String namespace) {
        webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces/" + namespace))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();

        if (StringUtils.isBlank(authorization)) {
            // Bad auth-header.
            verify(telemetryComponents)
                    .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents)
                    .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
            verify(telemetryComponents)
                    .recordExceptionWithoutErrorStatus(any(ServerWebExchange.class), any(Exception.class));
        }
    }


    @Test
    void getNamespace_onSuccess_200() {
        when(authChecker.authOperator(anyString(), any()))
                .thenReturn(Mono.just(true));
        when(namespaceFacade.getNamespace(anyString()))
                .thenReturn(Mono.just(mock(NamespaceInfo.class)));
        webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces/" + TEST_NAMESPACE))
                .header(HttpHeaders.AUTHORIZATION, "some auth")
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(NamespaceInfo.class)
                .returnResult();

        var invocationOrder = inOrder(telemetryComponents);

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
        invocationOrder.verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
    }

    private static Stream<Arguments> invalidListNamespacesArguments() {
        return invalidHeadersArguments().map(arguments -> Arguments.of(arguments.get()[0]))
                .filter(arguments -> !(arguments.get()[0] instanceof String s && StringUtils.isNotBlank(s)));
    }


    @ParameterizedTest
    @MethodSource("invalidListNamespacesArguments")
    void listNamespaces_onInvalidInput_400(String authorization) {
        webTestClient.get()
                .uri(buildUrl("/v1/sys/namespaces"))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents)
                .setSpanAttribute(any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq("UNKNOWN"));
        verify(telemetryComponents)
                .recordExceptionWithoutErrorStatus(any(ServerWebExchange.class), any(Exception.class));
    }
}
