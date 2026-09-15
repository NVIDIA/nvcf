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
package com.nvidia.notary.config;

import static com.nvidia.notary.utils.TestData.SIGNING_KEY_ID_1;
import static com.nvidia.notary.utils.TestUtils.assertThrowsExceptionWithMessage;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

/**
 * Covers the runtime checks {@link ConfigurationValidator} still performs after the field-level
 * constraints moved to Jakarta Bean Validation on {@link NotaryProperties}. Specifically, the
 * {@code signingKid} → {@link JWKSet} lookup contract: kid must resolve to a present, private key.
 * Constraint-level checks (blank/null/positive/URL/audience shape/cross-field) are exercised by
 * {@link NotaryPropertiesValidationTest}.
 */
@ExtendWith(MockitoExtension.class)
class ConfigurationValidatorTest {

    @Mock
    private NotaryProperties notaryPropertiesMock;

    @Mock
    private JWKSet jwkSetMock;

    @Mock
    private JWK keyMock;

    @InjectMocks
    private ConfigurationValidator validator;

    @Test
    void validate_throwsIfSigningKidNotSet() {
        when(notaryPropertiesMock.getSigningKid()).thenReturn(null);

        assertThrowsExceptionWithMessage(
                IllegalStateException.class, () -> validator.validate(),
                "Signing key id is not set.");
    }

    @Test
    void validate_throwsIfKeySetIsEmpty() {
        when(notaryPropertiesMock.getSigningKid()).thenReturn(SIGNING_KEY_ID_1);
        when(jwkSetMock.isEmpty()).thenReturn(true);

        assertThrowsExceptionWithMessage(
                IllegalStateException.class, () -> validator.validate(),
                "Signing key set is empty.");
    }

    @Test
    void validate_throwsIfKeyNotFoundInTheSet() {
        when(notaryPropertiesMock.getSigningKid()).thenReturn(SIGNING_KEY_ID_1);
        when(jwkSetMock.isEmpty()).thenReturn(false);
        when(jwkSetMock.getKeyByKeyId(SIGNING_KEY_ID_1)).thenReturn(null);

        assertThrowsExceptionWithMessage(
                IllegalStateException.class, () -> validator.validate(),
                "Signing key 'signing-key-kid-1' not found in key set.");
    }

    @Test
    void validate_throwsIfKeyNotPrivate() {
        when(notaryPropertiesMock.getSigningKid()).thenReturn(SIGNING_KEY_ID_1);
        when(jwkSetMock.isEmpty()).thenReturn(false);
        when(jwkSetMock.getKeyByKeyId(SIGNING_KEY_ID_1)).thenReturn(keyMock);
        when(keyMock.isPrivate()).thenReturn(false);

        assertThrowsExceptionWithMessage(
                IllegalStateException.class, () -> validator.validate(),
                "Signing key 'signing-key-kid-1' is not private.");
    }

    @Test
    void validate_pass() {
        when(notaryPropertiesMock.getSigningKid()).thenReturn(SIGNING_KEY_ID_1);
        when(jwkSetMock.isEmpty()).thenReturn(false);
        when(jwkSetMock.getKeyByKeyId(SIGNING_KEY_ID_1)).thenReturn(keyMock);
        when(keyMock.isPrivate()).thenReturn(true);

        assertDoesNotThrow(() -> validator.validate());
    }
}
