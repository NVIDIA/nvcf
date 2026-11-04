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
package com.nvidia.ess.encryption.crypto;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mockStatic;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.Payload;
import com.nimbusds.jose.crypto.DirectEncrypter;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.integrity.IntegrityChecks;
import com.nvidia.ess.encryption.integrity.IntegrityChecksPopulationFields;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockedStatic;
import org.mockito.Mockito;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;

@Slf4j
@ExtendWith(MockitoExtension.class)
class MekServiceTest {

    @Spy
    @InjectMocks
    private MekService mekService = new MekService();

    @Mock
    private IntegrityChecks integrityChecks;

    @Test
    void crypto_EncryptWithIntegrityCheck_Success() throws JOSEException {
        // Arrange
        String testKey = "TestKey";
        String expectedEncryptedData = "encryptedTestKey";
        JWEHeader.Builder mockdBuilder = Mockito.mock(JWEHeader.Builder.class);
        IntegrityChecksPopulationFields mockIcFields = Mockito.mock(IntegrityChecksPopulationFields.class);
        OctetSequenceKey mockedKey = Mockito.mock(OctetSequenceKey.class);
        Payload mockedPayload = Mockito.mock(Payload.class);
        JWEObject mockedJWEObject = Mockito.mock(JWEObject.class);
        JWEHeader mockedHeader = Mockito.mock(JWEHeader.class);
        DirectEncrypter mockedDirectEncrypter = Mockito.mock(DirectEncrypter.class);

        doReturn(mockedPayload).when(mekService).getPayload(testKey);
        doReturn(mockedJWEObject).when(mekService).getJWEObject(mockedHeader, mockedPayload);
        doReturn(mockedDirectEncrypter).when(mekService).getDirectEncrypter(mockedKey);
        doReturn(mockdBuilder).when(integrityChecks).populateIfEnabled(mockdBuilder, mockIcFields);

        when(mockdBuilder.build()).thenReturn(mockedHeader);
        doNothing().when(mockedJWEObject).encrypt(mockedDirectEncrypter);
        when(mockedJWEObject.serialize()).thenReturn(expectedEncryptedData);

        // Mock the static method getJWEBuilder
        try (MockedStatic<CryptoService> mockedStatic = mockStatic(CryptoService.class)) {

            mockedStatic.when(() -> CryptoService.getJWEBuilder(mockedKey, testKey)).thenReturn(mockdBuilder);
            String result = mekService.encryptWithIntegrityCheck(mockedKey, testKey, mockIcFields);

            // verify calling of helper methods
            mockedStatic.verify(() -> CryptoService.getJWEBuilder(mockedKey, testKey));
            verify(integrityChecks).populateIfEnabled(mockdBuilder, mockIcFields);
            verify(mockdBuilder).build();
            verify(mekService).getPayload(testKey);
            verify(mekService).getJWEObject(mockedHeader, mockedPayload);
            verify(mockedJWEObject).encrypt(any(DirectEncrypter.class));
            verify(mockedJWEObject).serialize();

            // Assert
            assertEquals(expectedEncryptedData, result);
        }


    }
}
