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

package com.nvidia.boot.telemetry.client;

import io.netty.channel.ChannelOption;
import java.time.Duration;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.client.reactive.ClientHttpConnector;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.netty.http.client.HttpClient;

/**
 * Factory for creating WebClient instances for telemetry operations.
 */
public final class TelemetryWebClientFactory {

    private TelemetryWebClientFactory() {
    }

    /**
     * Creates a WebClient for sending telemetry requests with OAuth2 bearer token filter.
     */
    public static WebClient createTelemetryWebClient(
            WebClient.Builder webClientBuilder,
            String baseUrl,
            Duration connectTimeout,
            Duration readTimeout,
            ExchangeFilterFunction oauth2BearerFilter
    ) {
        var normalized = baseUrl.replaceAll("/$", "");
        return webClientBuilder
                .clientConnector(clientConnector(connectTimeout))
                .baseUrl(normalized)
                .filter(oauth2BearerFilter)
                .defaultStatusHandler(
                        HttpStatusCode::is4xxClientError,
                        TelemetryWebClientFactory::handle4xx)
                .defaultStatusHandler(
                        HttpStatusCode::is5xxServerError,
                        TelemetryWebClientFactory::handle5xx)
                .filter((req, next) -> next.exchange(req).timeout(readTimeout))
                .build();
    }

    /**
     * Creates a WebClient for OAuth2 token requests (no base URL; use full URI per request).
     */
    public static WebClient createTokenWebClient(
            WebClient.Builder webclientBuilder,
            Duration connectTimeout,
            Duration readTimeout) {
        return webclientBuilder
                .clientConnector(clientConnector(connectTimeout))
                .defaultStatusHandler(
                        HttpStatusCode::is4xxClientError,
                        TelemetryWebClientFactory::handle4xx)
                .defaultStatusHandler(
                        HttpStatusCode::is5xxServerError,
                        TelemetryWebClientFactory::handle5xx)
                .filter((req, next) -> next.exchange(req).timeout(readTimeout))
                .build();
    }

    /**
     * TCP connect timeout for Reactor Netty. {@link WebClient} has no direct connect-timeout API;
     * it is set on the underlying {@link HttpClient}.
     */
    private static ClientHttpConnector clientConnector(Duration connectTimeout) {
        var millis = connectTimeout.toMillis();
        var connectMs = (int) Math.min(Math.max(millis, 1L), Integer.MAX_VALUE);
        var httpClient = HttpClient.create()
                .option(ChannelOption.CONNECT_TIMEOUT_MILLIS, connectMs);
        return new ReactorClientHttpConnector(httpClient);
    }

    private static Mono<IllegalStateException> handle4xx(ClientResponse response) {
        return response.bodyToMono(String.class)
                .defaultIfEmpty("")
                .flatMap(body -> {
                    var msg = "Request failed (status: %d) - %s".formatted(
                            response.statusCode().value(), body);
                    return Mono.error(new IllegalStateException(msg));
                });
    }

    private static Mono<IllegalStateException> handle5xx(ClientResponse response) {
        return response.bodyToMono(String.class)
                .defaultIfEmpty("")
                .flatMap(body -> {
                    var msg = "Server error (status: %d) - %s".formatted(
                            response.statusCode().value(), body);
                    return Mono.error(new IllegalStateException(msg));
                });
    }
}
