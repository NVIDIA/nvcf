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
package com.nvidia.ess.encryption.crypto.key.validation;


import com.google.common.annotations.VisibleForTesting;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.MekService;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationResultWithKey;
import com.nvidia.ess.encryption.exceptions.IntegrityChecksValidationException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.KeyValidationException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import jakarta.annotation.PostConstruct;
import java.text.ParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.function.UnaryOperator;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

@Component
@Slf4j
public class KeyValidationExecutor {

    // potentially there can be a list of validators on the model itself as well
    @Setter(onMethod_ = {@Autowired})
    private List<OctetSequenceKeyValidator> octetJwkValidators = new ArrayList<>();

    @Setter(onMethod_ = {@Autowired})
    private MekService mekService;


    @PostConstruct
    public void init() {
        log.info("Registered {} NEK validators, {}", octetJwkValidators.size(), octetJwkValidators
                .stream()
                .map(validator -> validator.getClass().getName())
                .toList());
    }

    public KeyValidationResult validate(EncryptionKeyV2Model model) {
        KeyValidationResultWithKey keyExtractionResult = extractKeyAndValidateHeaders(model);
        if (!keyExtractionResult.isValid()) {
            // sending metric in extractKeyAndValidateHeaders instead since it is called from other flows too
            return KeyValidationResult.failure(keyExtractionResult.getValidationError());
        }
        OctetSequenceKey octetSequenceKey = keyExtractionResult.getOctetSequenceKey();
        UnaryOperator<String> errMsgFormatter = model.logMessageFormatter();

        for (var validator : getOctetJwkValidators()) {
            KeyValidationResult validationResult = validator.validate(octetSequenceKey, errMsgFormatter);
            if (!validationResult.isValid()) {
                return validationResult;
            }
        }

        return KeyValidationResult.success();
    }

    public KeyValidationResultWithKey extractKeyAndValidateHeaders(EncryptionKeyV2Model model) {
        return extractKeyAndValidateHeaders(model.toEncryptionKeyByKidModel());
    }

    public KeyValidationResultWithKey extractKeyAndValidateHeaders(EncryptionKeyModel model) {
        var errMsgWithNekStr = model.logMessageFormatter();
        try {
            return KeyValidationResultWithKey.success(mekService.extractKey(model));
        } catch (MissingMasterKeyException e) {
            log.error(errMsgWithNekStr.apply("Failed encryption key validation: missing MEK."), e);
            var errorCode = KeyFetchErrorCode.ENCRYPTING_KEY_UNAVAILABLE;

            return KeyValidationResultWithKey.failure(new KeyFetchError(errorCode, e));
        } catch (ParseException e) {
            log.error(errMsgWithNekStr.apply("Failed encryption key validation: unable to parse JWE."), e);
            // likely scenario is missing MEK or corrupted ciphertext
            var errorCode = KeyFetchErrorCode.JWE_PARSE_ERROR;

            return KeyValidationResultWithKey.failure(new KeyFetchError(errorCode,
                    new KeyValidationException("JWE parse error", e)));
        } catch (JOSEException e) {
            log.error(errMsgWithNekStr.apply("Failed encryption key validation: decryption error."), e);
            var errorCode = KeyFetchErrorCode.KEY_DECRYPTION_ERROR;

            return KeyValidationResultWithKey.failure(new KeyFetchError(errorCode,
                    new KeyValidationException("Error while decrypting the encryption key", e)));
        } catch (IntegrityChecksValidationException e) {
            log.error(errMsgWithNekStr.apply("Failed encryption key validation: integrity checks."), e);
            var errorCode = KeyFetchErrorCode.INTEGRITY_CHECKS_ERROR;

            return KeyValidationResultWithKey.failure(new KeyFetchError(errorCode,
                    new KeyValidationException("Integrity check error", e)));
        }
    }

    @VisibleForTesting
    public List<OctetSequenceKeyValidator> getOctetJwkValidators() {
        return this.octetJwkValidators;
    }
}
