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
package com.nvidia.ess.encryption.crypto.key;

import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_ENCRYPTED_AT_KEY;
import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_KID_KEY;
import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_NAMESPACE_KEY;
import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.crypto.MekService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptionKeyPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.MulticallErrHandlingPredicate.ErrorReportingPredicate;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationReactiveHelper;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.shaded.BootResponseException;
import com.nvidia.ess.encryption.integrity.IntegrityChecksPopulationFields;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.util.EncryptionKeyGenerator;
import com.nvidia.ess.encryption.util.TracingUtils;
import io.opentelemetry.api.trace.Span;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.util.HashMap;
import java.util.UUID;
import java.util.function.Consumer;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.util.Pair;
import reactor.core.publisher.Mono;

/**
 * Long-term this is will be the main implementation. Extend to add: 1. Look for default EK for
 * decryption 2. Allow list for rollout
 */
@Slf4j
public class BaseEncryptionKeyService
        implements EncryptionKeyService, EncryptionKeyRotationService, EncryptionKeyReencryptionService {

    @Setter(onMethod_ = {@Autowired})
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @Setter(onMethod_ = {@Autowired})
    protected EncryptionProperties encryptionProperties;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    protected MekService mekService;

    @Setter(onMethod_ = {@Autowired})
    protected KeyValidationExecutor keyValidationExecutor;

    @Setter(onMethod_ = {@Autowired})
    private KeyValidationReactiveHelper keyValidationReactiveHelper;

    private static final String FAILED_GET_EK_MSG = "Failed to get encryption key for %s";
    private static final String FAILED_GET_DK_MSG = "Failed to get decryption key for %s";
    private static final String FAILED_ENCRYPT_EK_MSG = "Failed to encrypt encryption key for %s";
    private static final String FAILED_REENCRYPT_EK_MSG = "failed to re-encrypt encryption key for %s";

    private final ErrorReportingPredicate<EncryptionKeyModel, KeyFetchError> validationFunction = this::validateExtractedKeyAndHeaders;

    @Override
    public Mono<OctetSequenceKey> getEncryptionKey(String namespace) {
        return Mono.deferContextual(ctx -> crudEncryptionKeyService.getKey(namespace, validationFunction)
                .onErrorResume(e -> {
                    if (e instanceof MissingKeyException) {
                        // No key found. Just proceed to create a new key.
                        return Mono.empty();
                    }
                    return Mono.error(e);
                })
                .onErrorMap(e -> {
                    if (e instanceof BootResponseException ex) {
                        return ex;
                    }
                    return new EncryptionException(String.format("Unable to obtain an encryption key " +
                            "corresponding to namespace: %s due to an unrecognized error", namespace), e);
                })
                .switchIfEmpty(Mono.defer(() ->
                        createEncryptionKey(namespace, EncryptionKeyStatus.CREATION_VALIDATED))
                )
                .flatMap(encryptionKeyModel -> {
                    var validationResult = keyValidationExecutor.extractKeyAndValidateHeaders(encryptionKeyModel);
                    if (!validationResult.isValid()) {
                        keyValidationReactiveHelper.recordValidationErrorTelemetry(ctx, encryptionKeyModel, validationResult.getValidationError()
                                .getErrorCode());
                        log.error(String.format(FAILED_GET_EK_MSG, namespace),
                                validationResult.getValidationError().getErrorException());
                        return Mono.error(() -> new EncryptionException(
                                String.format(FAILED_GET_EK_MSG, namespace),
                                validationResult.getValidationError().getErrorException()));
                    }
                    return Mono.just(validationResult.getOctetSequenceKey());
                })
        );

    }

    @Override
    public Mono<OctetSequenceKey> getDecryptionKey(String namespace, String kid) {

        return Mono.deferContextual(ctx -> crudEncryptionKeyService.getKey(namespace, kid, validationFunction)
                .onErrorMap(e -> {
                    if (e instanceof BootResponseException ex) {
                        return ex;
                    }
                    return new EncryptionException(String.format("Unable to obtain a decryption key " +
                            "corresponding to namespace: %s, kid: %s due to an unrecognized error",
                            namespace, kid), e);
                })
                // This `switchIfEmpty(...)` is precautionary and shouldn't be reached during normal
                // execution. The absence of a decryption-key should cause a `MissingKeyException` instead.
                .switchIfEmpty(Mono.error(() -> new MissingKeyException(
                                    "Unexpected error: no response received from fetch of decryption-key " +
                                    "corresponding to kid")))
                .flatMap(encryptionKeyModel -> {
                    var validationResult = keyValidationExecutor.extractKeyAndValidateHeaders(encryptionKeyModel);
                    if (!validationResult.isValid()) {
                        keyValidationReactiveHelper.recordValidationErrorTelemetry(ctx, encryptionKeyModel, validationResult.getValidationError()
                                .getErrorCode());
                        log.error(String.format(FAILED_GET_DK_MSG, namespace),
                                validationResult.getValidationError().getErrorException());
                        return Mono.error(() -> new EncryptionException(
                                String.format(FAILED_GET_DK_MSG, namespace),
                                validationResult.getValidationError().getErrorException()));
                    }
                    return Mono.just(validationResult.getOctetSequenceKey());
                })
        );
    }

    @Override
    public Mono<Boolean> rotateEncryptionKey(String namespace, EncryptionKeyPredicate predicate) {

        // get uncached key
        return crudEncryptionKeyService.getKeyWithFailsafe(namespace, validationFunction)
                .onErrorMap(e -> {

                    if (e instanceof BootResponseException ex) {
                        return ex;
                    }

                    return new EncryptionException(String.format("Unable to obtain any key corresponding to "
                            + "namespace: %s due to an unrecognized error", namespace), e);
                })
                .switchIfEmpty(Mono.error(() -> new MissingKeyException(String.format(
                                            "No encryption key to rotate in %s", namespace))))
                .doOnNext(encryptionKeyModel -> encryptionMetricsRegistry.registerNekRotationAgeDelta(
                        encryptionKeyModel.getNamespace(), encryptionKeyModel.getCreatedAt()))
                .flatMap(encryptionKeyModel -> {
                    // allow rotation of some namespaces skipping the 80 days schedule for testing
                    if (encryptionProperties.getRotation().getAlwaysRotateList().contains(namespace)
                            || (encryptionProperties.getRotation().isEnabled()
                            && predicate.shouldRun(encryptionKeyModel))) {
                        return createEncryptionKey(namespace, EncryptionKeyStatus.PENDING_ROTATION)
                                .map(newKey -> true);
                    }

                    // Rotation predicate did not pass for this NEK and the NEK should not
                    // even register a rotation attempt.
                    return Mono.empty();
                });
    }

    /**
     * Tested with on a paginated sync Cassandra Repository with 100k encryption keys to force
     * rotate that took around ~15 minutes (ignoring last rotation time) on old Data Model 1. Tested
     * on-reactive blocking in Data Model 2 - 100k took around ~40 seconds. Most of the time only
     * several keys out of all the full table scan will need to be rotated and not at that scale.
     * Likely can be optimized more with limitRate and other backpressure mechanics
     */
    @Override
    public Mono<Integer> rotateAllEncryptionKeys(EncryptionKeyPredicate predicate) {
        return Mono.deferContextual(ctx -> {
            // force remove before execution in case there is a NEK that is gone from the DB that would cause the age metric to not get updated
            encryptionMetricsRegistry.removeNekRotationAgeDelta();

            CassandraPageRequest pageRequest = CassandraPageRequest.first(
                    encryptionProperties.getRotation().getScheduled().getFetchSize());

            return crudEncryptionKeyService.performActionOncePerNamespace(pageRequest,
                            encryptionProperties.getRotation().getScheduled().getBackpressurePageCount(),
                            namespace -> rotateEncryptionKey(namespace, predicate)
                                    // report rotation success / failure.
                                    .map(success -> Pair.of(namespace, success))
                                    // single rotation error should not stop continuing other rotations
                                    .onErrorResume(ex -> {
                                        log.error(String.format("Failed to rotate key for namespace %s",
                                                namespace), ex);
                                        // NOTE: An alert-metric is updated inside `rotateEncryptionKey(...)` corresponding to
                                        // the namespace that failed rotation.
                                        encryptionMetricsRegistry.recordNekRotationError(namespace,
                                                ex.getClass().getName());
                                        TracingUtils.recordException(ctx, ex);
                                        TracingUtils.setSpanAttribute(ctx, EK_NAMESPACE_KEY, namespace);

                                        Span.current()
                                                .recordException(ex)
                                                .setAttribute(EK_NAMESPACE_KEY, namespace);
                                        return Mono.just(Pair.of(namespace, false));
                                    })
                    )
                    .collectList()
                    .map(list -> {

                        var successful = list.stream()
                                .filter(x -> Boolean.TRUE.equals(x.getSecond()))
                                .map(Pair::getFirst)
                                .toList();

                        if (!list.isEmpty()) {

                            var failed = list.stream()
                                    .filter(x -> !Boolean.TRUE.equals(x.getSecond()))
                                    .map(Pair::getFirst)
                                    .toList();

                            log.info("Attempted NEK rotation in {} namespaces. Successful: {}. " +
                                    " Failed: {}", list.size(), successful, failed);

                        }

                        return successful.size();
                    });
        });
    }

    @Override
    public Mono<Integer> reencryptAllEncryptionKeys(EncryptionKeyPredicate predicate) {
        return Mono.deferContextual(ctx -> {
            CassandraPageRequest pageRequest = CassandraPageRequest.first(
                    encryptionProperties.getRotation().getScheduled().getFetchSize());

            return crudEncryptionKeyService.performActionOncePerNamespaceKidPair(
                            pageRequest,
                            encryptionProperties.getRotation().getScheduled().getBackpressurePageCount(),
                            validationFunction,
                            nekModel -> {
                                if ((encryptionProperties.getReencryption().getAllowList()
                                        .contains(nekModel.getNamespace())
                                        || encryptionProperties.getReencryption().isEnabled())
                                        && predicate.shouldRun(nekModel)) {
                                    return reEncryptEncryptionKey(nekModel)
                                            .doOnError(e -> {
                                                TracingUtils.recordException(ctx, e);
                                                TracingUtils.setSpanAttribute(ctx, EK_NAMESPACE_KEY, nekModel.getNamespace());
                                                TracingUtils.setSpanAttribute(ctx, EK_KID_KEY, maskKid(nekModel.getKid()));
                                                TracingUtils.setSpanAttribute(ctx, EK_ENCRYPTED_AT_KEY, nekModel.getEncryptedAt().toString());

                                                Span.current().recordException(e)
                                                        .setAttribute(EK_NAMESPACE_KEY, nekModel.getNamespace())
                                                        .setAttribute(EK_KID_KEY, maskKid(nekModel.getKid()))
                                                        .setAttribute(EK_ENCRYPTED_AT_KEY, nekModel.getEncryptedAt().toString());
                                                encryptionMetricsRegistry.recordNekReencryptionError(
                                                        nekModel.getNamespace(), nekModel.getKid(),
                                                        e.getClass().getName());
                                            });
                                }

                                log.debug("Skipping re-encryption for NEK with KID: {}, created_at: {}, " +
                                                "encrypted_at: {}", maskKid(nekModel.getKid()),
                                        nekModel.getCreatedAt(),
                                        nekModel.getEncryptedAt());
                                return Mono.empty();
                            }
                    )
                    .reduce(new HashMap<String, Integer>(), (keyCounts, nekReencryption) -> {
                        keyCounts.put(nekReencryption.getNamespace(),
                                keyCounts.getOrDefault(nekReencryption.getNamespace(), 0) + 1);
                        return keyCounts;
                    })
                    .map(keyCounts -> {
                        var totalCount = 0;
                        for (var count : keyCounts.entrySet()) {
                            totalCount += count.getValue();
                            log.info("Re-encrypted: {} NEKs in namespace: {}", count.getValue(),
                                    count.getKey());
                        }
                        return totalCount;
                    });
        });
    }

    private Mono<EncryptionKeyModel> createEncryptionKey(String namespace,
            EncryptionKeyStatus status) {
        return Mono.deferContextual(ctx -> {
            EncryptionKeyV2Model model;
            try {
                OctetSequenceKey newKey = EncryptionKeyGenerator.generateEncryptionKey();
                OctetSequenceKey mek = cryptoPropertiesHolder.get().getValidMek();
                UUID createdAtUUID = Uuids.timeBased();
                Instant encryptedAt = Instant.ofEpochMilli(Uuids.unixTimestamp(createdAtUUID));

                IntegrityChecksPopulationFields icFFields =
                        new IntegrityChecksPopulationFields(namespace, createdAtUUID, encryptedAt);
                var encryptedKey = mekService.encryptWithIntegrityCheck(mek,
                        newKey.getKeyValue().toString(), icFFields);

                model = EncryptionKeyV2Model.builder()
                        .createdAt(createdAtUUID)
                        .namespace(namespace)
                        .kid(newKey.getKeyID())
                        .status(status.name())
                        .currentKid(newKey.getKeyID())
                        .encryptedKey(encryptedKey)
                        .encryptedByKid(mek.getKeyID())
                        .encryptedAt(encryptedAt)
                        .build();
            } catch (NoSuchAlgorithmException | JOSEException e) {
                log.error(String.format(FAILED_ENCRYPT_EK_MSG, namespace), e);
                return Mono.error(
                        () -> new EncryptionException(
                                String.format(FAILED_ENCRYPT_EK_MSG, namespace),
                                e));
            }

            if (EncryptionKeyStatus.CREATION_VALIDATED == status) {
                var validationResult = keyValidationExecutor.validate(model);
                if (!validationResult.isValid()) {
                    keyValidationReactiveHelper.recordValidationErrorTelemetry(ctx, model.toEncryptionKeyByKidModel(), validationResult.getValidationError()
                            .getErrorCode());
                    return Mono.error(validationResult.getValidationError().getErrorException());
                }
            }

            return crudEncryptionKeyService.addKey(model)
                    .flatMap(persisted -> {
                        if (Boolean.FALSE.equals(persisted)) {
                            log.error("Conflict error: failed to create key. Please try again");
                            return Mono.error(() -> new EncryptionException(
                                    "Unexpected error: failed to create key. Please try again"));
                        }
                        return Mono.just(model.toEncryptionKeyByKidModel());
                    });
        });
    }

    private Mono<EncryptionKeyV2Model> reEncryptEncryptionKey(EncryptionKeyModel keyModel) {
        return Mono.deferContextual(ctx -> {
            EncryptionKeyV2Model model;
            var validationResult = keyValidationExecutor.extractKeyAndValidateHeaders(keyModel);
            if (!validationResult.isValid()) {
                keyValidationReactiveHelper.recordValidationErrorTelemetry(ctx, keyModel, validationResult.getValidationError()
                        .getErrorCode());
                log.error(String.format(FAILED_REENCRYPT_EK_MSG, keyModel.getNamespace()),
                        validationResult.getValidationError().getErrorException());
                return Mono.error(() -> new EncryptionException(
                        String.format(FAILED_REENCRYPT_EK_MSG, keyModel.getNamespace()),
                        validationResult.getValidationError().getErrorException()));
            }
            OctetSequenceKey decryptedKey = validationResult.getOctetSequenceKey();

            OctetSequenceKey mek = cryptoPropertiesHolder.get().getValidMek();
            Instant encryptedAt = Instant.ofEpochMilli(Uuids.unixTimestamp(Uuids.timeBased()));

            IntegrityChecksPopulationFields icFFields =
                    new IntegrityChecksPopulationFields(keyModel.getNamespace(),
                            keyModel.getCreatedAt(), encryptedAt);
            try {
                var encryptedKey = mekService.encryptWithIntegrityCheck(mek,
                        decryptedKey.getKeyValue().toString(),
                        icFFields);

                model = EncryptionKeyV2Model.builder()
                        .createdAt(keyModel.getCreatedAt())
                        .namespace(keyModel.getNamespace())
                        .kid(keyModel.getKid())
                        // If status != VALIDATED | CREATION_VALIDATED, and the destination table is
                        // NEKv2 then the value of `current_kid` is ignored and not persisted by
                        // `addKey(...)` below.
                        .currentKid(keyModel.getKid())
                        .status(EncryptionKeyStatus.PENDING_REENCRYPTION.name())
                        .encryptedKey(encryptedKey)
                        .encryptedByKid(mek.getKeyID())
                        .encryptedAt(encryptedAt)
                        .build();
            } catch (JOSEException e) {
                log.error(String.format(FAILED_REENCRYPT_EK_MSG, keyModel.getNamespace()), e);
                return Mono.error(
                        () -> new EncryptionException(
                                String.format(FAILED_REENCRYPT_EK_MSG, keyModel.getNamespace()),
                                e));
            }

            return crudEncryptionKeyService.addKey(model)
                    .flatMap(persisted -> {
                        if (Boolean.FALSE.equals(persisted)) {
                            log.error("Conflict error: failed to create key. Please try again");
                            return Mono.error(() -> new EncryptionException(
                                    "Unexpected error: failed to create key. Please try again"));
                        }
                        return Mono.just(model);
                    });
        });
    }

    private boolean validateExtractedKeyAndHeaders(EncryptionKeyModel model, Consumer<KeyFetchError> errConsumer) {
        var validationResultWithKey = keyValidationExecutor.extractKeyAndValidateHeaders(model);

        if (!validationResultWithKey.isValid()) {
            errConsumer.accept(validationResultWithKey.getValidationError());
        }
        return validationResultWithKey.isValid();
    }

}
