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
package com.nvidia.icms.inbound.rest.model.nvca;

import static org.junit.jupiter.api.Assertions.*;

import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import jakarta.validation.ValidatorFactory;
import java.util.Set;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

class UpdateJwksRequestTest {

    private static Validator validator;

    @BeforeAll
    static void setUpValidator() {
        ValidatorFactory factory = Validation.buildDefaultValidatorFactory();
        validator = factory.getValidator();
    }

    @Test
    void validRequest_noViolations() {
        UpdateJwksRequest request = new UpdateJwksRequest("{\"keys\":[]}", null);
        Set<ConstraintViolation<UpdateJwksRequest>> violations = validator.validate(request);
        assertTrue(violations.isEmpty());
    }

    @Test
    void nullJwks_hasViolation() {
        UpdateJwksRequest request = new UpdateJwksRequest(null, null);
        Set<ConstraintViolation<UpdateJwksRequest>> violations = validator.validate(request);
        assertFalse(violations.isEmpty());
        assertEquals("jwks is required", violations.iterator().next().getMessage());
    }

    @Test
    void blankJwks_hasViolation() {
        UpdateJwksRequest request = new UpdateJwksRequest("   ", null);
        Set<ConstraintViolation<UpdateJwksRequest>> violations = validator.validate(request);
        assertFalse(violations.isEmpty());
    }

    @Test
    void getterSetter_works() {
        UpdateJwksRequest request = new UpdateJwksRequest();
        request.setJwks("{\"keys\":[{\"kty\":\"RSA\"}]}");
        request.setOidcIssuer("https://kubernetes.default.svc.cluster.local");
        assertEquals("{\"keys\":[{\"kty\":\"RSA\"}]}", request.getJwks());
        assertEquals("https://kubernetes.default.svc.cluster.local", request.getOidcIssuer());
    }

    @Test
    void oidcIssuerOptional_noViolationsWhenMissing() {
        // oidcIssuer is intentionally unannotated: the rotate endpoint treats null/blank
        // as "keep the existing persisted value". A required-style validation here would
        // break that backward-compat path.
        UpdateJwksRequest request = new UpdateJwksRequest("{\"keys\":[]}", null);
        Set<ConstraintViolation<UpdateJwksRequest>> violations = validator.validate(request);
        assertTrue(violations.isEmpty());
    }
}
