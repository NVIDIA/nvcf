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

import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationReactiveHelper;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.util.TracingUtils;
import io.opentelemetry.api.trace.Span;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.Pair;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class EncryptionKeyPromotionService {

    @Setter(onMethod_ = {@Autowired})
    private KeyValidationExecutor keyValidationExecutor;

    @Setter(onMethod_ = {@Autowired})
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @Setter(onMethod_ = {@Autowired})
    protected EncryptionProperties encryptionProperties;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    private KeyValidationReactiveHelper keyValidationReactiveHelper;

    public Mono<Pair<Integer, Integer>> promoteAllEncryptionKeys() {

        if (!encryptionProperties.getPromotion().isEnabled() &&
                encryptionProperties.getPromotion().getAllowList().isEmpty()) {
            // Promotion is globally disabled and namespace-specific allow-list
            // is empty too.
            log.info("Skipping NEK promotion as it is disabled at a global level and is not " +
                    "allow-listed for any namespace in particular");
            return Mono.just(Pair.of(0, 0));
        }

        return Mono.deferContextual(ctx ->
                crudEncryptionKeyService.findAllV2Keys(
                        encryptionProperties.getPromotion().getScheduled().getFetchSize(),
                        encryptionProperties.getPromotion().getScheduled().getBackpressurePageCount())
                // use concatmap to serialize writes to the same partition
                .concatMap(model -> {
                            var logMsgFormatter = model.logMessageFormatter();
                            return promoteEncryptionKey(model)
                                    .onErrorResume(e -> {
                                        log.error(logMsgFormatter.apply("Failed to promote NEK"), e);
                                        TracingUtils.recordException(ctx, e);
                                        TracingUtils.setSpanAttribute(ctx, EK_NAMESPACE_KEY, model.getNamespace());
                                        TracingUtils.setSpanAttribute(ctx, EK_KID_KEY, maskKid(model.getKid()));
                                        TracingUtils.setSpanAttribute(ctx, EK_ENCRYPTED_AT_KEY, model.getEncryptedAt().toString());
                                        Span.current().recordException(e)
                                                .setAttribute(EK_NAMESPACE_KEY, model.getNamespace())
                                                .setAttribute(EK_KID_KEY, maskKid(model.getKid()))
                                                .setAttribute(EK_ENCRYPTED_AT_KEY, model.getEncryptedAt().toString());
                                        encryptionMetricsRegistry.recordNekPromotionError(
                                                model.getNamespace(), model.getKid(), e.getClass().getName());
                                        return Mono.just(false);
                                    });
                        }
                )
                .reduce(
                        // accumulate count of successful and failed promotions
                        Pair.of(0, 0),
                        (pair, bool) -> {
                            if (Boolean.TRUE.equals(bool)) {
                                return Pair.of(pair.getLeft() + 1, pair.getRight());
                            }
                            return Pair.of(pair.getLeft(), pair.getRight() + 1);
                        }
                )
        );
    }


    // 3 possible outputs
    // 1. Mono.empty() - already promoted. Simpler to differentiate bet
    // 2. Mono.error() - promotion failed (validation, etc.)
    // 3. Has a value - successfully promoted.
    //
    // Technically cannot be False.
    // If failed validation, need to propagate error in case this method will be exposed to the API, thus cannot be expressed as False
    // Mono<Void> type does not work either because there is a need to distinguish between Promoted and Skipped NEKs
    public Mono<Boolean> promoteEncryptionKey(EncryptionKeyV2Model model) {
        return Mono.deferContextual(ctx -> {
            if (EncryptionKeyStatus.VALIDATED.name().equals(model.getStatus())) {
                // skip, already promoted
                return Mono.empty();
            }

            if (!encryptionProperties.getPromotion().isEnabled() &&
                    !encryptionProperties.getPromotion().getAllowList().contains(model.getNamespace())) {
                // Skip promotion of this NEK as promotion is disabled globally and the namespace is not
                // allow-listed.
                if (log.isDebugEnabled()) {
                    log.debug(model.logMessageFormatter().apply("Skipping promotion of NEK (disabled for namespace)"));
                }
                return Mono.empty();
            }

            var validationResult = keyValidationExecutor.validate(model);
            if (!validationResult.isValid()) {
                keyValidationReactiveHelper.recordValidationErrorTelemetry(ctx, model.toEncryptionKeyByKidModel(), validationResult.getValidationError()
                        .getErrorCode());
                return Mono.error(validationResult.getValidationError().getErrorException());
            }

            // should be ~1-2 PENDING_ROTATION NEKs for the same namespace
            // if promotion fails, then more PENDING_ROTATION NEKs can accumulate
            //  because only Promotion is responsible for setting the current_kid for rotated NEKs
            // ordering does not matter as none of PENDING_ROTATION NEKs were used for cryptographic operations.
            // rotation schedule might be off due to ordering, but should not affect operations in general
            Mono<Boolean> updateMono;
            if (EncryptionKeyStatus.PENDING_ROTATION.name().equals(model.getStatus())) {

                // not being used internally, but still setting in case implementation changes
                model.setCurrentKid(model.getKid());
                model.setStatus(EncryptionKeyStatus.VALIDATED.name());
                updateMono = crudEncryptionKeyService.promoteRotationKey(model);
            } else {
                model.setStatus(EncryptionKeyStatus.VALIDATED.name());
                updateMono = crudEncryptionKeyService.promoteKey(model);
            }
            return updateMono
                    .flatMap(wasApplied -> {
                        if (!Boolean.TRUE.equals(wasApplied)) {
                            return Mono.error(new EncryptionException("Failed to promote NEK's status"));
                        }
                        log.info(model.logMessageFormatter().apply("Successfully promoted NEKv2"));
                        return Mono.just(true);
                    })
                    // explicit check if it was wrapped in an EncryptionException already (no subclasses)
                    .onErrorMap(e -> !EncryptionException.class.equals(e.getClass()), e ->
                        new EncryptionException("Unexpected error updating promote NEK's status", e)
                    );
        });
    }
}
