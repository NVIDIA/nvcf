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
package com.nvidia.nvct.util;

import static com.nvidia.nvct.util.NvctUtils.getDetailFromProblemDetailsResponse;
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
import java.time.Duration;
import java.util.Arrays;
import java.util.List;
import java.util.concurrent.TimeUnit;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.client.reactive.ClientHttpConnector;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.security.oauth2.client.AuthorizedClientServiceReactiveOAuth2AuthorizedClientManager;
import org.springframework.security.oauth2.client.ClientAuthorizationException;
import org.springframework.security.oauth2.client.ClientCredentialsReactiveOAuth2AuthorizedClientProvider;
import org.springframework.security.oauth2.client.InMemoryReactiveOAuth2AuthorizedClientService;
import org.springframework.security.oauth2.client.endpoint.WebClientReactiveClientCredentialsTokenResponseClient;
import org.springframework.security.oauth2.client.registration.ClientRegistration;
import org.springframework.security.oauth2.client.registration.InMemoryReactiveClientRegistrationRepository;
import org.springframework.security.oauth2.client.web.reactive.function.client.ServerOAuth2AuthorizedClientExchangeFilterFunction;
import org.springframework.security.oauth2.core.AuthorizationGrantType;
import org.springframework.security.oauth2.core.OAuth2ErrorCodes;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;
import reactor.core.publisher.Mono;
import reactor.netty.http.client.HttpClient;
import reactor.netty.resources.ConnectionProvider;
import reactor.netty.resources.LoopResources;
import reactor.util.retry.Retry;

@Slf4j
@UtilityClass
public final class NvctOAuth2ClientUtils {

    private static final int CONNECT_TIMEOUT_MILLIS = 60000;
    private static final int READ_TIMEOUT = 30;
    private static final int WRITE_TIMEOUT = 30;
    private static final Duration RESPONSE_TIMEOUT_DURATION = Duration.ofSeconds(30);
    private static final Duration MAX_IDLE_TIMEOUT_DURATION = Duration.ofSeconds(60);
    private static final int MAX_CONNECTIONS_PER_POOL = 500;

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();
    private static final String MESG_4XX_RESPONSE =
            "Upstream response from '%s' with 4xx error %d";
    private static final String MESG_5XX_RESPONSE =
            "Upstream response from '%s' with 5xx error %d";
    private static final String MESG_5XX_RESPONSE_WITH_DETAIL =
            "Upstream response from '%s' with 5xx error %d - %s";

    /**
     * Creates a {@link ClientHttpConnector} along with the Reactor Netty
     * {@link ConnectionProvider} and {@link LoopResources} that back it, returned
     * together as a {@link ManagedHttpResources} so the underlying thread/connection
     * pools can be disposed when no longer needed. Without this, each
     * {@link org.springframework.cloud.context.config.annotation.RefreshScope @RefreshScope}
     * refresh would leak a pool.
     */
    public static ManagedHttpResources getClientHttpConnectorManaged(String clientRegistrationId) {
        var provider = ConnectionProvider.builder(clientRegistrationId)
                .maxConnections(MAX_CONNECTIONS_PER_POOL)
                .maxIdleTime(MAX_IDLE_TIMEOUT_DURATION)
                .build();
        var loopResources = LoopResources.create(clientRegistrationId);
        var httpClient = HttpClient.create(provider)
                .option(ChannelOption.CONNECT_TIMEOUT_MILLIS, CONNECT_TIMEOUT_MILLIS)
                .doOnConnected(connection -> connection
                        .addHandlerFirst(new ReadTimeoutHandler(READ_TIMEOUT, TimeUnit.SECONDS))
                        .addHandlerFirst(new WriteTimeoutHandler(WRITE_TIMEOUT, TimeUnit.SECONDS)))
                .responseTimeout(RESPONSE_TIMEOUT_DURATION)
                .followRedirect(true)
                .runOn(loopResources);
        httpClient.warmup().block();
        return new ManagedHttpResources(
                new ReactorClientHttpConnector(httpClient),
                provider,
                loopResources,
                clientRegistrationId);
    }

    // Returns a retry filter for both token server and resource server. Retries twice on
    // 5xx from either the resource server or token server - server_error, temporarily_unavailable,
    // or HTTP 5xx and then throws UpstreamException.
    //
    // For other ClientAuthorizationException (auth failures typically from token server), retries
    // once and throws UnauthorizedException with details if the retry fails.
    public static ExchangeFilterFunction getRetryableFilter(String upstream) {
        var svcName = upstream.toUpperCase();
        var retrySpec = Retry.backoff(2, Duration.ofMillis(200))
                .jitter(0.75)
                .doBeforeRetry(retrySignal -> log.info("Before retrying {} call", svcName))
                .doAfterRetry(retrySignal -> log.info("After retrying {} call", svcName))
                .filter(throwable -> throwable instanceof UpstreamException
                        || isTokenServer5xx(throwable))
                .onRetryExhaustedThrow((retryBackoffSpec, retrySignal) -> {
                    log.error("{} failed to process after max retries", svcName);
                    var mesg = "Failed to get response from '%s' after retries.".formatted(svcName);
                    return new UpstreamException(mesg);
                });

        return (request, next) -> next.exchange(request)
                .onErrorResume(ClientAuthorizationException.class, ex -> {
                    if (isTokenServer5xx(ex)) {
                        return Mono.error(ex);
                    }
                    log.warn("OAuth2 token fetch failed for '{}', retrying: {}", svcName,
                             ex.getMessage());
                    return next.exchange(request)
                            .onErrorResume(ClientAuthorizationException.class, retryEx -> {
                                var mesg = getClientAuthErrorMessage(svcName, retryEx);
                                return Mono.error(new UnauthorizedException(mesg, ex));
                            });
                })
                .flatMap(clientResponse -> Mono.just(clientResponse).thenReturn(clientResponse))
                .retryWhen(retrySpec);
    }

    public static ServerOAuth2AuthorizedClientExchangeFilterFunction getOAuth2ExchangeFilter(
            WebClient.Builder webClientBuilder,
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
                new InMemoryReactiveClientRegistrationRepository(clientRegistration);
        var clientService =
                new InMemoryReactiveOAuth2AuthorizedClientService(clientRegistrationRepository);

        // Use the Boot-customized builder for the token endpoint so token and resource calls
        // share the same observation convention. When the token client used a raw
        // WebClient.builder(), it registered http.client.requests with legacy tag keys while
        // resource clients registered the same metric name with Boot's current tag keys.
        // Prometheus requires every time series under the same metric name to have the same tag
        // keys, so it rejected the resource-server meters after the token-server meter existed.
        var tokenWebClient = webClientBuilder.clone()
                .build();
        var tokenResponseClient = new WebClientReactiveClientCredentialsTokenResponseClient();
        tokenResponseClient.setWebClient(tokenWebClient);

        var clientCredentialsProvider =
                new ClientCredentialsReactiveOAuth2AuthorizedClientProvider();
        clientCredentialsProvider.setAccessTokenResponseClient(tokenResponseClient);

        var reactiveClientManager =
                new AuthorizedClientServiceReactiveOAuth2AuthorizedClientManager(
                clientRegistrationRepository, clientService);
        reactiveClientManager.setAuthorizedClientProvider(clientCredentialsProvider);

        var oauth2ExchangeFilter =
                new ServerOAuth2AuthorizedClientExchangeFilterFunction(reactiveClientManager);
        oauth2ExchangeFilter.setDefaultClientRegistrationId(clientRegistrationId);
        return oauth2ExchangeFilter;
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

    private static Mono<ClientResponse> handle4xxError(
            String serviceName, ClientResponse response) {
        var status = response.statusCode();
        var errorMsg = MESG_4XX_RESPONSE.formatted(serviceName, status.value());
        log.error(errorMsg);

        return response.bodyToMono(String.class)
                .defaultIfEmpty(errorMsg)
                .flatMap(body -> {
                    var detail = getDetailFromProblemDetailsResponse(JSON_MAPPER, serviceName, body);
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

    private static Mono<ClientResponse> handle5xxError(
            String serviceName, ClientResponse response) {
        var statusValue = response.statusCode().value();
        var errorMsg = MESG_5XX_RESPONSE.formatted(serviceName, statusValue);
        log.error(errorMsg);

        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.defer(() -> {
                    log.error(errorMsg);
                    return Mono.error(new UpstreamException(errorMsg));
                }))
                .flatMap(body -> {
                    var detail = getDetailFromProblemDetailsResponse(JSON_MAPPER, serviceName, body);
                    var mesg = MESG_5XX_RESPONSE_WITH_DETAIL
                                            .formatted(serviceName, statusValue, detail);
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                });
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

    /**
     * Bundles a {@link ClientHttpConnector} with the Reactor Netty
     * {@link ConnectionProvider} and {@link LoopResources} that back it, so they
     * can be disposed together via {@link #close()}.
     *
     * <p>BLOCK_TIMEOUT must stay under {@code spring.lifecycle.timeout-per-shutdown-phase}
     * (default 30s), otherwise Spring kills the JVM before disposal finishes.
     * Ideally DISPOSE_TIMEOUT ≥ {@link #RESPONSE_TIMEOUT_DURATION} so in-flight
     * requests can finish cleanly, but this is not required.
     */
    @Slf4j
    public static final class ManagedHttpResources implements AutoCloseable {
        static final Duration QUIET_PERIOD = Duration.ofSeconds(2);
        static final Duration DISPOSE_TIMEOUT = Duration.ofSeconds(25);
        static final Duration BLOCK_TIMEOUT = DISPOSE_TIMEOUT.plusSeconds(2);

        private static final String MESG_DISPOSED_CLEANLY = "%s '%s' disposed cleanly";
        private static final String MESG_DISPOSE_TIMED_OUT =
                "%s '%s' dispose did not complete within %s — resources may be force-closed by "
                        + "Netty shutdown hooks";

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
            disposeQuietly("ConnectionProvider", connectionProvider == null ? null :
                    connectionProvider.disposeLater());
            disposeQuietly("LoopResources", loopResources == null ? null :
                    loopResources.disposeLater(QUIET_PERIOD, DISPOSE_TIMEOUT));
        }

        private void disposeQuietly(String kind, Mono<Void> disposeMono) {
            if (disposeMono == null) {
                return;
            }
            try {
                disposeMono.block(BLOCK_TIMEOUT);
                log.info(MESG_DISPOSED_CLEANLY.formatted(kind, name));
            } catch (Exception ex) {
                log.warn(MESG_DISPOSE_TIMED_OUT.formatted(kind, name, BLOCK_TIMEOUT), ex);
            }
        }
    }
}
