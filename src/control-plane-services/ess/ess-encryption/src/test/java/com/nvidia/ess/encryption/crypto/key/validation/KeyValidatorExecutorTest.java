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
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JOSEException;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.MekService;
import com.nvidia.ess.encryption.exceptions.IntegrityChecksValidationException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import com.nvidia.ess.encryption.exceptions.shaded.BootResponseException;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import java.text.ParseException;
import java.util.List;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.HttpStatus;

@Slf4j
@ExtendWith(MockitoExtension.class)
class KeyValidatorExecutorTest {

    private KeyValidationExecutor keyValidationExecutor;

    @Mock
    private MekService mekService;

    @Mock
    private OctetSequenceKeyValidator octetSequenceKeyValidator;

    @Mock
    private EncryptionKeyV2Model model;

    @Mock
    private EncryptionKeyModel modelV1;

    @BeforeEach
    void setup() {
        keyValidationExecutor = new KeyValidationExecutor();
        keyValidationExecutor.setOctetJwkValidators(List.of(octetSequenceKeyValidator));
        keyValidationExecutor.setMekService(mekService);
    }


    private static Stream<Arguments> errorAndKeyFetchErrorCode() {
        return Stream.of(
                Arguments.of(new MissingMasterKeyException(), KeyFetchErrorCode.ENCRYPTING_KEY_UNAVAILABLE),
                Arguments.of(new ParseException("", 0), KeyFetchErrorCode.JWE_PARSE_ERROR),
                Arguments.of(new JOSEException(""), KeyFetchErrorCode.KEY_DECRYPTION_ERROR),
                Arguments.of(new IntegrityChecksValidationException(), KeyFetchErrorCode.INTEGRITY_CHECKS_ERROR)
        );
    }

    @ParameterizedTest
    @MethodSource("errorAndKeyFetchErrorCode")
    void extractKeyIfValidated_onException_emitsKeyFetchErrorCode(Exception e, KeyFetchErrorCode errorCode)
            throws ParseException, JOSEException {
        when(model.toEncryptionKeyByKidModel())
                .thenReturn(modelV1);
        when(modelV1.logMessageFormatter())
                .thenReturn(msg -> msg);
        when(mekService.extractKey(any(EncryptionKeyModel.class)))
                .thenThrow(e);

        var validationResultWithKey = keyValidationExecutor.extractKeyAndValidateHeaders(model);

        Assertions.assertFalse(validationResultWithKey.isValid());
        Assertions.assertNull(validationResultWithKey.getOctetSequenceKey());
        Assertions.assertNotNull(validationResultWithKey.getValidationError());
        Assertions.assertEquals(errorCode, validationResultWithKey.getValidationError().getErrorCode());
    }


    @SneakyThrows
    @ParameterizedTest
    @ValueSource(booleans = {true, false})
    void validate_onFailingValidator_returnsFalse(boolean isValid) {
        when(model.logMessageFormatter())
                .thenReturn(msg -> msg);
        when(model.toEncryptionKeyByKidModel())
                .thenReturn(modelV1);
        when(modelV1.logMessageFormatter())
                .thenReturn(msg -> msg);
        when(mekService.extractKey(any(EncryptionKeyModel.class)))
                .thenReturn(CryptoTestUtils.generateMasterEncryptionKey());

        KeyValidationResult expectedValidationResult;
        if (isValid) {
            expectedValidationResult = KeyValidationResult.success();
        } else {
            expectedValidationResult = KeyValidationResult.failure(new KeyFetchError(
                    KeyFetchErrorCode.INTERNAL_KEY_FETCH_EXECUTION_ERROR,
                    new BootResponseException(HttpStatus.INTERNAL_SERVER_ERROR, "", BootResponseException.class) {
                    }));
        }

        when(octetSequenceKeyValidator.validate(any(), any()))
                .thenReturn(expectedValidationResult);

        Assertions.assertEquals(expectedValidationResult, keyValidationExecutor.validate(model));
    }

}
