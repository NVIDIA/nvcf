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
package com.nvidia.ess.filter;

import static com.nvidia.ess.constants.Constants.X_ESS_AGENT_ID_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.AGENT_ID_KEY;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.ess.telemetry.TelemetryComponents;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.mock.http.server.reactive.MockServerHttpRequest;
import org.springframework.mock.web.server.MockServerWebExchange;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class EssAgentIdWebFilterTest {

    @InjectMocks
    private EssAgentIdWebFilter filter;

    @Mock
    private TelemetryComponents telemetryComponents;

    @Mock
    private WebFilterChain chain;

    @BeforeEach
    void setUp() {
        when(chain.filter(any()))
                .thenReturn(Mono.empty());
    }

    public static Stream<Arguments> agentIdArguments() {
        return Stream.of(
                Arguments.of(""),
                Arguments.of((Object) null),
                Arguments.of(UUID.randomUUID().toString())
        );
    }

    @ParameterizedTest
    @MethodSource("agentIdArguments")
    void filter_onAgentIdRequestHeader_shouldPopulateResponseHeader(String agentId) {
        var request = MockServerHttpRequest.get("/some-endpoint")
                .header(X_ESS_AGENT_ID_HEADER, agentId)
                .build();
        var exchange = MockServerWebExchange.builder(request)
                .build();

        StepVerifier.create(filter.filter(exchange, chain))
                .verifyComplete();
        Assertions.assertEquals(agentId, exchange.getResponse().getHeaders().getFirst(X_ESS_AGENT_ID_HEADER));

        if (!Objects.isNull(agentId)) {
            verify(telemetryComponents).setSpanAttribute(any(ServerWebExchange.class), eq(AGENT_ID_KEY), eq(agentId));
            verifyNoMoreInteractions(telemetryComponents);
        } else {
            verifyNoInteractions(telemetryComponents);
        }
    }
}