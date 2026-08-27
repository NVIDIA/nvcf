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
package com.nvidia.ess.encryption.persistence.services;

import static com.nvidia.ess.encryption.constants.Constants.TRACE_ONLY_NAME;
import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.github.benmanes.caffeine.cache.AsyncLoadingCache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.AllValidStatus;
import com.nvidia.ess.encryption.crypto.key.predicate.MulticallErrHandlingPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.MulticallErrHandlingPredicate.ErrorReportingPredicate;
import com.nvidia.ess.encryption.exceptions.CurrentKidCheckException;
import com.nvidia.ess.encryption.exceptions.CurrentKidConditionalSetException;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.KeyMustExistException;
import com.nvidia.ess.encryption.exceptions.KeyStatusUpdateException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.UnsetCurrentKidException;
import com.nvidia.ess.encryption.exceptions.shaded.BootResponseException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyByTimestampModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2PartitionModel;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2PartitionRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.binder.cache.CaffeineStatsCounter;
import io.micrometer.observation.annotation.Observed;
import io.opentelemetry.instrumentation.annotations.WithSpan;
import jakarta.annotation.PostConstruct;
import java.time.Duration;
import java.time.Instant;
import java.util.Objects;
import java.util.Optional;
import java.util.function.Function;
import java.util.function.Predicate;
import lombok.AllArgsConstructor;
import lombok.EqualsAndHashCode;
import lombok.Getter;
import lombok.NonNull;
import lombok.Setter;
import lombok.ToString;
import lombok.extern.slf4j.Slf4j;
import org.reactivestreams.Publisher;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

/**
 * Caching only the encrypted NEKs.
 * <p></p>
 * Not using Spring Cache's @Cacheable annotations because it caches the Mono itself. This includes
 * 1. Mono.empty() and Mono.error()
 * <p></p>
 * As an example, {@link com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService#getEncryptionKey(String)}
 * has a {@code switchIfEmpty()} on {@link CrudEncryptionKeyService#getKey(String, Predicate)}.
 * <br>
 * When {@link com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService#getEncryptionKey(String)}
 * is called twice, it ends up triggering {@code switchIfEmpty()} twice since Mono.empty() is
 * cached
 * <p></p>
 * <a href="https://www.baeldung.com/spring-webflux-cacheable">More Info on Webflux Caching</a>
 */
@Service
@Slf4j
public class CrudEncryptionKeyService {

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyRepository encryptionKeyRepository;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyCustomRepository encryptionKeyCustomRepository;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyV2PartitionRepository encryptionKeyV2PartitionRepository;

    @Setter(onMethod_ = {@Autowired})
    private MeterRegistry meterRegistry;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    // cache likely should stay bound to the V1 model until all happy path traffic is cut over to V2 tables
    //  main reason is that read traffic from V1 can't really populate current_kid and version columns
    //  once traffic is reversed, even if reads need to fallback to the V1 tables, current_kid and version columns likely can be populated from the V2 model
    private AsyncLoadingCache<ValidatedEncryptionCacheKey, EncryptionKeyModel> encryptionCache;
    private AsyncLoadingCache<ValidatedDecryptionCacheKey, EncryptionKeyModel> decryptionCache;

    @Getter
    @Setter
    @ToString
    @EqualsAndHashCode
    @AllArgsConstructor
    public static class ValidatedEncryptionCacheKey {

        private String namespace;

        @EqualsAndHashCode.Exclude
        private ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate;
    }

    @Getter
    @Setter
    @ToString
    @EqualsAndHashCode
    @AllArgsConstructor
    public static class ValidatedDecryptionCacheKey {

        private String namespace;
        private String kid;

        @EqualsAndHashCode.Exclude
        private ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate;
    }

    @PostConstruct
    public void initCache() {
        var encryptionStatsCounter = new CaffeineStatsCounter(meterRegistry, "encryptionKeys");

        encryptionCache = Caffeine.newBuilder()
                .maximumSize(encryptionProperties.getCache().getEncryption().getMaxSize())
                .expireAfterWrite(encryptionProperties.getCache().getEncryption().getTtl())
                .refreshAfterWrite(encryptionProperties.getCache().getEncryption().getRefreshAfterWrite())
                .scheduler(Scheduler.systemScheduler())
                .recordStats(() -> encryptionStatsCounter)
                // used for refresh only
                .buildAsync((key, executor) -> getKeyWithFailsafe(key.getNamespace(), key.getPredicate()).toFuture());
        encryptionStatsCounter.registerSizeMetric(encryptionCache.synchronous());

        var decryptionStatsCounter = new CaffeineStatsCounter(meterRegistry, "decryptionKeys");
        decryptionCache = Caffeine.newBuilder()
                .maximumSize(encryptionProperties.getCache().getDecryption().getMaxSize())
                .expireAfterWrite(encryptionProperties.getCache().getDecryption().getTtl())
                .refreshAfterWrite(encryptionProperties.getCache().getDecryption().getRefreshAfterWrite())
                .scheduler(Scheduler.systemScheduler())
                .recordStats(() -> decryptionStatsCounter)
                // used for refresh only
                .buildAsync((key, executor) -> getKeyWithFailsafe(key.getNamespace(), key.getKid(),
                        key.getPredicate(), false).toFuture());
        decryptionStatsCounter.registerSizeMetric(decryptionCache.synchronous());

    }

    // not touching cache - will be correctly populated on get eventually. No strict reqs on up-to-date
    // even if choosing to evict/update, other replicas will still be using the stale entry
    public Mono<Boolean> addKey(EncryptionKeyV2Model encryptionKeyModel) {
        Mono<Boolean> saveMono;
        var messageFormatter = encryptionKeyModel.logMessageFormatter();
        if (encryptionProperties.getImmutableTable().isNekv2WriteEnabled()
                || encryptionProperties.getImmutableTable().getNekV2WriteAllowList()
                .contains(encryptionKeyModel.getNamespace())) {
            log.info(messageFormatter.apply("NEKv2 write"));
            saveMono = encryptionKeyCustomRepository.addKeyV2(encryptionKeyModel);
        } else {
            log.info(messageFormatter.apply("NEKv1 write"));
            saveMono = encryptionKeyCustomRepository.addKey(encryptionKeyModel);
        }

        return saveMono.onErrorMap(e -> {
            log.error(String.format(
                            "Unexpected NEKv2 repository error on creation of NEK for namespace: %s",
                            encryptionKeyModel.getNamespace()
                    ), e);
            return new EncryptionException(
                    String.format("Failed to create encryption key for namespace %s",
                            encryptionKeyModel.getNamespace()), e);
        });
    }

    // add span on cached calls to avoid gaps between spans
    @Observed(name = TRACE_ONLY_NAME)
    @WithSpan
    public Mono<EncryptionKeyModel> getKey(String namespace,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate) {
        // contextWrite must be on getKeyWithFailsafe (before toFuture), not on Mono.fromFuture.
        // Futures are eager - toFuture() calls subscribe() on the Mono internally
        // so context must already be attached at that point for spans to connect
        return Mono.deferContextual(contextView ->
                Mono.fromFuture(
                    encryptionCache.get(new ValidatedEncryptionCacheKey(namespace, predicate), (key, executor) -> getKeyWithFailsafe(key.getNamespace(), key.getPredicate())
                            .contextWrite(ctx -> ctx.putAll(contextView))
                            .toFuture())
                )
        );
    }

    public Mono<EncryptionKeyModel> getKeyWithFailsafe(String namespace,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate) {

        final var nekv1Validator = MulticallErrHandlingPredicate.create(predicate);

        final var nekv1Read = getKeyUncached(namespace)
                .filter(model -> validatePredicate(nekv1Validator, model, true))
                .switchIfEmpty(Mono.defer(() -> {
                    // No valid NEK-rows were found in NEKv1 for the given namespace.
                    var err = nekv1Validator.cumulativeError();

                    // If the validator found an error corresponding to a bad NEK-row, return the
                    // error to the caller.
                    return Mono.error(!Objects.isNull(err)
                            ? err.getErrorException()
                            : new MissingKeyException(String.format(
                                    "Failed to get a valid key for namespace %s in NEKv1", namespace))
                    );
                }))
                .doOnNext(keyInStorage -> encryptionMetricsRegistry.recordGetEncryptionNekV1(namespace,
                        Optional.of(keyInStorage.getKid())))
                // important to ignore MissingKeyException when namespace has no NEK yet
                .doOnError(e -> !(e instanceof MissingKeyException),
                        ignored ->  encryptionMetricsRegistry.recordGetEncryptionNekV1(namespace, Optional.empty()));

        if (!encryptionProperties.getImmutableTable().isNekv2ReadEnabled()) {
            // NEKv2 read currently disabled. Read only from NEKv1.
            return nekv1Read;
        }

        return getKeyUncachedV2(namespace, predicate)
                .doOnNext(keyInStorage ->
                        encryptionMetricsRegistry.recordGetEncryptionNekV2(namespace, keyInStorage.getKid()))
                .map(EncryptionKeyV2Model::toEncryptionKeyByKidModel)
                .onErrorResume(nekV2Err -> {
                    // NEKv2-fetch yielded an error.

                    if (!(nekV2Err instanceof MissingKeyException)) {

                        log.error(String.format("Encountered an error fetching an encryption-key for namespace %s " +
                                "from NEKv2", namespace), nekV2Err);

                        encryptionMetricsRegistry.recordGetEncryptionNekV2Error(namespace,
                                nekV2Err.getClass().getName());
                    }

                    if (!encryptionProperties.getImmutableTable().isNekv1FallbackReadEnabled()) {

                        // If fallback to NEKv1 is disabled, just surface the NEKv2 fetch error
                        // to the caller while ensuring to wrap any non-BaseRestException errors
                        // with an `EncryptionException` wrapper.

                        if (nekV2Err instanceof BootResponseException ex) {
                            return Mono.error(ex);
                        }

                        return Mono.error(() -> new EncryptionException(
                                String.format("Failed to get key for namespace %s from NEKv2 "
                                        + "and fallback to NEKv1 is disabled.",
                                        namespace),
                                nekV2Err));
                    }

                    // If fallback to NEKv1 is enabled, attempt to fetch a valid NEK from
                    // NEKv1.
                    //
                    // If a valid NEK was obtained from NEKv1:
                    //      -- Return it and drop the NEKv2 fetch-error.
                    //
                    // If the NEKv1-fetch resulted in anything other than a MissingKeyException
                    // (empty-fetch):
                    //      -- Surface the NEKv1-fetch-error to the caller.
                    //
                    // Otherwise:
                    //
                    //      -- If the NEKv2-fetch error was a MissingKeyException (empty-fetch)
                    //         return either one (we return the NEKv2-fetch-error here).
                    //
                    //      -- Otherwise, drop the NEKv2-fetch error and surface the NEKv1-fetch
                    //         error to the caller.

                    return nekv1Read.onErrorMap(nekV1Err -> {
                        if (nekV1Err instanceof MissingKeyException) {
                            return nekV2Err;
                        }
                        return nekV1Err;
                    });
                })
                .doOnError(e -> !(e instanceof MissingKeyException),
                        e -> encryptionMetricsRegistry.recordGetEncryptionNekError(
                                namespace, e.getClass().getName()))
                .doOnNext(model -> {
                    Instant createdAtTimestamp = Instant.ofEpochMilli(Uuids.unixTimestamp(model.getCreatedAt()));
                    Instant now = Instant.now();
                    // if alert conditions overlap, trigger high severity alert
                    if (Duration.between(createdAtTimestamp, now).compareTo(encryptionProperties.getRotation()
                            .getCompliancePeriod().minus(encryptionProperties.getNekAgeAlertingOffset())) >= 0) {
                        // critical alert before compliance schedule - 2 days
                        encryptionMetricsRegistry.recordNekRotationAgeCritical(
                                model.getNamespace());
                    } else if (Duration.between(createdAtTimestamp, now).compareTo(encryptionProperties.getRotation()
                            .getScheduled().getPeriod().plus(encryptionProperties.getNekAgeAlertingOffset())) >= 0) {
                        // warning alert after expected rotation schedule + 2 days (should be ~7 days earlier than compliance)
                        encryptionMetricsRegistry.recordNekRotationAgeWarning(
                                model.getNamespace());
                    }
                });

    }

    // add span on cached calls to avoid gaps between spans
    @Observed(name = TRACE_ONLY_NAME)
    @WithSpan
    public Mono<EncryptionKeyModel> getKey(String namespace, String kid,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate) {
        // contextWrite must be on getKeyWithFailsafe (before toFuture), not on Mono.fromFuture.
        // Futures are eager - toFuture() calls subscribe() on the Mono internally
        // so context must already be attached at that point for spans to connect
        return Mono.deferContextual(contextView ->
                Mono.fromFuture(
                        decryptionCache.get(
                                new ValidatedDecryptionCacheKey(namespace, kid, predicate), (key, executor) -> getKeyWithFailsafe(key.getNamespace(), key.getKid(),
                                        key.getPredicate(), false)
                                        .contextWrite(ctx -> ctx.putAll(contextView))
                                        .toFuture())
                )
        );
    }

    public Mono<EncryptionKeyModel> getKeyWithFailsafe(String namespace, String kid, 
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate, boolean keyMustExist) {


        var nekv1Validator = MulticallErrHandlingPredicate.create(predicate);

        var nekv1Read = getKeyUncached(namespace, kid)
                .filter(model -> validatePredicate(nekv1Validator, model, true))
                .switchIfEmpty(Mono.defer(() -> {
                    // No valid NEK-rows were found in NEKv1 for the given namespace and KID.
                    var err = nekv1Validator.cumulativeError();

                    // If the validator found an error corresponding to a bad NEK-row, return the
                    // error to the caller.
                    return Mono.error(!Objects.isNull(err)
                            ? err.getErrorException()
                            : new MissingKeyException(String.format(
                                    "Failed to get a valid key %s for namespace %s in NEKv1", maskKid(kid), namespace))
                    );
                }))
                .doOnNext(ignored -> encryptionMetricsRegistry.recordGetDecryptionNekV1(namespace, kid, true))
                .doOnError(ignored -> encryptionMetricsRegistry.recordGetDecryptionNekV1(namespace, kid, false));

        if (!encryptionProperties.getImmutableTable().isNekv2ReadEnabled()) {
            // NEKv2 read currently disabled. Read only from NEKv1.
            return nekv1Read;
        }

        return getKeyUncachedV2(namespace, kid, predicate, false)
                .doOnNext(ignored -> encryptionMetricsRegistry.recordGetDecryptionNekV2(namespace, kid))
                .map(EncryptionKeyV2Model::toEncryptionKeyByKidModel)
                .onErrorResume(nekV2Err -> {
                    // NEKv2-fetch yielded an error.

                    if (!(nekV2Err instanceof MissingKeyException)) {

                        log.error(String.format("Encountered an error fetching a key %s for namespace %s " +
                                "from NEKv2", maskKid(kid), namespace), nekV2Err);

                        encryptionMetricsRegistry.recordGetDecryptionNekV2Error(namespace, kid,
                                nekV2Err.getClass().getName());
                    }

                    if (!encryptionProperties.getImmutableTable().isNekv1FallbackReadEnabled()) {

                        // If fallback to NEKv1 is disabled, just surface the NEKv2 fetch error
                        // to the caller while ensuring to wrap any non-BaseRestException errors
                        // with an `EncryptionException` wrapper.

                        if (nekV2Err instanceof BootResponseException ex) {
                            return Mono.error(ex);
                        }

                        return Mono.error(() -> new EncryptionException(
                                String.format("Failed to get key %s for namespace %s from "
                                        + "NEKv2 and fallback-read to NEKv1 is disabled", maskKid(kid),
                                        namespace),
                                nekV2Err));
                    }

                    // If fallback to NEKv1 is enabled, attempt to fetch a valid NEK from
                    // NEKv1.
                    //
                    // If a valid NEK was obtained from NEKv1:
                    //      -- Return it and drop the NEKv2 fetch-error.
                    //
                    // If the NEKv1-fetch resulted in anything other than a MissingKeyException
                    // (empty-fetch):
                    //      -- Surface the NEKv1-fetch-error to the caller.
                    //
                    // Otherwise:
                    //
                    //      -- If the NEKv2-fetch error was a MissingKeyException (empty-fetch)
                    //         return either one (we return the NEKv2-fetch-error here).
                    //
                    //      -- Otherwise, drop the NEKv2-fetch error and surface the NEKv1-fetch
                    //         error to the caller.

                    return nekv1Read
                            .onErrorMap(nekV1Err -> {
                                if (nekV1Err instanceof MissingKeyException) {
                                    return nekV2Err;
                                }
                                return nekV1Err;
                            });
                })
                .onErrorMap(e -> keyMustExist && e instanceof MissingKeyException
                        ? new KeyMustExistException(String.format("Valid key %s in namespace %s must exist " +
                                "but none found", maskKid(kid), namespace))
                        : e
                )
                .doOnError(e -> !(e instanceof MissingKeyException),
                        e -> encryptionMetricsRegistry.recordGetDecryptionNekError(
                                namespace, kid, e.getClass().getName()));

    }

    public Mono<EncryptionKeyModel> getKeyUncached(String namespace) {
        return encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(
                        namespace).map(
                        EncryptionKeyByTimestampModel::toEncryptionKeyByKidModel)
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected NEKv1 repository error fetching latest NEK for " +
                                    "namespace: %s", namespace),
                            e);
                    return new EncryptionException(
                            String.format("Failed to get encryption key for namespace %s from NEKv1 due "
                                    + "to an unexpected repository error",
                                    namespace),
                            e);
                });

    }

    public Mono<EncryptionKeyV2Model> getKeyUncachedV2(String namespace,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate) {
        return encryptionKeyV2PartitionRepository.findFirstByNamespace(namespace)
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected NEKv2 repository error fetching current NEK kid " +
                                    "for namespace: %s", namespace),
                            e);
                    return new EncryptionException(
                            String.format("Failed to get key for namespace %s from NEKv2 due to "
                                    + "an unexpected repository error",
                                    namespace),
                            e);
                })
                // NEKv2 partitions with current_kid = NULL can exist if they only contain
                // NEK entries formed by re-encryption of NEKs in NEKv1 for the same namespace,
                // and none of those NEK entries have undergone promotion yet.
                //
                // Skip such NEKv2 partitions (return a MissingKeyException that will either result in
                // a fallback read of NEKv1 or be surfaced to the caller).
                .filter(nekv2Partition -> !Objects.isNull(nekv2Partition.getCurrentKid()))
                .switchIfEmpty(Mono.error(() -> new MissingKeyException(
                        String.format("No key for namespace %s found in NEKv2", namespace))
                ))
                .map(EncryptionKeyV2PartitionModel::getCurrentKid)
                .flatMap(kid -> getKeyUncachedV2(namespace, kid, predicate, true));
    }

    public Mono<EncryptionKeyModel> getKeyUncached(String namespace, String kid) {
        return encryptionKeyRepository.findByNamespaceAndKid(namespace, kid)
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected NEKv1 repository error fetching NEK for KID: %s, " +
                                    "namespace: %s", maskKid(kid), namespace),
                            e);
                    return new EncryptionException(
                            String.format("Failed to get decryption key %s for namespace %s from NEKv1 "
                                    + "due to an unexpected repository error", maskKid(kid), namespace),
                            e);
                });
    }

    private Mono<EncryptionKeyV2Model> getKeyUncachedV2(String namespace, String kid,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate, boolean keyMustExistForKid) {

        @AllArgsConstructor
        class RowWithValidationCheck {
            @NonNull public final EncryptionKeyV2Model model;
            public final boolean valid;
        }

        final var validator = MulticallErrHandlingPredicate.create(predicate);

        final Function<Mono<EncryptionKeyV2Model>, Mono<RowWithValidationCheck>> validateAndGet =
                modelHandle -> modelHandle.map(model ->
                    new RowWithValidationCheck(
                            model,
                            AllValidStatus.allValidStatusStrings().contains(model.getStatus()) &&
                                    validatePredicate(validator, model.toEncryptionKeyByKidModel(),
                                            true)
                    )
                );

        return validateAndGet.apply(encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, kid))
                .expand(row -> row.valid
                        ? Mono.empty()
                        : validateAndGet.apply(
                                encryptionKeyV2Repository.findFirstByNamespaceAndKidAndEncryptedAtLessThan(
                                        row.model.getNamespace(), row.model.getKid(), row.model.getEncryptedAt()
                                ))
                )
                .filter(row -> row.valid)
                .map(row -> row.model)
                .next()
                .onErrorMap(e -> {
                    // An NEK should have been retrievable for the given KID.
                    log.error(String.format("Unexpected NEKv2 repository error fetching valid NEK " +
                                    "for KID: %s, namespace: %s", maskKid(kid), namespace),
                            e);
                    return new EncryptionException(
                            String.format("Failed to get key %s for namespace %s from NEKv2 "
                                    + "due to unexpected repository error",
                                    maskKid(kid), namespace),
                            e);
                })
                // entering this part of the code means that current_kid was successfully fetched, so both scenarios are considered "error"
                // 1. No NEK corresponding to kid exists
                // 2. NEK is found, but none of the versions are valid
                .switchIfEmpty(Mono.error(() -> {

                    // No valid NEK found in NEKv2 corresponding to the namespace and KID.
                    log.debug("Either no NEK for {}: {} exists or all versions failed validation", namespace, maskKid(kid));

                    var err = validator.cumulativeError();
                    if (!Objects.isNull(err)) {
                        // If an error was found by the validator corresponding to a bad NEK-row, return the
                        // error to the caller.
                        //
                        return err.getErrorException();
                    }

                    // Validator reported no error, which should mean that no bad NEK-rows were found.
                    // NEKv2 query simply returned an empty result.

                    if (keyMustExistForKid) {

                        // When called from `getKeyUncachedV2(namespace, predicate)`: a valid key MUST exist
                        // for the given KID as the given KID is the `current_kid` in the namespace. In that situation,
                        // something other than a `MissingKeyException` should be thrown (a `MissingKeyException` will
                        // simply be ignored by some callers like NEK-creation and NEK-rotation).
                        //
                        // TODO: alerting

                        return new KeyMustExistException(String.format("Valid key %s in namespace %s must exist " +
                                "but none found", maskKid(kid), namespace));
                    }

                    return new MissingKeyException(String.format(
                            "Failed to get a valid key %s for namespace %s in NEKv2", maskKid(kid), namespace));
                }));
    }

    @VisibleForTesting
    public void clearEncryptionCache() {
        encryptionCache.synchronous().invalidateAll();
        // evictionListener does not trigger on invalidation
        encryptionMetricsRegistry.removeNekRotationAgeDelta();
    }


    @VisibleForTesting
    public void clearDecryptionCache() {
        decryptionCache.synchronous().invalidateAll();
    }

    /**
     *
     * <p>For each unique namespace found in the union of NEKv1 and NEKv2 rows (in no particular order),
     * execute the passed callback ({@code actionToPerform}) exactly once and return a stream ({@link Mono}
     * or {@link Flux}) of elements of type {@link T}. Concatenate all returned sequences into a single sequence
     * and return it.</p>
     * 
     * @param <T>
     * @param page
     * @param prefetchRateLimit
     * @param actionToPerform
     * @return
     */
    public <T> Flux<T> performActionOncePerNamespace(CassandraPageRequest page, int prefetchRateLimit,
            Function<String, Publisher<T>> actionToPerform) {

        final var nekv2ReadEnabled = encryptionProperties.getImmutableTable().isNekv2ReadEnabled();
        final var nekv1FallbackReadEnabled = encryptionProperties.getImmutableTable().isNekv1FallbackReadEnabled();

        Function<Slice<String>, Mono<Slice<String>>> nextNekv2NamespaceBatch = slice -> slice.hasNext()
                // Continue to the next page of NEKv2 namespaces.
                ? encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv2(
                    (CassandraPageRequest) slice.getPageable())
                // NEKv2 namespace iteration exhausted.
                : Mono.empty();

        Function<Slice<String>, Mono<Slice<String>>> nextNekv1NamespaceBatch = slice -> slice.hasNext()
                // Continue to the next page of NEKv1 namespaces.
                ? encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv1(
                    (CassandraPageRequest) slice.getPageable())
                // NEKv1 namespace iteration exhausted.
                : Mono.empty();

        return Flux.merge(
                // Read namespaces in NEKv2 if NEKv2 reads are enabled.
                nekv2ReadEnabled
                        // Iterate through all namespaces in NEKv2.
                        ? encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv2(page)
                                .expand(nextNekv2NamespaceBatch::apply)
                        : Flux.empty(),
                // Read namespaces in NEKv1 if NEKv2 reads are disabled or fallback reads from NEKv1 are
                // enabled.
                !nekv2ReadEnabled || nekv1FallbackReadEnabled
                        // Iterate through all namespaces in NEKv1.
                        ? encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv1(page)
                                .expand(nextNekv1NamespaceBatch::apply)
                        : Flux.empty()
        )
                // Extract namespaces in a single page.
                .map(Slice::getContent)
                // Flatten the stream of lists.
                .flatMapIterable(Function.identity())
                // Dedup the namespaces across the stream.
                .distinct()
                // Apply the action to perform on each namespace.
                // use concatmap to serialize writes to the same partition
                .concatMap(actionToPerform::apply)
                // Limit prefetch-rate from here to downstream operators.
                .limitRate(prefetchRateLimit);
    }

    /**
     *
     * <p>For each namespace and KID, iterate over all NEKv1 and NEKv2 rows in the following order:</p>
     *
     * <pre>
     * - For each unique namespace (in no particular order):
     *    -- For each unique `kid` in the namespace (in no particular order):
     *        --- [1] Iterate over NEKv2 rows for (namespace, kid) in DESC order of `encrypted_at`.
     *        --- [2] Iterate over the NEKv1 row for (namespace, kid), if one exists.
     *        --- [3] Obtain the first row in the above-described order that passes the `predicate`
     *                check for this KID, and execute `actionToPerform` on it if it does.
     *        --- [4] Log errors corresponding to predicate failures on any row as well as predicate failures
     *                on all rows corresponding to the given KID (alerts to be implemented).
     *        --- [5] Move on to the next `kid` for the `namespace`.
     * </pre>
     *
     * @param <T>
     * @param page
     * @param prefetchRateLimit
     * @param predicate
     * @param actionToPerform
     * @return
     */
    public <T> Flux<T> performActionOncePerNamespaceKidPair(CassandraPageRequest page, int prefetchRateLimit,
            ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> predicate,
            Function<EncryptionKeyModel, Mono<T>> actionToPerform) {

        final var nekv2ReadEnabled = encryptionProperties.getImmutableTable().isNekv2ReadEnabled();
        final var nekv1FallbackReadEnabled = encryptionProperties.getImmutableTable().isNekv1FallbackReadEnabled();

        @AllArgsConstructor
        class RowWithValidationChecks {
            final String namespace;
            public final String kid;
            @NonNull public final Optional<EncryptionKeyModel> model;
            @NonNull public final Optional<BootResponseException> validationError;
        }

        return performActionOncePerNamespace(page, prefetchRateLimit, namespace -> {

            Function<Slice<EncryptionKeyV2Model>, Mono<Slice<EncryptionKeyV2Model>>> nextNekv2RowBatch =
                    slice -> slice.hasNext()
                            // Continue iterating through NEKv2.
                            ? encryptionKeyV2Repository.findAllByNamespace(
                                    namespace, (CassandraPageRequest) slice.getPageable())
                            // Finished iterating through NEKv2.
                            : Mono.empty();

            // Read KIDs for the current namespace in NEKv2 if NEKv2 reads are enabled.
            var kidsInNEKv2 = nekv2ReadEnabled

                    // Iterate through NEKv2 to collect all distinct KIDs.
                    ? encryptionKeyV2Repository.findAllByNamespace(namespace, page)
                            .expand(nextNekv2RowBatch::apply)
                            // Extract NEKv2 rows from each page.
                            .map(Slice::getContent)
                            // Flatten the stream of lists of NEKv2 rows to a stream
                            // of NEKv2 rows.
                            .flatMapIterable(Function.identity())
                            // Discard NEKv2 rows whose `status` is not `VALIDATED`
                            // or `CREATION_VALIDATED`.
                            .filter(model -> AllValidStatus.allValidStatusStrings()
                                            .contains(model.getStatus()))
                            // Extract the `kid` from the rest.
                            .map(EncryptionKeyV2Model::getKid)

                    : Flux.<String>empty();

            Function<Slice<EncryptionKeyModel>, Mono<Slice<EncryptionKeyModel>>> nextNekv1RowBatch =
                    slice -> slice.hasNext()
                            // Continue iterating through NEKv1.
                            ? encryptionKeyRepository.findAllByNamespace(namespace,
                                    (CassandraPageRequest) slice.getPageable())
                            // Finished iterating through NEKv1.
                            : Mono.empty();

            // Read KIDs for the current namespace in NEKv1 if NEKv2 reads are disabled or
            // fallback reads from NEKv1 are enabled.
            var kidsInNEKv1 = !nekv2ReadEnabled || nekv1FallbackReadEnabled

                    // Start iterating through NEKv1 to collect all distinct KIDs.
                    ? encryptionKeyRepository.findAllByNamespace(namespace, page)
                            .expand(nextNekv1RowBatch::apply)
                            // Extract NEKv1 rows from each page.
                            .map(Slice::getContent)
                            // Flatten the stream of lists of NEKv1 rows to a stream
                            // of NEKv1 rows.
                            .flatMapIterable(Function.identity())
                            // Extract the `kid` from the rest.
                            .map(EncryptionKeyModel::getKid)

                    : Flux.<String>empty();

            // Concatenate both streams of KIDs.
            return Flux.merge(kidsInNEKv2, kidsInNEKv1)
                    // Dedup the concatenated stream of KIDs.
                    .distinct()
                    // Attempt to fetch at least one valid `EncryptionKeyModel` corresponding to the KID.
                    .flatMap(kid ->

                        // Attempt to obtain a valid NEK for this KID.

                        getKeyWithFailsafe(namespace, kid, predicate, true)
                                // Successfully obtained and validated an NEK for this KID.
                                .map(model -> new RowWithValidationChecks(namespace, kid, Optional.of(model),
                                        Optional.empty()))
                                // NOTE: `getKeyWithFailsafe` SHOULD NOT yield a `Mono.empty()` - that would
                                // be indicative of a bug. This `switchIfEmpty(...)` exists only in order to
                                // signal a scenario like this for logging or alerting downstream.
                                .switchIfEmpty(Mono.just(new RowWithValidationChecks(namespace, kid, Optional.empty(),
                                        Optional.empty())))
                                // An error was encountered when attempting to obtain a valid NEK for this
                                // KID. Propagate this error downward for error-handling / reporting.
                                .onErrorResume(e -> {

                                    BootResponseException encryptionEx;
                                    if (e instanceof BootResponseException ex) {
                                        encryptionEx = ex;
                                    } else {
                                        encryptionEx = new EncryptionException("Internal key-fetch error.", e);
                                    }

                                    return Mono.just(new RowWithValidationChecks(
                                            namespace,
                                            kid,
                                            Optional.empty(),
                                            Optional.of(encryptionEx)
                                    ));
                                })
                    )
                    // use concatmap to serialize writes to the same partition
                    .concatMap(modelAndMaybeValidationError -> {

                        if (modelAndMaybeValidationError.model.isEmpty()) {

                            // TODO: Named constants for all such error-messages.
                            var noFetchErrFallbackMsg = String.format("An NEK (namespace: %s, KID: %s) was found in storage " +
                                    "but an attempt to fetch it yielded an empty result instead of an exception. " +
                                    "THIS MAY BE A BUG THAT NEEDS TO BE FIXED.",
                                    modelAndMaybeValidationError.namespace,
                                    modelAndMaybeValidationError.kid);

                            // No valid NEK for KID found.
                            var ex = modelAndMaybeValidationError.validationError
                                    .orElse(new EncryptionException(noFetchErrFallbackMsg));

                            log.error(String.format("Unable to perform operation on any NEK (namespace: %s, KID: %s). " +
                                            "No valid NEK found.",
                                            modelAndMaybeValidationError.namespace,
                                            modelAndMaybeValidationError.kid),
                                    ex);

                            return Mono.empty();
                        }

                        // A valid NEK was found for this KID.
                        // Apply the action-to-perform the valid NEK that was found.
                        return actionToPerform.apply(modelAndMaybeValidationError.model.get())
                                .onErrorResume(e -> {
                                    // Action failed to execute on the NEK.
                                    var model = modelAndMaybeValidationError.model.get();

                                    log.error(String.format("Action failed to execute on NEK with KID: %s, " +
                                                    "created_at: %s, encrypted_at: %s -> %s",
                                                    maskKid(modelAndMaybeValidationError.kid), model.getCreatedAt(),
                                                    model.getEncryptedAt(), model),
                                            e);

                                    return Mono.empty();
                                });
                    });
        });
    }

    // TODO need to query both tables
    public Mono<Slice<String>> findNamespaces(CassandraPageRequest page) {
        return encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv1(page)
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected NEKv1 repository error paginating (size: %s) " +
                                    "through distinct namespaces in NEKv1", page.getPagingState()),
                            e);
                    return new EncryptionException(
                            String.format("Failed to get all namespaces for page %s",
                                    page.getPagingState()),
                            e);
                });
    }

    public Mono<Slice<EncryptionKeyModel>> findAllKeys(String namespace,
            CassandraPageRequest page) {
        return encryptionKeyRepository.findAllByNamespace(namespace, page)
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected repository error paginating (size: %s, namespace: %s) " +
                                    "through encryption key versions", page.getPagingState(), namespace),
                            e);
                    return new EncryptionException(
                            String.format(
                                    "Failed to get encryption keys in namespace %s for page %s",
                                    namespace, page.getPagingState()),
                            e);
                });
    }

    private static boolean validatePredicate(
            MulticallErrHandlingPredicate<EncryptionKeyModel, KeyFetchError> predicate,
            EncryptionKeyModel model, boolean reportAnyErrors) {
        var result = predicate.test(model, reportAnyErrors);
        if (!result) {
            log.error("failed predicate");
        }
        return result;
    }

    public Flux<EncryptionKeyV2Model> findAllV2Keys(int fetchSize, int prefetchRateLimit) {
        CassandraPageRequest pageRequest = CassandraPageRequest.first(fetchSize);

        return encryptionKeyCustomRepository.findAllV2Keys(pageRequest)
                .expand(slice -> {
                    if (!slice.hasNext()) {
                        return Mono.empty();
                    }
                    return encryptionKeyCustomRepository.findAllV2Keys(
                            (CassandraPageRequest) slice.getPageable());
                })
                .onErrorMap(e -> {
                    log.error(String.format("Unexpected repository error paginating (size: %s) " +
                                    "through all encryption key versions", fetchSize),
                            e);
                    return new EncryptionException("Failed to get encryption keys", e);
                })
                .limitRate(prefetchRateLimit)
                .flatMapIterable(Function.identity());
    }

    public Mono<Boolean> promoteKey(EncryptionKeyV2Model model) {

        var msgFormatter = model.logMessageFormatter();

        Function<EncryptionKeyV2PartitionModel, Mono<Boolean>> passForNonNullCurrentKid =
                partitionModel -> Objects.isNull(partitionModel.getCurrentKid())
                        ? Mono.error(() -> new UnsetCurrentKidException(msgFormatter.apply(
                                "current_kid is not set (NULL) for namespace in NEKv2 after promoting key")))
                        : Mono.just(true);

        // Persist the status-update to VALIDATED for this NEK.
        return encryptionKeyCustomRepository.updateStatus(model.getNamespace(), model.getKid(),
                        model.getEncryptedAt(), model.getStatus())
                .onErrorMap(e -> new KeyStatusUpdateException(msgFormatter.apply(
                        "Error while promoting status of key"), e)
                )
                // Assume `currentKid` is from storage and not manually overridden. Check
                // for whether it is set (non-null) and if it is not set, then set it with an LWT.
                .flatMap(updateWasApplied -> Boolean.TRUE.equals(updateWasApplied) && Objects.isNull(model.getCurrentKid())

                        ? encryptionKeyCustomRepository.updateCurrentKidIfNotSet(model.getNamespace(), model.getKid())
                                .onErrorMap(e -> new CurrentKidConditionalSetException(msgFormatter.apply(
                                        "Error during conditional set (if currently NULL) of " +
                                        "current_kid for namespace in NEKv2 after promoting key"), e)
                                )
                                .then(encryptionKeyV2PartitionRepository.findFirstByNamespace(model.getNamespace())
                                        .onErrorMap(e -> new CurrentKidCheckException(msgFormatter.apply(
                                                "Error while checking whether current_kid is set for " +
                                                "namespace in NEKv2 after promoting key"), e)
                                        )
                                        .switchIfEmpty(Mono.error(() -> new UnsetCurrentKidException(msgFormatter.apply(
                                                "No partition found for namespace in NEKv2 after promoting key")))
                                        )
                                        .flatMap(passForNonNullCurrentKid::apply)
                                )

                        : Mono.just(updateWasApplied)
                );
    }

    public Mono<Boolean> promoteRotationKey(EncryptionKeyV2Model model) {

        var msgFormatter = model.logMessageFormatter();

        return encryptionKeyCustomRepository.updateStatusAndCurrentKid(model.getNamespace(),
                        model.getKid(), model.getEncryptedAt(), model.getStatus(), model.getCurrentKid())
                .onErrorMap(e -> new KeyStatusUpdateException(msgFormatter.apply("Error while promoting status " +
                        "of rotation key"), e)
                );
    }
}
