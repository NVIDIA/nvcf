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

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.KeyValidationException;
import java.text.ParseException;
import java.util.function.UnaryOperator;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.Strings;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

@Component
@Slf4j
public class EncryptionOperationsValidator implements OctetSequenceKeyValidator {

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    @Override
    public KeyValidationResult validate(OctetSequenceKey key, UnaryOperator<String> errorMessageFormatter) {
        for (String payload : encryptionProperties.getPromotion()
                .getValidationPayloads()) {
            var onePayloadResult = validateAgainstPayload(key, payload, errorMessageFormatter);
            if (!onePayloadResult.isValid()) {
                return onePayloadResult;
            }
        }
        return KeyValidationResult.success();
    }


    private KeyValidationResult validateAgainstPayload(OctetSequenceKey key, String payload,
            UnaryOperator<String> errorMessageFormatter) {
        log.debug("validating {} against {}", key.getKeyID(), payload);
        try {
            String ciphertext = CryptoService.encrypt(key, payload);
            String plaintext = CryptoService.decrypt(key, ciphertext);
            boolean isEqual = Strings.CS.equals(payload, plaintext);
            if (!isEqual) {
                log.error(errorMessageFormatter.apply(String.format(
                        "Input %s does not match output %s after encrypt&decrypt operations",
                        payload, plaintext)));
                return KeyValidationResult.failure(new KeyFetchError(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR,
                        new KeyValidationException(
                                "Encrypt/Decrypt payload validation failed during promotion")));
            }
            return KeyValidationResult.success();
        } catch (JOSEException | ParseException e) {
            log.error(errorMessageFormatter.apply(
                            String.format("Encryption/Decryption validation failed against %s", payload)),
                    e);

            return KeyValidationResult.failure(new KeyFetchError(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR,
                    new KeyValidationException(
                            "Unable to encrypt or decrypt a payload during promotion")));
        }
    }
}
