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
package com.nvidia.ess.startup;

import static com.nvidia.ess.config.ObservedAspectConfiguration.TRACE_ONLY_NAME;
import static java.lang.String.format;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.core.warmup.BootWarmupBase;
import com.nvidia.ess.auth.jwk.JwkCacheProperties;
import com.nvidia.ess.auth.jwk.JwkSetService;
import com.nvidia.ess.exceptions.WarmupException;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import java.util.List;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.collections4.CollectionUtils;
import org.apache.commons.collections4.MapUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Lazy;
import org.springframework.stereotype.Component;
import reactor.core.publisher.Mono;

@Component
@Slf4j
public class ServiceWarmup extends BootWarmupBase {

    private final NamespaceService namespaceService;
    private final CrudEncryptionKeyService crudEncryptionKeyService;
    private final JwkSetService jwkSetService;
    private final JwkCacheProperties jwkCacheProperties;
    private final EncryptionProperties encryptionProperties;

    // self-proxy injection to activate AOP on bean proxy (similar to dealing with calling @Transactional methods from same bean)
    @Setter(onMethod_ = {@Autowired, @Lazy})
    private ServiceWarmup self;

    public ServiceWarmup(
            @Value("${warmup.blockHealthStatusUntilComplete:true}")
            boolean blockHealthStatusUntilComplete,
            @Value("${warmup.timeout:15s}")
            Duration maxTimeout,
            JwkSetService jwkSetService,
            NamespaceService namespaceService,
            JwkCacheProperties jwkCacheProperties,
            CrudEncryptionKeyService crudEncryptionKeyService,
            EncryptionProperties encryptionProperties) {
        super(blockHealthStatusUntilComplete, maxTimeout);
        this.jwkSetService = jwkSetService;
        this.namespaceService = namespaceService;
        this.jwkCacheProperties = jwkCacheProperties;
        this.crudEncryptionKeyService = crudEncryptionKeyService;
        this.encryptionProperties = encryptionProperties;
    }

    @VisibleForTesting
    @Observed(name = TRACE_ONLY_NAME)
    void jwksCacheWarmup() {
        log.info("starting JWKS cache warmup");
        // BootWarmupBase will submit the Runnable to CompletableFuture.supplyAsync() that will run on ForkJoinPool.commonPool(), no need to set a bounded elastic scheduler to avoid blocking the main loop
        List<Throwable> exceptions = namespaceService.getNamespaces()
                .filter(namespaceModel -> namespaceModel.getDeletedAt() == null)
                .flatMapIterable(namespaceModel -> CollectionUtils.union(
                        MapUtils.emptyIfNull(namespaceModel.getNotaryAuthorizations()).values(),
                        MapUtils.emptyIfNull(namespaceModel.getOauthAuthorizations()).values()))
                // populate half of cache max size to not force eviction immediately
                .take(jwkCacheProperties.getMaxSize() / 2)
                .map(AuthorizationUdt::getJwksUrl)
                .flatMap(jwksUrl -> jwkSetService.getJwkSet(jwksUrl)
                        // collect errors only
                        .then(Mono.<Throwable>empty())
                        .onErrorResume(Mono::just)
                )
                .collectList()
                .contextCapture() // needed to propagate observation context (includes span) from ThreadLocal into the Reactor context
                .block(Duration.ofMinutes(1)); // JWKS should not take 1 minute, putting a high timeout in case a JWKS endpoint that hangs is configured

        if (CollectionUtils.isNotEmpty(exceptions)) {
            var ex = new WarmupException(
                    format("Failed (maybe partially) JWKS cache warmup with %d errors",
                            exceptions.size()));
            for (var thrown: exceptions) {
                ex.addSuppressed(thrown);
            }
            throw ex;
        }
    }

    @VisibleForTesting
    @Observed(name = TRACE_ONLY_NAME)
    void nekCacheWarmup() {
        log.info("starting NEK cache warmup: will populate only current kid for decryptionKeys");
        // BootWarmupBase will submit the Runnable to CompletableFuture.supplyAsync() that will run on ForkJoinPool.commonPool(), no need to set a bounded elastic scheduler to avoid blocking the main loop
        List<Throwable> exceptions = namespaceService.getNamespaces()
                .filter(namespaceModel -> namespaceModel.getDeletedAt() == null)
                // populate half of cache max size to not force eviction immediately
                .take(encryptionProperties.getCache().getEncryption().getMaxSize() / 2)
                // not using EncryptionKeyService since it will create unnecessary NEKs for empty namespaces
                .flatMap(namespaceModel ->
                        crudEncryptionKeyService.getKey(namespaceModel.getNamespace(), (model, e) -> true)
                                // loaded encryption key will be loaded for decryption key too.
                                // Avoid iterating over all kids
                                // Eventually, all data should be re-encrypted to have the last 4 NEKs in use
                                // Maybe add SAI on createdAt column and grab the latest 4 for each namespace
                                .flatMap(encryptionKeyModel -> crudEncryptionKeyService.getKey(
                                        encryptionKeyModel.getNamespace(),
                                        encryptionKeyModel.getKid(), (model, e) -> true))
                                // collect errors only
                                .then(Mono.<Throwable>empty())
                                .onErrorResume(e -> {
                                    if (e instanceof MissingKeyException) {
                                        return Mono.empty();
                                    }
                                    return Mono.just(e);
                                })
                )
                .collectList()
                .contextCapture() // needed to propagate observation context (includes span) from ThreadLocal into the Reactor context
                .block(Duration.ofSeconds(30)); // Going through all namespaces and NEKs should not take longer than request timeout on the C* client. Account for initial namespaces fetch as well

        if (CollectionUtils.isNotEmpty(exceptions)) {
            var ex = new WarmupException(
                    format("Failed (maybe partially) NEK cache warmup with %d errors",
                            exceptions.size()));
            for (var thrown: exceptions) {
                ex.addSuppressed(thrown);
            }
            throw ex;
        }
    }

    @Override
    public List<WarmupRunnable> createWarmupTasks() {
        // might add lightweight read query directly to all local C* contact points
        return List.of(
                new WarmupRunnable("Warmup JWKS cache", 0, self::jwksCacheWarmup),
                new WarmupRunnable("Warmup NEK encryptionKeys cache", 0, self::nekCacheWarmup)
        );
    }
}
