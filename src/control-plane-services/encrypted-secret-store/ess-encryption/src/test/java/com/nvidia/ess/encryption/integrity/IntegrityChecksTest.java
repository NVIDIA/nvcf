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
package com.nvidia.ess.encryption.integrity;


import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.datastax.driver.core.utils.UUIDs;
import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.Constants;
import com.nvidia.ess.encryption.constants.IntegrityChecksKeys;
import com.nvidia.ess.encryption.exceptions.IntegrityChecksValidationException;
import java.text.ParseException;
import java.time.Duration;
import java.time.Instant;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockedStatic;
import org.mockito.Mockito;
import org.mockito.MockitoAnnotations;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;

@Slf4j
@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.LENIENT)
class IntegrityChecksTest {

    @Mock
    private EncryptionProperties encryptionProperties;

    @Mock
    private EncryptionProperties.IntegrityChecksProperties integrityChecksProperties;

    @Mock
    private JWEHeader.Builder builder;

    @Mock
    private IntegrityChecksPopulationFields icPopulationFields;

    @Mock
    private IntegrityChecksValidationFields icValidationFields;

    @Mock
    private JWEObject jweObject;

    @Mock
    private JWEHeader jweHeader;

    @Mock
    private OctetSequenceKey decryptedNEK;

    @InjectMocks
    private IntegrityChecks integrityChecksComponent;

    private static final UUID CREATED_AT = Uuids.timeBased();
    private static final Instant ENCRYPTED_AT = Instant.now();
    private static final String ENCRYPTED_NEK = "encryptedNEK";
    private static final String BAD_ENCRYPTED_NEK = "badEncryptedNEK";
    private static final String TEST_NAMESPACE = "testNamespace";
    private static final String TEST_KID = "testKid";
    private static final String TEST_ENCRYPTED_BY_KID = "testEncryptedByKid";

    private MockedStatic<JWEObject> mockedStatic;
    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);
        when(encryptionProperties.getIntegrityChecks()).thenReturn(integrityChecksProperties);
        mockedStatic = Mockito.mockStatic(JWEObject.class);
        mockedStatic.when(() -> JWEObject.parse(anyString())).thenReturn(jweObject);
    }

    @AfterEach
    void tearDown() {
        mockedStatic.close();
    }

    @Test
    void populateIfEnabled_WhenPopulationEnabled_ShouldPopulateBuilder() {
        when(integrityChecksProperties.isPopulationEnabled()).thenReturn(true);

        when(icPopulationFields.namespace()).thenReturn(TEST_NAMESPACE);
        when(icPopulationFields.createdAt()).thenReturn(CREATED_AT);
        when(icPopulationFields.encryptedAt()).thenReturn(ENCRYPTED_AT);

        when(builder.customParam(IntegrityChecksKeys.VERSION, IntegrityChecksKeys.VERSION_1)).thenReturn(builder);
        when(builder.customParam(IntegrityChecksKeys.NAMESPACE, TEST_NAMESPACE)).thenReturn(builder);
        when(builder.customParam(IntegrityChecksKeys.CREATED_AT, CREATED_AT.toString())).thenReturn(builder);
        when(builder.customParam(IntegrityChecksKeys.ENCRYPTED_AT, ENCRYPTED_AT.toEpochMilli())).thenReturn(builder);

        JWEHeader.Builder result = integrityChecksComponent.populateIfEnabled(builder, icPopulationFields);

        verify(builder).customParam(IntegrityChecksKeys.VERSION, IntegrityChecksKeys.VERSION_1);
        verify(builder).customParam(IntegrityChecksKeys.NAMESPACE, TEST_NAMESPACE);
        verify(builder).customParam(IntegrityChecksKeys.CREATED_AT, CREATED_AT.toString());
        verify(builder).customParam(IntegrityChecksKeys.ENCRYPTED_AT, ENCRYPTED_AT.toEpochMilli());
        Assertions.assertEquals(builder, result);
    }

    @Test
    void populateIfEnabled_WhenPopulationDisabled_ShouldResultInNoInteraction() {
        when(integrityChecksProperties.isPopulationEnabled()).thenReturn(false);
        JWEHeader.Builder result = integrityChecksComponent.populateIfEnabled(builder, icPopulationFields);
        verifyNoInteractions(builder);
        assertEquals(builder, result);
    }

    @Test
    void validateIfEnabled_WhenValidationDisabled_ShouldResultInNoInteraction() throws ParseException {
        when(integrityChecksProperties.isValidationEnabled()).thenReturn(false);
        integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK, decryptedNEK, icValidationFields);
        verifyNoInteractions(jweObject);
        verifyNoInteractions(jweHeader);
    }


    // mockSetup
    private void mockSetup() {
        when(integrityChecksProperties.isValidationEnabled()).thenReturn(true);
        when(jweObject.getHeader()).thenReturn(jweHeader);
        when(jweHeader.getCustomParam(IntegrityChecksKeys.VERSION)).thenReturn(IntegrityChecksKeys.VERSION_1);
        when(jweHeader.getCustomParam(IntegrityChecksKeys.NAMESPACE)).thenReturn(TEST_NAMESPACE);
        when(jweHeader.getCustomParam(IntegrityChecksKeys.CREATED_AT)).thenReturn(CREATED_AT.toString());
        when(jweHeader.getCustomParam(IntegrityChecksKeys.ENCRYPTED_AT)).thenReturn(ENCRYPTED_AT.toEpochMilli());
        when(jweHeader.getKeyID()).thenReturn(TEST_ENCRYPTED_BY_KID);
        when(decryptedNEK.getKeyID()).thenReturn(TEST_KID);

        when(icValidationFields.namespace()).thenReturn(TEST_NAMESPACE);
        when(icValidationFields.kid()).thenReturn(TEST_KID);
        when(icValidationFields.createdAt()).thenReturn(CREATED_AT);
        when(icValidationFields.encryptedByKid()).thenReturn(TEST_ENCRYPTED_BY_KID);
        when(icValidationFields.encryptedAt()).thenReturn(ENCRYPTED_AT);

    }

    public static Stream<Arguments> failedNamespaceParameters() {
        return Stream.of(
                Arguments.of("invalidNamespace", TEST_NAMESPACE),
                Arguments.of("", TEST_NAMESPACE),
                Arguments.of(null, TEST_NAMESPACE),
                Arguments.of(123, TEST_NAMESPACE),
                Arguments.of(123L, TEST_NAMESPACE),
                Arguments.of(123.32, TEST_NAMESPACE),
                Arguments.of(Instant.now(), TEST_NAMESPACE),
                Arguments.of(UUID.randomUUID(), TEST_NAMESPACE)
        );
    }

    @ParameterizedTest
    @MethodSource("failedNamespaceParameters")
    void validateIfEnabled_WhenNamespaceMissing_ShouldThrowException(Object invalidNamespace, String expectedNamespace)  {
        mockSetup();
        //override
        // actual
        when(jweHeader.getCustomParam(IntegrityChecksKeys.NAMESPACE)).thenReturn(invalidNamespace);
        // expected
        when(icValidationFields.namespace()).thenReturn(expectedNamespace);

        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        if (Objects.isNull(invalidNamespace)) {
            assertEquals(String.format(Constants.MSG_OBJ_NULL, IntegrityChecksKeys.NAMESPACE), exception.getBody().getDetail());
        } else if (invalidNamespace instanceof String) {
            assertEquals(String.format(Constants.MSG_NAMESPACE_MISMATCHED, invalidNamespace,
                    expectedNamespace), exception.getBody().getDetail());
        } else {
            assertEquals(String.format(Constants.MSG_TYPE_MISMATCH, IntegrityChecksKeys.NAMESPACE, invalidNamespace.getClass().getName(), expectedNamespace.getClass().getName()), exception.getBody().getDetail());
        }
    }

    public static Stream<Arguments> failedKidParameters() {
        return Stream.of(
                Arguments.of("invalidKid", TEST_KID),
                Arguments.of("", TEST_KID),
                Arguments.of(null, TEST_KID)
        );
    }

    @ParameterizedTest
    @MethodSource("failedKidParameters")
    void validateIfEnabled_WhenKidMismatched_ShouldThrowException(String badKid, String expectedKid)  {
        mockSetup();

        // override
        // actual
        when(decryptedNEK.getKeyID()).thenReturn(badKid);
        // expected
        when(icValidationFields.kid()).thenReturn(expectedKid);

        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        if (Objects.isNull(badKid)) {
            assertEquals(String.format(Constants.MSG_OBJ_NULL, IntegrityChecksKeys.KID), exception.getBody().getDetail());
        } else {
            assertEquals(String.format(Constants.MSG_KID_MISMATCHED, badKid, TEST_KID),
                    exception.getBody().getDetail());
        }
    }

    public static Stream<Arguments> failedEncryptedByKidParameters() {
        return Stream.of(
                Arguments.of("invalidEncryptedByKid", TEST_ENCRYPTED_BY_KID),
                Arguments.of("", TEST_ENCRYPTED_BY_KID),
                Arguments.of(null, TEST_ENCRYPTED_BY_KID)
        );
    }


    @ParameterizedTest
    @MethodSource("failedEncryptedByKidParameters")
    void validateIfEnabled_WhenEncryptedByKidMismatched_ShouldThrowException(String badEncryptedByKid, String expectedEncryptedByKid)  {

        mockSetup();

        // override
        // actual
        when(jweHeader.getKeyID()).thenReturn(badEncryptedByKid);
        // expected
        when(icValidationFields.encryptedByKid()).thenReturn(expectedEncryptedByKid);

        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        if (badEncryptedByKid != null) {
            assertEquals(String.format(Constants.MSG_ENCRYPTED_BY_KID_MISMATCHED, badEncryptedByKid,
                    expectedEncryptedByKid), exception.getBody().getDetail());
        } else {
            assertEquals(String.format(Constants.MSG_OBJ_NULL, IntegrityChecksKeys.ENCRYPTED_BY_KID), exception.getBody().getDetail());
        }
    }

    public static Stream<Arguments> failedCreatedAtMatching() {
        return Stream.of(
                Arguments.of(UUIDs.timeBased().toString(), CREATED_AT),
                Arguments.of(null, CREATED_AT),
                Arguments.of(123, CREATED_AT),
                Arguments.of(123L, CREATED_AT),
                Arguments.of(123.32, CREATED_AT),
                Arguments.of(Instant.now(), CREATED_AT),
                Arguments.of(UUID.randomUUID(), CREATED_AT)
        );
    }

    @ParameterizedTest
    @MethodSource("failedCreatedAtMatching")
    void validateIfEnabled_WhenCreatedAtMismatched_ShouldThrowException(Object corruptedCreatedAt, UUID expectedCreatedAt)  {
        mockSetup();
        // override
        // actual
        when(jweHeader.getCustomParam(IntegrityChecksKeys.CREATED_AT)).thenReturn(corruptedCreatedAt);
        // expected
        when(icValidationFields.createdAt()).thenReturn(expectedCreatedAt);


        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        if (Objects.isNull(corruptedCreatedAt)) {
            assertEquals(String.format(Constants.MSG_OBJ_NULL, IntegrityChecksKeys.CREATED_AT), exception.getBody().getDetail());
        } else if (corruptedCreatedAt instanceof String) {
            assertEquals(String.format(Constants.MSG_CREATED_AT_MISMATCHED, corruptedCreatedAt,
                    expectedCreatedAt.toString()), exception.getBody().getDetail());
        } else {
            assertEquals(String.format(Constants.MSG_TYPE_MISMATCH, IntegrityChecksKeys.CREATED_AT, corruptedCreatedAt.getClass().getName(), expectedCreatedAt.toString().getClass().getName()), exception.getBody().getDetail());
        }
    }

    public static Stream<Arguments> failedEncryptedAtMatching() {
        return Stream.of(
                Arguments.of(Instant.now().minus(Duration.ofHours(1).plusMinutes(30)).toEpochMilli(), ENCRYPTED_AT),
                Arguments.of(null, ENCRYPTED_AT),
                Arguments.of("", ENCRYPTED_AT),
                Arguments.of("xxx", ENCRYPTED_AT),
                Arguments.of(Instant.now(), ENCRYPTED_AT),
                Arguments.of(UUID.randomUUID(), ENCRYPTED_AT)
        );
    }

    @ParameterizedTest
    @MethodSource("failedEncryptedAtMatching")
    void validateIfEnabled_WhenEncryptedAtMismatched_ShouldThrowException(Object corruptedEncryptedAt, Instant expectedEncryptedAt)  {
        mockSetup();

        // override
        // actual
        when(jweHeader.getCustomParam(IntegrityChecksKeys.ENCRYPTED_AT)).thenReturn(corruptedEncryptedAt);
        // expected
        when(icValidationFields.encryptedAt()).thenReturn(expectedEncryptedAt);

        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        if (Objects.isNull(corruptedEncryptedAt)) {
            assertEquals(String.format(Constants.MSG_OBJ_NULL, IntegrityChecksKeys.ENCRYPTED_AT), exception.getBody().getDetail());
        } else if (corruptedEncryptedAt instanceof Long) {
            assertEquals(String.format(Constants.MSG_ENCRYPTED_AT_MISMATCHED, corruptedEncryptedAt,
                    expectedEncryptedAt.toEpochMilli()), exception.getBody().getDetail());
        } else {
            assertEquals(String.format(Constants.MSG_TYPE_MISMATCH, IntegrityChecksKeys.ENCRYPTED_AT, corruptedEncryptedAt.getClass().getName(), Long.class.getName()), exception.getBody().getDetail());
        }

    }

    @Test
    void validateIfEnabled_WhenVersionKeyNotFound_ShouldSucceed()  {
        when(jweHeader.getCustomParam(IntegrityChecksKeys.VERSION)).thenReturn(null);
        assertDoesNotThrow(() -> integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK, decryptedNEK, icValidationFields));
    }

    @Test
    void validateIfEnabled_WhenFailedToParseKey_ShouldThrowException()  {
        final String errorMsg = "failed to parse nek";
        ParseException returnedException = new ParseException(errorMsg, 0);
        mockedStatic.when(() -> JWEObject.parse(BAD_ENCRYPTED_NEK)).thenThrow(returnedException);

        when(integrityChecksProperties.isValidationEnabled()).thenReturn(true);
        IntegrityChecksValidationException exception =
                assertThrows(IntegrityChecksValidationException.class,
                        () -> integrityChecksComponent.validateIfEnabled(BAD_ENCRYPTED_NEK,
                                decryptedNEK, icValidationFields));

        assertEquals(errorMsg, exception.getBody().getDetail());
    }

    @ParameterizedTest
    @NullSource
    void testPopulateIfEnabled_NullBuilder(JWEHeader.Builder builder) {
        assertThrows(NullPointerException.class, () -> {
            integrityChecksComponent.populateIfEnabled(builder, icPopulationFields);
        });
    }

    @ParameterizedTest
    @NullSource
    void testPopulateIfEnabled_NullIcFields(IntegrityChecksPopulationFields icFields) {
        assertThrows(NullPointerException.class, () -> {
            integrityChecksComponent.populateIfEnabled(builder, icFields);
        });
    }

    @ParameterizedTest
    @NullSource
    void testValidationIfEnabled_NullEncryptedNEK(String nullEncryptedNEK) {
        assertThrows(NullPointerException.class, () -> {
            integrityChecksComponent.validateIfEnabled(nullEncryptedNEK,
                    decryptedNEK, icValidationFields);
        });
    }

    @ParameterizedTest
    @NullSource
    void testValidationIfEnabled_NullDecryptedNEK(OctetSequenceKey nullDecryptedNEK) {
        assertThrows(NullPointerException.class, () -> {
            integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                    nullDecryptedNEK, icValidationFields);
        });
    }

    @ParameterizedTest
    @NullSource
    void testValidationIfEnabled_NullICValidationFields(IntegrityChecksValidationFields nullICValidationFields) {
        assertThrows(NullPointerException.class, () -> {
            integrityChecksComponent.validateIfEnabled(ENCRYPTED_NEK,
                    decryptedNEK, nullICValidationFields);
        });
    }
}
