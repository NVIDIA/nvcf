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

package com.nvidia.apikeys.config.exceptions;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;

import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.dao.DataAccessException;
import org.springframework.data.cassandra.CassandraConnectionFailureException;
import org.springframework.data.cassandra.CassandraReadTimeoutException;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.web.context.request.WebRequest;

class MvcExceptionHandlerTest {

    private final MvcExceptionHandler handler = new MvcExceptionHandler();

    @Test
    void handleCassandraUnavailable_readTimeout() throws Exception {
        assertServiceUnavailable(new CassandraReadTimeoutException(
                false, "Cassandra timeout during read query at consistency LOCAL_QUORUM", null));
    }

    @Test
    void handleCassandraUnavailable_connectionFailure() throws Exception {
        assertServiceUnavailable(new CassandraConnectionFailureException(
                Map.of(), "All host(s) tried for query failed", null));
    }

    private void assertServiceUnavailable(DataAccessException ex) throws Exception {
        ResponseEntity<Object> response = handler.handleCassandraUnavailable(
                ex, mock(WebRequest.class));

        assertThat(response).isNotNull();
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
        assertThat(response.getHeaders().getFirst(HttpHeaders.RETRY_AFTER)).isEqualTo("10");
        assertThat(response.getBody()).isInstanceOfSatisfying(ProblemDetail.class, body -> {
            assertThat(body.getDetail()).isEqualTo("Key storage is temporarily unavailable");
            assertThat(body.getType())
                    .hasToString("urn:nv-boot:problem-details:service-unavailable");
        });
    }
}
