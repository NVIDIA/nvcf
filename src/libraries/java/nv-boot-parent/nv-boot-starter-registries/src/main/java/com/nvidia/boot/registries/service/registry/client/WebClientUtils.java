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

package com.nvidia.boot.registries.service.registry.client;

import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.TOO_MANY_REQUESTS;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import io.netty.channel.ChannelOption;
import io.netty.handler.timeout.WriteTimeoutHandler;
import java.io.IOException;
import java.net.URI;
import java.time.Duration;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientRequestException;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;
import reactor.core.Exceptions;
import reactor.core.publisher.Mono;
import reactor.netty.http.client.HttpClient;
import reactor.util.retry.Retry;

/**
 * Shared WebClient utilities for registry clients. Provides standard WebClient
 * construction with boot-exception error handlers and HttpServiceProxyFactory
 * stub creation.
 */
@Slf4j
@UtilityClass
public class WebClientUtils {

    private static final String MESG_4XX_RESPONSE =
            "Registry response with 4xx error %d";
    private static final String MESG_5XX_RESPONSE =
            "Registry response with 5xx error %d";
    private static final String MESG_RETRY_NOT_SUCCESSFUL =
            "Retryable request {} is not successful - {}: {}";
    private static final String MESG_RETRY_FAILED =
            "Retryable request retries failed";
    private static final String MESG_429_RETRY =
            "{} responded with 429 (rate limited) at {}, retrying (attempt {})";

    private static final Duration MIN_BACKOFF_429 = Duration.ofSeconds(1);

    /**
     * Creates a {@link WebClient.Builder} for tests and manual wiring (Jackson 3 default codecs).
     */
    public static WebClient.Builder builder() {
        return WebClient.builder();
    }

    /**
     * Creates a WebClient with standard boot-exception status handlers and
     * a timeout filter using an injected builder.
     */
    public static WebClient createWebClient(WebClient.Builder webClientBuilder,
                                            String baseUrl,
                                            Duration timeout) {
        return webClientBuilder
                .baseUrl(baseUrl)
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      WebClientUtils::handle4xxError)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError,
                                      WebClientUtils::handle5xxError)
                .filter((request, next) -> next.exchange(request).timeout(timeout))
                .build();
    }

    /**
     * Creates a WebClient with granular Reactor Netty timeout configuration and
     * optional retry on 5xx / IO errors. Each retry attempt gets its own
     * per-attempt timeout (exchangeTimeout). 4xx errors are never retried.
     *
     * @param webClientBuilder injected WebClient builder
     * @param baseUrl         base URL for all requests
     * @param exchangeTimeout end-to-end deadline for the entire HTTP exchange
     *                        (connect + write + server processing + read);
     *                        applied via Reactor {@code .timeout()} operator
     * @param connectTimeout  max time to establish a TCP connection;
     *                        applied via Netty {@code CONNECT_TIMEOUT_MILLIS}
     * @param responseTimeout max idle duration between network-level read
     *                        operations while reading a response;
     *                        applied via {@link HttpClient#responseTimeout}
     * @param writeTimeout    max time for an individual write operation to
     *                        complete; applied via Netty {@code WriteTimeoutHandler}
     * @param maxTries        total number of attempts including the initial request
     *                        (e.g. 3 = 1 initial + 2 retries; 0 or 1 = no retry)
     */
    public static WebClient createWebClient(
            WebClient.Builder webClientBuilder,
            String baseUrl,
            Duration exchangeTimeout,
            Duration connectTimeout,
            Duration responseTimeout,
            Duration writeTimeout,
            int maxTries) {
        var httpClient = HttpClient.create()
                .option(ChannelOption.CONNECT_TIMEOUT_MILLIS, (int) connectTimeout.toMillis())
                .responseTimeout(responseTimeout)
                .doOnConnected(conn -> conn.addHandlerLast(
                        new WriteTimeoutHandler(writeTimeout.toSeconds(), TimeUnit.SECONDS)));

        var builder = webClientBuilder
                .baseUrl(baseUrl)
                .clientConnector(new ReactorClientHttpConnector(httpClient))
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      WebClientUtils::handle4xxError)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError,
                                      WebClientUtils::handle5xxError);

        if (maxTries > 1) {
            builder.filter(retryOn5xxAndIoError(maxTries));
            builder.filter(retryOn429WithBackoff(maxTries, URI.create(baseUrl).getHost()));
        }

        return builder
                .filter((request, next) -> next.exchange(request).timeout(exchangeTimeout))
                .build();
    }

    /**
     * Creates a typed HTTP service proxy backed by the given WebClient.
     */
    public static <S> S createStubService(WebClient webClient, Class<S> serviceType) {
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        return factory.createClient(serviceType);
    }

    /**
     * Retries up to {@code maxTries} total attempts on 5xx and IO/timeout errors.
     * <ul>
     *   <li>On the last attempt, a 5xx response passes through to the status handler
     *       (so the response body is included in the exception)</li>
     *   <li>If all attempts fail with IO/timeout errors (no response),
     *       throws {@link UpstreamException} with "retryable request retries failed"</li>
     * </ul>
     */
    private static ExchangeFilterFunction retryOn5xxAndIoError(int maxTries) {
        return (request, next) -> {
            var attempts = new AtomicInteger(0);
            return next.exchange(request)
                    .flatMap(response -> {
                        int attempt = attempts.incrementAndGet();
                        if (response.statusCode().is5xxServerError() && attempt < maxTries) {
                            return response.releaseBody()
                                    .then(Mono.<ClientResponse>error(
                                            new ServerRetryException(
                                                    response.statusCode().value())));
                        }
                        return Mono.just(response);
                    })
                    .retryWhen(Retry.max((long) maxTries - 1)
                                       .filter(WebClientUtils::isRetryable)
                                       .doBeforeRetry(signal -> log.warn(
                                               MESG_RETRY_NOT_SUCCESSFUL,
                                               request.url(),
                                               signal.totalRetries() + 1,
                                               signal.failure().getMessage())))
                    .onErrorMap(Exceptions::isRetryExhausted,
                                ex -> new UpstreamException(MESG_RETRY_FAILED));
        };
    }

    private static boolean isRetryable(Throwable t) {
        return t instanceof ServerRetryException
                || t instanceof IOException
                || t instanceof TimeoutException
                || t instanceof WebClientRequestException;
    }

    private static class ServerRetryException extends RuntimeException {

        ServerRetryException(int statusCode) {
            super("Server error " + statusCode);
        }
    }

    /**
     * Retries up to {@code maxTries} total attempts when NGC responds with 429 Too Many Requests.
     * Uses exponential backoff with jitter so retries respect rate-limiting semantics.
     * Logs an INFO message before each retry attempt. On the final attempt, a 429 response
     * passes through to the status handler, which converts it to {@link TooManyRequestsException}.
     */
    private static ExchangeFilterFunction retryOn429WithBackoff(int maxTries, String hostname) {
        return (request, next) -> {
            var attempts = new AtomicInteger(0);
            return next.exchange(request)
                    .flatMap(response -> {
                        int attempt = attempts.incrementAndGet();
                        var statusCode = response.statusCode();
                        if (statusCode.isSameCodeAs(TOO_MANY_REQUESTS) && attempt < maxTries) {
                            return response.releaseBody()
                                    .then(Mono.error(new RateLimitRetryException()));
                        }
                        return Mono.just(response);
                    })
                    .retryWhen(Retry.backoff((long) maxTries - 1, MIN_BACKOFF_429)
                                       .jitter(0.5)
                                       .filter(RateLimitRetryException.class::isInstance)
                                       .doBeforeRetry(signal -> log.info(
                                               MESG_429_RETRY,
                                               hostname,
                                               request.url(),
                                               signal.totalRetries() + 1)));
        };
    }

    private static class RateLimitRetryException extends RuntimeException {

        RateLimitRetryException() {
            super("Rate limited (429)");
        }
    }

    private static Mono<BootResponseException> handle4xxError(ClientResponse response) {
        var status = response.statusCode();
        log.error(MESG_4XX_RESPONSE.formatted(response.statusCode().value()));

        if (status.isSameCodeAs(UNAUTHORIZED)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Unauthorized")
                    .flatMap(body -> Mono.error(new UnauthorizedException(body)));
        }
        if (status.isSameCodeAs(FORBIDDEN)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Forbidden")
                    .flatMap(body -> Mono.error(new ForbiddenException(body)));
        }
        if (status.isSameCodeAs(NOT_FOUND)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Not Found")
                    .flatMap(body -> Mono.error(new NotFoundException(body)));
        }
        if (status.isSameCodeAs(TOO_MANY_REQUESTS)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Too Many Requests")
                    .flatMap(body -> Mono.error(new TooManyRequestsException(body)));
        }
        if (status.isSameCodeAs(CONFLICT)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Conflict")
                    .flatMap(body -> Mono.error(new ConflictException(body)));
        }
        return response.bodyToMono(String.class)
                .defaultIfEmpty("Bad Request")
                .flatMap(body -> Mono.error(new BadRequestException(body)));
    }

    private static Mono<BootResponseException> handle5xxError(ClientResponse response) {
        var errorMsg = MESG_5XX_RESPONSE.formatted(response.statusCode().value());
        log.error(errorMsg);
        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.error(new UpstreamException(errorMsg)))
                .flatMap(body -> Mono.error(new UpstreamException(errorMsg + " - " + body)));
    }
}
