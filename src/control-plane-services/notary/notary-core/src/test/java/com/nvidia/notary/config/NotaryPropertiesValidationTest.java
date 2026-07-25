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

import static org.assertj.core.api.Assertions.assertThat;

import com.nimbusds.jose.JWSAlgorithm;
import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import jakarta.validation.ValidatorFactory;
import java.util.List;
import java.util.Set;
import java.util.function.Consumer;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

/**
 * Exercises the Jakarta Bean Validation constraints declared on {@link NotaryProperties}. Spring
 * runs these during {@code @ConfigurationProperties} binding; here we run them directly against a
 * standalone {@link Validator} so failures are localized to the constraint that fired.
 */
class NotaryPropertiesValidationTest {

    private static ValidatorFactory factory;
    private static Validator validator;

    @BeforeAll
    static void setUpValidator() {
        factory = Validation.buildDefaultValidatorFactory();
        validator = factory.getValidator();
    }

    @AfterAll
    static void tearDownValidator() {
        factory.close();
    }

    /** Build a valid baseline so each test can mutate exactly one field. */
    private static NotaryProperties.NotaryPropertiesBuilder validBuilder() {
        return NotaryProperties.builder()
                .privateJwks("{\"keys\":[]}")
                .signingKid("kid-1")
                .signingAlgorithm(JWSAlgorithm.ES256)
                .issuerUrl("https://issuer.example")
                .signingScope("notary-sign")
                .maxAssertionsRequestSize(4096L)
                .requireAudience(true)
                .requiredAudiences(List.of("s:service-id"));
    }

    @Test
    void valid_passes() {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(validBuilder().build());
        assertThat(violations).isEmpty();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" ", "\t"})
    void privateJwks_blankFails(String value) {
        assertProperty("privateJwks", b -> b.privateJwks(value));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void signingKid_blankFails(String value) {
        assertProperty("signingKid", b -> b.signingKid(value));
    }

    @Test
    void signingAlgorithm_nullFails() {
        assertProperty("signingAlgorithm", b -> b.signingAlgorithm(null));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" ", "not a url"})
    void issuerUrl_blankOrInvalidFails(String value) {
        assertProperty("issuerUrl", b -> b.issuerUrl(value));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" "})
    void signingScope_blankFails(String value) {
        assertProperty("signingScope", b -> b.signingScope(value));
    }

    @ParameterizedTest
    @ValueSource(longs = {-1L, 0L})
    void maxAssertionsRequestSize_nonPositiveFails(long value) {
        assertProperty("maxAssertionsRequestSize", b -> b.maxAssertionsRequestSize(value));
    }

    @ParameterizedTest
    @MethodSource("invalidAudienceEntries")
    void requiredAudiences_invalidElementFails(String badEntry) {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(
                validBuilder().requiredAudiences(List.of(badEntry)).build());
        assertThat(violations)
                .as("violations for entry '%s'", badEntry)
                .isNotEmpty();
        assertThat(violations)
                .extracting(v -> v.getPropertyPath().toString())
                .anyMatch(p -> p.startsWith("requiredAudiences"));
    }

    private static Stream<String> invalidAudienceEntries() {
        return Stream.of(
                "",          // empty
                " ",         // blank
                "s:",        // trailing colon
                "${UNRESOLVED}",  // placeholder leaked through
                "s:foo${BAR}"     // placeholder embedded
        );
    }

    @Test
    void requiredAudiences_emptyFailsWhenRequireAudienceTrue() {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(
                validBuilder().requireAudience(true).requiredAudiences(List.of()).build());
        // The method-level @AssertTrue on isRequiredAudiencesValid fires.
        assertThat(violations)
                .extracting(v -> v.getPropertyPath().toString())
                .contains("requiredAudiencesValid");
    }

    @Test
    void requiredAudiences_emptyPassesWhenRequireAudienceFalse() {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(
                validBuilder().requireAudience(false).requiredAudiences(List.of()).build());
        assertThat(violations).isEmpty();
    }

    /**
     * @NotNull on the field rejects an explicit empty YAML/env binding that Spring's relaxed
     * binder would otherwise coerce to {@code null}, leaving downstream consumers exposed to NPEs.
     */
    @Test
    void requiredAudiences_nullFailsAlways() {
        Set<ConstraintViolation<NotaryProperties>> violationsRequiredTrue = validator.validate(
                validBuilder().requireAudience(true).requiredAudiences(null).build());
        assertThat(violationsRequiredTrue)
                .extracting(v -> v.getPropertyPath().toString())
                .contains("requiredAudiences");

        Set<ConstraintViolation<NotaryProperties>> violationsRequiredFalse = validator.validate(
                validBuilder().requireAudience(false).requiredAudiences(null).build());
        assertThat(violationsRequiredFalse)
                .extracting(v -> v.getPropertyPath().toString())
                .contains("requiredAudiences");
    }

    /**
     * Audience entries can technically contain a newline (the JWT {@code aud} claim is just a
     * string). Without the DOTALL flag, {@code .} doesn't cross newlines and the negative
     * lookahead {@code (?!.*\$\{)} would only scan the first line, letting {@code ${...}} on a
     * second line slip past. {@code (?s)} pins the regex to match across the whole string.
     */
    @Test
    void requiredAudiences_multilinePlaceholderIsRejected() {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(
                validBuilder().requiredAudiences(List.of("s:foo\n${BAR}")).build());
        assertThat(violations)
                .as("multi-line ${BAR} should be rejected by the DOTALL-anchored regex")
                .isNotEmpty();
        assertThat(violations)
                .extracting(v -> v.getPropertyPath().toString())
                .anyMatch(p -> p.startsWith("requiredAudiences"));
    }

    /**
     * Element-level {@code @Pattern} and the class-level {@code @AssertTrue} are independent —
     * an invalid element should still surface even when the cross-field rule is satisfied
     * (non-empty list, requireAudience=true). Confirms both layers fire together.
     */
    @Test
    void requiredAudiences_invalidElementFailsAlongsideCrossFieldRule() {
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(
                validBuilder().requireAudience(true).requiredAudiences(List.of("s:")).build());
        // Element-level @Pattern fires; @AssertTrue passes because the list is non-empty.
        assertThat(violations)
                .extracting(v -> v.getPropertyPath().toString())
                .anyMatch(p -> p.startsWith("requiredAudiences"))
                .noneMatch(p -> p.equals("requiredAudiencesValid"));
    }

    private void assertProperty(String expectedProperty,
                                 Consumer<NotaryProperties.NotaryPropertiesBuilder> mutate) {
        NotaryProperties.NotaryPropertiesBuilder b = validBuilder();
        mutate.accept(b);
        Set<ConstraintViolation<NotaryProperties>> violations = validator.validate(b.build());
        assertThat(violations)
                .as("violations for property %s", expectedProperty)
                .isNotEmpty();
        assertThat(violations)
                .extracting(v -> v.getPropertyPath().toString())
                .anyMatch(p -> p.equals(expectedProperty) || p.startsWith(expectedProperty));
    }
}
