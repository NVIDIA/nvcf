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

package com.nvidia.boot.exceptions;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;

class BootResponseExceptionTest {

    private static final String MESSAGE = "something went wrong";

    @Test
    void badRequestExceptionSetsStatusDetailAndUrn() {
        var ex = new BadRequestException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        var body = ex.getBody();
        assertThat(body.getDetail()).isEqualTo(MESSAGE);
        assertThat(body.getType()).hasToString("urn:nv-boot:problem-details:bad-request");
        assertThat(body.getTitle()).isEqualTo(HttpStatus.BAD_REQUEST.getReasonPhrase());
    }

    @Test
    void badRequestExceptionWithCausePreservesCause() {
        var cause = new RuntimeException("root");
        var ex = new BadRequestException(MESSAGE, cause);
        assertThat(ex.getCause()).isSameAs(cause);
        assertThat(ex.getBody().getDetail()).isEqualTo(MESSAGE);
    }

    @Test
    void badRequestExceptionNullMessageUsesStatusReasonPhraseAsDetail() {
        var ex = new BadRequestException(null);
        assertThat(ex.getBody().getDetail()).isEqualTo(HttpStatus.BAD_REQUEST.getReasonPhrase());
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:bad-request");
    }

    @Test
    void badRequestExceptionBlankMessageUsesStatusReasonPhraseAsDetail() {
        var ex = new BadRequestException("   ");
        assertThat(ex.getBody().getDetail()).isEqualTo(HttpStatus.BAD_REQUEST.getReasonPhrase());
    }

    @Test
    void notFoundExceptionSetsCorrectUrn() {
        var ex = new NotFoundException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:not-found");
        assertThat(ex.getBody().getDetail()).isEqualTo(MESSAGE);
    }

    @Test
    void unauthorizedExceptionSetsCorrectUrn() {
        var ex = new UnauthorizedException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.UNAUTHORIZED);
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:unauthorized");
    }

    @Test
    void forbiddenExceptionSetsCorrectUrn() {
        var ex = new ForbiddenException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:forbidden");
    }

    @Test
    void conflictExceptionSetsCorrectUrn() {
        var ex = new ConflictException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.CONFLICT);
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:conflict");
    }

    @Test
    void unprocessableEntityExceptionSetsCorrectUrn() {
        var ex = new UnprocessableEntityException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.UNPROCESSABLE_CONTENT);
        assertThat(ex.getBody().getType())
                .hasToString("urn:nv-boot:problem-details:unprocessable-entity");
    }

    @Test
    void tooManyRequestsExceptionSetsCorrectUrn() {
        var ex = new TooManyRequestsException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.TOO_MANY_REQUESTS);
        assertThat(ex.getBody().getType())
                .hasToString("urn:nv-boot:problem-details:too-many-requests");
    }

    @Test
    void upstreamExceptionSetsCorrectUrn() {
        var ex = new UpstreamException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.BAD_GATEWAY);
        assertThat(ex.getBody().getType()).hasToString("urn:nv-boot:problem-details:upstream");
    }

    @Test
    void paymentRequiredExceptionSetsCorrectUrn() {
        var ex = new PaymentRequiredException(MESSAGE);
        assertThat(ex.getStatusCode()).isEqualTo(HttpStatus.PAYMENT_REQUIRED);
        assertThat(ex.getBody().getType())
                .hasToString("urn:nv-boot:problem-details:payment-required");
    }
}
