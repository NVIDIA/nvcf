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


import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mockStatic;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import java.text.ParseException;
import java.util.List;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.MockedStatic;
import org.mockito.junit.jupiter.MockitoExtension;

@Slf4j
@ExtendWith(MockitoExtension.class)
class EncryptionOperationsValidatorTest {

    private EncryptionOperationsValidator encryptionOperationsValidator;

    private EncryptionProperties encryptionProperties;

    @BeforeEach
    void setup() {
        encryptionProperties = new EncryptionProperties();
        encryptionOperationsValidator = new EncryptionOperationsValidator();
        encryptionOperationsValidator.setEncryptionProperties(encryptionProperties);
    }


    private static Stream<Arguments> validationPayloads() {
        return Stream.of(
                Arguments.of("test"),
                Arguments.of("{\"data\": {\"field1\": \"asd\", \"field2\": {\"subfield3\": \"agoaldksglgsaldsagoaldksglgsaldsagoaldksglgsaldsagoaldksglgsaldsagoa\"}}}"),
                Arguments.of("\uDB9A\uDE02䏜牪ɍ\uDBA6\uDDC0\uD927\uDE0Dރ\uDBEC\uDDFE\uDA05\uDC82d\uDA18\uDCCE\uDAB0\uDD4ET⋽궁ꅡӀĊhҾE\uE2BE\uD999\uDC81Ĩٓ⣹7ݝÆ\uDBB4\uDDDC伧\uDA33\uDEF5")
        );
    }

    @ParameterizedTest
    @MethodSource("validationPayloads")
    void validate_onDifferentPayloads_returnsTrue(String payload) {
        encryptionProperties.getPromotion().setValidationPayloads(List.of(payload));
        OctetSequenceKey octetSequenceKey = CryptoTestUtils.generateEncryptionKey();

        var validationResult = encryptionOperationsValidator.validate(octetSequenceKey, msg -> msg);

        Assertions.assertTrue(validationResult.isValid());
        Assertions.assertNull(validationResult.getValidationError());
    }

    @Test
    void validate_onJOSEException_emitsPromotionValidationErrorCode() {
        OctetSequenceKey octetSequenceKey = CryptoTestUtils.generateEncryptionKey();
        encryptionProperties.getPromotion().setValidationPayloads(List.of("test"));

        try (MockedStatic<CryptoService> mockedStatic = mockStatic(CryptoService.class)) {
            mockedStatic.when(() -> CryptoService.encrypt(any(OctetSequenceKey.class), anyString()))
                    .thenThrow(new JOSEException(""));

            var validationResult = encryptionOperationsValidator.validate(octetSequenceKey, msg -> msg);

            Assertions.assertFalse(validationResult.isValid());
            Assertions.assertNotNull(validationResult.getValidationError());
            Assertions.assertEquals(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR, validationResult.getValidationError().getErrorCode());
        }
    }

    @Test
    void validate_onParseException_emitsPromotionValidationErrorCode() {
        OctetSequenceKey octetSequenceKey = CryptoTestUtils.generateEncryptionKey();
        encryptionProperties.getPromotion().setValidationPayloads(List.of("test"));

        try (MockedStatic<CryptoService> mockedStatic = mockStatic(CryptoService.class)) {
            mockedStatic.when(() -> CryptoService.encrypt(any(OctetSequenceKey.class), anyString()))
                    .thenCallRealMethod();
            mockedStatic.when(() -> CryptoService.getJWEBuilder(any(OctetSequenceKey.class), anyString()))
                    .thenCallRealMethod();

            mockedStatic.when(() -> CryptoService.decrypt(any(OctetSequenceKey.class), anyString()))
                    .thenThrow(new ParseException("", 0));
            var validationResult = encryptionOperationsValidator.validate(octetSequenceKey, msg -> msg);

            Assertions.assertFalse(validationResult.isValid());
            Assertions.assertNotNull(validationResult.getValidationError());
            Assertions.assertEquals(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR, validationResult.getValidationError().getErrorCode());
        }
    }


    @Test
    void validate_onUnequalPlaintext_emitsPromotionValidationErrorCode() {
        OctetSequenceKey octetSequenceKey = CryptoTestUtils.generateEncryptionKey();
        encryptionProperties.getPromotion().setValidationPayloads(List.of("test"));

        try (MockedStatic<CryptoService> mockedStatic = mockStatic(CryptoService.class)) {
            mockedStatic.when(() -> CryptoService.encrypt(any(OctetSequenceKey.class), anyString()))
            .thenCallRealMethod();
            mockedStatic.when(() -> CryptoService.getJWEBuilder(any(OctetSequenceKey.class), anyString()))
                    .thenCallRealMethod();
    
            mockedStatic.when(() -> CryptoService.decrypt(any(OctetSequenceKey.class), anyString()))
                    .thenReturn("something else");

            var validationResult = encryptionOperationsValidator.validate(octetSequenceKey, msg -> msg);

            Assertions.assertFalse(validationResult.isValid());
            Assertions.assertNotNull(validationResult.getValidationError());
            Assertions.assertEquals(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR, validationResult.getValidationError().getErrorCode());
        }
    }
}
