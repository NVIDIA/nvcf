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

import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.Mockito.mock;

import org.junit.jupiter.api.Test;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.Context;

class ReactiveRequestContextHolderTest {

    @Test
    void getExchange_shouldReturnServerWebExchangeFromContext() {
        // Arrange
        ServerWebExchange mockExchange = mock(ServerWebExchange.class);

        // Act
        Mono<ServerWebExchange> result = ReactiveRequestContextHolder.getExchange()
                .contextWrite(Context.of(ServerWebExchange.class, mockExchange));

        // Assert
        StepVerifier.create(result)
                .assertNext(exchange -> {
                    assertNotNull(exchange);
                    assertNotNull(mockExchange);
                })
                .verifyComplete();
    }

    @Test
    void getExchange_shouldThrowExceptionWhenNoExchangeInContext() {
        // Act
        Mono<ServerWebExchange> result = ReactiveRequestContextHolder.getExchange();

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof IllegalStateException || throwable instanceof java.util.NoSuchElementException)
                .verify();
    }
}
