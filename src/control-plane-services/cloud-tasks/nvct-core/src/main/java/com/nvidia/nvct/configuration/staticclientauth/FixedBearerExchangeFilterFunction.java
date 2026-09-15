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
package com.nvidia.nvct.configuration.staticclientauth;

import jakarta.annotation.Nonnull;
import java.util.function.Supplier;
import org.springframework.web.reactive.function.client.ClientRequest;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.ExchangeFunction;
import reactor.core.publisher.Mono;


public record FixedBearerExchangeFilterFunction(Supplier<String> tokenSupplier) implements
        ExchangeFilterFunction {

    @Nonnull
    @Override
    public Mono<ClientResponse> filter(@Nonnull ClientRequest request, ExchangeFunction next) {
        return next.exchange(ClientRequest.from(request)
                                     .headers(headers -> headers.setBearerAuth(tokenSupplier.get()))
                                     .build());

    }
}
