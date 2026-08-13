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
package com.nvidia.ess.auth.jwk;


import static com.nvidia.ess.config.ObservedAspectConfiguration.TRACE_ONLY_NAME;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.URL_FULL_KEY;

import com.github.benmanes.caffeine.cache.AsyncLoadingCache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.binder.cache.CaffeineStatsCounter;
import io.micrometer.observation.annotation.Observed;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientRequestException;
import reactor.core.publisher.Mono;

@Service
@Slf4j
public class JwkSetService {

    private final TelemetryComponents telemetryComponents;
    private final WebClient webClient;
    private final AsyncLoadingCache<String, JWKSet> jwkCache;

    public JwkSetService(WebClient.Builder webClientBuilder, JwkCacheProperties jwkCacheProperties, MeterRegistry meterRegistry,
                         @Qualifier(TelemetryComponentsImpl.BEAN_NAME) TelemetryComponents telemetryComponents) {
        this.telemetryComponents = telemetryComponents;
        this.webClient = webClientBuilder.build();
        var jwkCacheStats = new CaffeineStatsCounter(meterRegistry, "jwkCache");
        this.jwkCache = Caffeine.newBuilder()
                // expire after TTL
                .expireAfterWrite(jwkCacheProperties.getExpireAfterWrite())
                // https://github.com/ben-manes/caffeine/wiki/Refresh
                .refreshAfterWrite(jwkCacheProperties.getRefreshAfterWrite())
                .initialCapacity(jwkCacheProperties.getInitSize())
                .maximumSize(jwkCacheProperties.getMaxSize())
                .recordStats(() -> jwkCacheStats)
                // used for refresh only
                .buildAsync((key, executor) -> loadJWKSet(key).toFuture());
        jwkCacheStats.registerSizeMetric(this.jwkCache.synchronous());
    }

    @Observed(name = TRACE_ONLY_NAME)
    public Mono<JWKSet> getJwkSet(String jwksUrl) {
        // contextWrite must be on loadJWKSet (before toFuture), not on Mono.fromFuture.
        // Futures are eager - toFuture() calls subscribe() on the Mono internally 
        // so context must already be attached at that point for spans to connect 
        return Mono.deferContextual(contextView -> {
            telemetryComponents.setSpanAttribute(contextView, URL_FULL_KEY, jwksUrl);
            return Mono.fromFuture(jwkCache.get(jwksUrl, (key, executor) ->
                        loadJWKSet(key)
                                .contextWrite(ctx -> ctx.putAll(contextView))
                                .toFuture()
                ));
        });
    }

    public Mono<JWKSet> loadJWKSet(String jwksUrl)  {
        return webClient.get()
                .uri(jwksUrl)
                .retrieve()
                .bodyToMono(String.class)
                .onErrorResume(e -> {
                    if (e instanceof WebClientRequestException) {
                        String errMsg = String.format("failed to download public key for jwksURL %s", jwksUrl);
                        log.error(errMsg, e);
                        return Mono.error(new UnauthorizedException(errMsg, e));
                    }
                    return Mono.error(new UnauthorizedException("unknown jwks set download error: " + e.getMessage(), e));
                })
                .flatMap(jwksJson -> Mono.fromCallable(() -> {
                    try {
                        return JWKSet.parse(jwksJson);
                    } catch (Exception e) {
                        throw new UnauthorizedException("failed to parse public key after download" + e.getMessage(), e);
                    }
                }));
    }
}