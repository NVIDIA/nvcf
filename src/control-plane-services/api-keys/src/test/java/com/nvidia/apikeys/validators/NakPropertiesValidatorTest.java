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

package com.nvidia.apikeys.validators;

import static com.nvidia.apikeys.TestData.DATA_DOMAIN_KEY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.jwt.configuration.EncryptedModelConverterProperties;
import com.nvidia.apikeys.config.JwksProperties;
import com.nvidia.apikeys.config.NakProperties;
import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import java.time.Duration;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EmptySource;
import org.junit.jupiter.params.provider.NullSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class NakPropertiesValidatorTest {

    private static final String VALID_NCA_ID = "test-nca-id";
    private static final String VALID_REGISTRATIONS = "[]";
    private static final Map<String, String> VALID_SERVICE_ID_MAP = Map.of("nvcf", "service-1");

    @InjectMocks
    private NakPropertiesValidator validator;

    private static JwksProperties validJwks() {
        return JwksProperties.builder()
                .privateJwks("private-jwks-test-value")
                .jweKeyMapping(Map.of("payload_jwe_kid", "alias-1"))
                .build();
    }

    private static NakProperties.NakPropertiesBuilder validBuilder() {
        return NakProperties.builder()
                .keyPrefix("nvcfapi-")
                .dataDomainKey(DATA_DOMAIN_KEY)
                .keepAfterExpiredDuration(Duration.ofDays(2))
                .ncaId(VALID_NCA_ID)
                .registrations(VALID_REGISTRATIONS)
                .serviceIdMap(VALID_SERVICE_ID_MAP)
                .jwks(validJwks())
                .encryptedModelConverter(new EncryptedModelConverterProperties());
    }

    @ValueSource(strings = {"nvcfapi-a", "nvcfapi-="})
    @ParameterizedTest
    void validate_shouldThrowIfSuffixInvalid(String prefix) {
        NakProperties config = NakProperties.builder()
                .keyPrefix(prefix)
                .dataDomainKey(DATA_DOMAIN_KEY)
                .build();

        assertThat(assertThrows(IllegalStateException.class, () -> validator.validate(config)))
                .hasMessage("api-key-prefix must end with a dash");
    }

    @ValueSource(strings = {"nvcfapi-", "nvcfapi-stage-", "nvcfapi-test-"})
    @ParameterizedTest
    void validate_shouldAcceptValidPrefix(String prefix) {
        NakProperties config = validBuilder().keyPrefix(prefix).build();

        assertDoesNotThrow(() -> validator.validate(config));
    }

    @EmptySource
    @ValueSource(strings = {
            // key that is too long
            "KVyrsdeNCW5IXxFgysCLN35sir4Uqh4ZuWUZmv9pBHRdIcUOTZ79JfMLDZlKEvPhKrVHZX-ZP1jpGMrxsKjfEXIiY_APV0dn-fp0mQBvC-GwbAot_w7ztxxXGrYX2vVcC5eGqpTR1x3up_OZHkMy6bfF731Qn_kZzOWOMNBWfBaU4_l2wbkolgk",
            // key that is too short
            "KVyrsdeNCW5IXxFgysCLN35sir4Uqh4ZuWUZmv9pBHRdIcUOTZ79JfMLDZlKEvPhKrVHZX-ZP1jpGMrxsKjfEXIiY_APV0dn-fp0mQBvC-GwbAot_w7ztxxXGrYX2vVcC5eGqpTR1x3up_OZHkMy6bfF731Qn_kZzOWOMNBWfBaU4_l2wbk"})
    @ParameterizedTest
    void validate_shouldThrowIfDataDomainKeyLengthInvalid(String dataDomainKey) {
        NakProperties config = NakProperties.builder()
                .keyPrefix("nvcfapi-")
                .dataDomainKey(dataDomainKey)
                .build();

        assertThat(assertThrows(IllegalStateException.class, () -> validator.validate(config)))
                .hasMessage("Data domain key length must be 136 bytes");
    }

    @Test
    void validate_shouldThrowIfDataDomainKeyCantBeDecoded() {
        NakProperties config = NakProperties.builder()
                .keyPrefix("nvcfapi-")
                .dataDomainKey("KVyrsdeNCW5IXxFgysCLN35sir4Uqh4ZuWUZmv9pBHRdIcUOTZ79JfMLDZlKEvPhKrVHZX-ZP1jpGMrxsKjfEXIiY_APV0dn-fp0mQBvC-GwbAot_w7ztxxXGrYX2vVcC5eGqpTR1x3up_OZHkMy6bfF731Qn_kZzOWOMNBWfBaU4_l2wbkol")
                .build();

        assertThat(assertThrows(IllegalStateException.class, () -> validator.validate(config)))
                .hasMessage("Failed to decode data domain key");
    }

    @Test
    void validate_shouldReturnValidConfig() {
        NakProperties config = validBuilder().build();

        NakProperties result = validator.validate(config);
        assertThat(result).isEqualTo(config);
    }

    @Test
    void validate_shouldThrowIfKeepAfterExpiredDurationIsNegative() {
        NakProperties config = NakProperties.builder()
                .keyPrefix("nvcfapi-")
                .dataDomainKey(DATA_DOMAIN_KEY)
                .keepAfterExpiredDuration(Duration.ofDays(-1))
                .build();

        assertThat(assertThrows(IllegalStateException.class, () -> validator.validate(config)))
                .hasMessage("keep-after-expired-duration must be set and not exceed PT720H");
    }

    @Test
    void validate_shouldThrowIfKeepAfterExpiredDurationExceedsCap() {
        NakProperties config = NakProperties.builder()
                .keyPrefix("nvcfapi-")
                .dataDomainKey(DATA_DOMAIN_KEY)
                .keepAfterExpiredDuration(Duration.ofDays(31))
                .build();

        assertThat(assertThrows(IllegalStateException.class, () -> validator.validate(config)))
                .hasMessage("keep-after-expired-duration must be set and not exceed PT720H");
    }

    @Test
    void validate_shouldAcceptKeepAfterExpiredDurationAtCap() {
        NakProperties config = validBuilder().keepAfterExpiredDuration(Duration.ofDays(30)).build();

        assertDoesNotThrow(() -> validator.validate(config));
    }

    // Bean Validation (Jakarta) tests for constraint annotations on NakProperties.
    // Spring fires these at @ConfigurationProperties binding time; the tests below
    // exercise the same validator directly so a removed/broken annotation fails fast.
    private static final Validator beanValidator =
            Validation.buildDefaultValidatorFactory().getValidator();

    private static String firstViolatedProperty(Set<ConstraintViolation<NakProperties>> violations) {
        return violations.iterator().next().getPropertyPath().toString();
    }

    @Test
    void beanValidation_shouldAcceptValidConfig() {
        Set<ConstraintViolation<NakProperties>> violations = beanValidator.validate(validBuilder().build());

        assertThat(violations).isEmpty();
    }

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {"", "   "})
    void beanValidation_shouldRejectBlankNcaId(String ncaId) {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().ncaId(ncaId).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("ncaId");
    }

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {"", "   "})
    void beanValidation_shouldRejectBlankRegistrations(String registrations) {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().registrations(registrations).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("registrations");
    }

    @Test
    void beanValidation_shouldRejectNullServiceIdMap() {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().serviceIdMap(null).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("serviceIdMap");
    }

    @Test
    void beanValidation_shouldRejectEmptyServiceIdMap() {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().serviceIdMap(Map.of()).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("serviceIdMap");
    }

    @Test
    void beanValidation_shouldRejectNullJwks() {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().jwks(null).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("jwks");
    }

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {"", "   "})
    void beanValidation_shouldRejectBlankPrivateJwks(String privateJwks) {
        JwksProperties jwks = JwksProperties.builder()
                .privateJwks(privateJwks)
                .jweKeyMapping(Map.of("payload_jwe_kid", "alias-1"))
                .build();
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().jwks(jwks).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("jwks.privateJwks");
    }

    @Test
    void beanValidation_shouldRejectNullJweKeyMapping() {
        JwksProperties jwks = JwksProperties.builder()
                .privateJwks("private-jwks-test-value")
                .jweKeyMapping(null)
                .build();
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().jwks(jwks).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("jwks.jweKeyMapping");
    }

    @Test
    void beanValidation_shouldRejectEmptyJweKeyMapping() {
        JwksProperties jwks = JwksProperties.builder()
                .privateJwks("private-jwks-test-value")
                .jweKeyMapping(Map.of())
                .build();
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().jwks(jwks).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("jwks.jweKeyMapping");
    }

    @Test
    void beanValidation_shouldRejectNullEncryptedModelConverter() {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().encryptedModelConverter(null).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("encryptedModelConverter");
    }

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {"", "   "})
    void beanValidation_shouldRejectBlankKeyPrefix(String keyPrefix) {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().keyPrefix(keyPrefix).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("keyPrefix");
    }

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {"", "   "})
    void beanValidation_shouldRejectBlankDataDomainKey(String dataDomainKey) {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().dataDomainKey(dataDomainKey).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("dataDomainKey");
    }

    @Test
    void beanValidation_shouldRejectNullKeepAfterExpiredDuration() {
        Set<ConstraintViolation<NakProperties>> violations =
                beanValidator.validate(validBuilder().keepAfterExpiredDuration(null).build());

        assertThat(violations).hasSize(1);
        assertThat(firstViolatedProperty(violations)).isEqualTo("keepAfterExpiredDuration");
    }
}
