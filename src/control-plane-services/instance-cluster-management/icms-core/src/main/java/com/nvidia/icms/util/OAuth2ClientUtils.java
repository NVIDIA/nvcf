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
package com.nvidia.icms.util;

import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.INSUFFICIENT_STORAGE;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.PAYMENT_REQUIRED;
import static org.springframework.http.HttpStatus.TOO_MANY_REQUESTS;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;
import static org.springframework.http.HttpStatus.UNPROCESSABLE_CONTENT;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.PaymentRequiredException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import com.nvidia.boot.exceptions.UpstreamException;
import io.netty.channel.ChannelOption;
import io.netty.handler.timeout.ReadTimeoutHandler;
import io.netty.handler.timeout.WriteTimeoutHandler;
import java.io.IOException;
import java.time.Duration;
import java.util.Arrays;
import java.util.List;
import java.util.concurrent.TimeUnit;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.ProblemDetail;
import org.springframework.http.client.reactive.ClientHttpConnector;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.security.oauth2.client.AuthorizedClientServiceOAuth2AuthorizedClientManager;
import org.springframework.security.oauth2.client.ClientAuthorizationException;
import org.springframework.security.oauth2.client.InMemoryOAuth2AuthorizedClientService;
import org.springframework.security.oauth2.client.registration.ClientRegistration;
import org.springframework.security.oauth2.client.registration.InMemoryClientRegistrationRepository;
import org.springframework.security.oauth2.client.web.reactive.function.client.ServletOAuth2AuthorizedClientExchangeFilterFunction;
import org.springframework.security.oauth2.core.AuthorizationGrantType;
import org.springframework.security.oauth2.core.OAuth2ErrorCodes;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClientResponseException;
import reactor.core.publisher.Mono;
import reactor.netty.http.client.HttpClient;
import reactor.netty.resources.ConnectionProvider;
import reactor.netty.resources.LoopResources;
import reactor.util.retry.Retry;

@Slf4j
@UtilityClass
public final class OAuth2ClientUtils {

    public static final int CONNECT_TIMEOUT_MILLIS = 60000;
    public static final int READ_TIMEOUT_SECONDS = 30;
    public static final int WRITE_TIMEOUT_SECONDS = 30;
    public static final Duration RESPONSE_TIMEOUT = Duration.ofSeconds(30);
    public static final Duration MAX_IDLE_TIME = Duration.ofSeconds(60);
    public static final int MAX_CONNECTIONS = 500;

    private static final String MISSING_PROBLEM_DETAILS_RESPONSE =
            "Missing ProblemDetails response from %s";
    private static final String INVALID_PROBLEM_DETAILS_RESPONSE =
            "Invalid ProblemDetails response from %s - '%s'";

    private static final JsonMapper objectMapper = new JsonMapper();
    private static final String MESG_4XX_RESPONSE =
            "Upstream response with 4xx error %d";
    private static final String MESG_5XX_RESPONSE =
            "Upstream response with 5xx error %d";
    private static final String MESG_5XX_RESPONSE_WITH_DETAIL =
            "Upstream response with 5xx error %d - %s";

    /**
     * Creates a {@link ClientHttpConnector} along with the Reactor Netty
     * {@link ConnectionProvider} and {@link LoopResources} that back it, returned
     * together as a {@link ManagedHttpResources}. Intended to be exposed as a
     * singleton {@link org.springframework.context.annotation.Bean @Bean} with
     * {@code destroyMethod = "close"} and injected into
     * {@link org.springframework.cloud.context.config.annotation.RefreshScope @RefreshScope}
     * outbound clients, so the underlying pools live for the JVM lifetime and are
     * disposed exactly once at context shutdown (rather than churned on every refresh).
     */
    public static ManagedHttpResources getClientHttpConnectorManaged(String clientRegistrationId) {
        var provider = ConnectionProvider.builder(clientRegistrationId)
                .maxConnections(MAX_CONNECTIONS)
                .maxIdleTime(MAX_IDLE_TIME)
                .build();
        var loopResources = LoopResources.create(clientRegistrationId);
        var httpClient = HttpClient.create(provider)
                .option(ChannelOption.CONNECT_TIMEOUT_MILLIS, CONNECT_TIMEOUT_MILLIS)
                .doOnConnected(connection -> connection
                        .addHandlerFirst(new ReadTimeoutHandler(READ_TIMEOUT_SECONDS, TimeUnit.SECONDS))
                        .addHandlerFirst(new WriteTimeoutHandler(WRITE_TIMEOUT_SECONDS, TimeUnit.SECONDS)))
                .responseTimeout(RESPONSE_TIMEOUT)
                .followRedirect(true)
                .runOn(loopResources);
        // Note: warmup() is intentionally blocking; call only during synchronous startup
        httpClient.warmup().block();
        return new ManagedHttpResources(
                new ReactorClientHttpConnector(httpClient),
                provider,
                loopResources,
                clientRegistrationId);
    }

    /**
     * Bundles a {@link ClientHttpConnector} with the Reactor Netty
     * {@link ConnectionProvider} and {@link LoopResources} that back it.
     * Exposed as a singleton {@link org.springframework.context.annotation.Bean @Bean}
     * with {@code destroyMethod = "close"}, so Spring invokes {@link #close()} exactly
     * once at context shutdown (the {@link AutoCloseable} contract).
     *
     * <p>Disposal uses a graceful two-arg {@code disposeLater(quietPeriod, timeout)}
     * for {@link LoopResources}:
     * <ul>
     *   <li><b>quietPeriod</b>: a short window with no new borrows before the pool
     *       starts closing.</li>
     *   <li><b>timeout</b>: hard cap aligned with
     *       {@link OAuth2ClientUtils#RESPONSE_TIMEOUT} (30s) so in-flight upstream
     *       requests have a chance to finish within their own response timeout before
     *       the pool is force-closed. Kept under Spring's default
     *       {@code spring.lifecycle.timeout-per-shutdown-phase} (30s) with a small
     *       buffer for Spring's own shutdown housekeeping.</li>
     * </ul>
     *
     * <p>{@link ConnectionProvider#disposeLater()} in reactor-netty-core has no
     * {@code (quietPeriod, timeout)} overload, so the {@link #BLOCK_TIMEOUT} cap on
     * {@link reactor.core.publisher.Mono#block(java.time.Duration) block()} is the
     * sole hard bound for that mono.
     */
    @Slf4j
    public static final class ManagedHttpResources implements AutoCloseable {

        // Derivation:
        //   DISPOSE_TIMEOUT ≈ RESPONSE_TIMEOUT − small_buffer,
        //   clamped below spring.lifecycle.timeout-per-shutdown-phase (30s).
        //   Increase if RESPONSE_TIMEOUT/READ_TIMEOUT_SECONDS is raised above 30s.
        static final Duration QUIET_PERIOD = Duration.ofSeconds(2);
        static final Duration DISPOSE_TIMEOUT = Duration.ofSeconds(25);
        static final Duration BLOCK_TIMEOUT = DISPOSE_TIMEOUT.plusSeconds(2);

        private final ClientHttpConnector connector;
        private final ConnectionProvider connectionProvider;
        private final LoopResources loopResources;
        private final String name;

        public ManagedHttpResources(
                ClientHttpConnector connector,
                ConnectionProvider connectionProvider,
                LoopResources loopResources,
                String name) {
            this.connector = connector;
            this.connectionProvider = connectionProvider;
            this.loopResources = loopResources;
            this.name = name;
        }

        public ClientHttpConnector connector() {
            return connector;
        }

        @Override
        public void close() {
            // ConnectionProvider.disposeLater() has no quietPeriod/timeout overload in
            // reactor-netty-core; the block() timeout is the sole hard cap.
            disposeQuietly("ConnectionProvider", connectionProvider == null ? null :
                    connectionProvider.disposeLater());
            // LoopResources.disposeLater(quietPeriod, timeout) supports graceful
            // EventLoopGroup shutdown (Netty semantics).
            disposeQuietly("LoopResources", loopResources == null ? null :
                    loopResources.disposeLater(QUIET_PERIOD, DISPOSE_TIMEOUT));
        }

        private void disposeQuietly(String kind, Mono<Void> disposeMono) {
            if (disposeMono == null) {
                return;
            }
            try {
                disposeMono.block(BLOCK_TIMEOUT);
                log.info("{} '{}' disposed cleanly", kind, name);
            } catch (Exception ex) {
                log.warn("{} '{}' dispose did not complete within {} — resources may be "
                                + "force-closed by Netty shutdown hooks",
                        kind, name, BLOCK_TIMEOUT, ex);
            }
        }
    }

    public static ServletOAuth2AuthorizedClientExchangeFilterFunction getOauth2ExchangeFilter(
            String clientRegistrationId,
            String tokenUri,
            String clientId,
            String clientSecret,
            String scope) {
        var scopes = StringUtils.isBlank(scope) ? List.<String>of() :
                Arrays.stream(scope.split(",")).map(String::trim).toList();
        var clientRegistration = ClientRegistration.withRegistrationId(clientRegistrationId)
                .authorizationGrantType(AuthorizationGrantType.CLIENT_CREDENTIALS)
                .clientId(clientId)
                .clientSecret(clientSecret)
                .scope(scopes)
                .tokenUri(tokenUri)
                .build();
        var clientRegistrationRepository =
                new InMemoryClientRegistrationRepository(clientRegistration);
        var clientService =
                new InMemoryOAuth2AuthorizedClientService(clientRegistrationRepository);
        var authorizedClientManager =
                new AuthorizedClientServiceOAuth2AuthorizedClientManager(
                        clientRegistrationRepository,
                        clientService);
        var oauth2ExchangeFilter =
                new ServletOAuth2AuthorizedClientExchangeFilterFunction(authorizedClientManager);
        oauth2ExchangeFilter.setDefaultClientRegistrationId(clientRegistrationId);
        return oauth2ExchangeFilter;
    }

    // Returns a retry filter for both token server and resource server. Retries twice on
    // 5xx from either the resource server or token server - server_error, temporarily_unavailable,
    // or HTTP 5xx and then throws UpstreamException.
    //
    // For other ClientAuthorizationException (auth failures typically from token server), retries
    // once and throws UnauthorizedException with details if the retry fails.
    public static ExchangeFilterFunction getRetryableFilter(String upstream) {
        var retrySpec = Retry.backoff(2, Duration.ofMillis(200))
                .jitter(0.75)
                .doBeforeRetry(retrySignal -> log.info("Before retrying {} call", upstream))
                .doAfterRetry(retrySignal -> log.info("After retrying {} call", upstream))
                .filter(throwable -> throwable instanceof UpstreamException
                        || throwable instanceof IOException
                        || isTokenServer5xx(throwable))
                .onRetryExhaustedThrow((retryBackoffSpec, retrySignal) -> {
                    log.error("{} failed to process after max retries", upstream);
                    return new RuntimeException(
                            "Failed to get response from %s after retries.".formatted(upstream));
                });

        return (request, next) -> next.exchange(request)
                .onErrorResume(ClientAuthorizationException.class, ex -> {
                    if (isTokenServer5xx(ex)) {
                        return Mono.error(ex);
                    }
                    log.warn("OAuth2 token fetch failed for {}, retrying: {}", upstream,
                             ex.getMessage());
                    return next.exchange(request)
                            .onErrorResume(ClientAuthorizationException.class, retryEx -> {
                                var mesg = getClientAuthErrorMessage(upstream, retryEx);
                                return Mono.error(new UnauthorizedException(mesg, retryEx));
                            });
                })
                .retryWhen(retrySpec);
    }

    private static boolean isTokenServer5xx(Throwable throwable) {
        if (!(throwable instanceof ClientAuthorizationException ex)) {
            return false;
        }
        if (ex.getError() != null) {
            var code = ex.getError().getErrorCode();
            if (OAuth2ErrorCodes.SERVER_ERROR.equals(code)
                    || OAuth2ErrorCodes.TEMPORARILY_UNAVAILABLE.equals(code)) {
                return true;
            }
        }
        var cause = ex.getCause();
        if (cause instanceof WebClientResponseException wce) {
            return wce.getStatusCode().is5xxServerError();
        }
        return false;
    }

    private static String getClientAuthErrorMessage(
            String upstream,
            ClientAuthorizationException ex) {
        var mesg = upstream + " authentication failed: " + ex.getMessage();
        if (ex.getError() != null) {
            mesg += " [OAuth2 error: " + ex.getError().getErrorCode();
            if (ex.getError().getDescription() != null) {
                mesg += " - " + ex.getError().getDescription();
            }
            mesg += "]";
        }
        return mesg;
    }

    public static ExchangeFilterFunction getResponseFilterProcessor(String upstream) {
        return ExchangeFilterFunction.ofResponseProcessor(response -> {
            if (response.statusCode().is5xxServerError()) {
                return handle5xxError(upstream, response);
            }
            if (response.statusCode().is4xxClientError()) {
                return handle4xxError(upstream, response);
            }
            return Mono.just(response);
        });
    }

    public static Mono<ClientResponse> handle4xxError(
            String serviceName, ClientResponse response) {
        var status = response.statusCode();
        var errorMsg = MESG_4XX_RESPONSE.formatted(status.value());
        log.info(errorMsg);

        return response.bodyToMono(String.class)
                .defaultIfEmpty(errorMsg)
                .flatMap(body -> {
                    var detail = getDetailFromProblemDetailsResponse(serviceName, body);
                    if (status.isSameCodeAs(UNAUTHORIZED)) {
                        return Mono.error(new UnauthorizedException(detail));
                    }
                    if (status.isSameCodeAs(PAYMENT_REQUIRED)) {
                        return Mono.error(new PaymentRequiredException(detail));
                    }
                    if (status.isSameCodeAs(FORBIDDEN)) {
                        return Mono.error(new ForbiddenException(detail));
                    }
                    if (status.isSameCodeAs(NOT_FOUND)) {
                        return Mono.error(new NotFoundException(detail));
                    }
                    if (status.isSameCodeAs(CONFLICT)) {
                        return Mono.error(new ConflictException(detail));
                    }
                    if (status.isSameCodeAs(TOO_MANY_REQUESTS)) {
                        return Mono.error(new TooManyRequestsException(detail));
                    }
                    if (status.isSameCodeAs(INSUFFICIENT_STORAGE)) {
                        return Mono.error(new UpstreamException(detail));
                    }
                    if (status.isSameCodeAs(UNPROCESSABLE_CONTENT)) {
                        return Mono.error(new UnprocessableEntityException(detail));
                    }
                    if (status.isSameCodeAs(BAD_REQUEST)) {
                        return Mono.error(new BadRequestException(detail));
                    }
                    return Mono.error(new UpstreamException(detail));
                });
    }

    public static Mono<ClientResponse> handle5xxError(
            String serviceName, ClientResponse response) {
        var statusValue = response.statusCode().value();
        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.defer(() -> {
                    var mesg = MESG_5XX_RESPONSE.formatted(statusValue);
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                }))
                .flatMap(body -> {
                    var detail = getDetailFromProblemDetailsResponse(serviceName, body);
                    var mesg = MESG_5XX_RESPONSE_WITH_DETAIL.formatted(
                            statusValue, detail);
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                });
    }

    private static String getDetailFromProblemDetailsResponse(
            String service,
            String body) {
        if (StringUtils.isBlank(body)) {
            return MISSING_PROBLEM_DETAILS_RESPONSE.formatted(service);
        }

        try {
            var pd = objectMapper.readValue(body, ProblemDetail.class);
            return ((pd != null) && StringUtils.isNotBlank(pd.getDetail())) ? pd.getDetail() : body;
        } catch (Exception ex) {
            log.warn(INVALID_PROBLEM_DETAILS_RESPONSE.formatted(service, ex.getMessage()));
            return body;
        }
    }
}
