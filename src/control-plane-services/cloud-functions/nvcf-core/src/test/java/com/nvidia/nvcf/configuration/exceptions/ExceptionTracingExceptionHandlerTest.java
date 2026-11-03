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

package com.nvidia.nvcf.configuration.exceptions;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;

import io.micrometer.tracing.Tracer;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.dao.DataAccessResourceFailureException;
import org.springframework.dao.TransientDataAccessResourceException;
import org.springframework.data.cassandra.CassandraConnectionFailureException;
import org.springframework.data.cassandra.CassandraDriverTimeoutException;
import org.springframework.data.cassandra.CassandraInsufficientReplicasAvailableException;
import org.springframework.data.cassandra.CassandraReadTimeoutException;
import org.springframework.data.cassandra.CassandraWriteTimeoutException;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.test.util.ReflectionTestUtils;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;

class ExceptionTracingExceptionHandlerTest {

    private static final String CQL = "Query; CQL [SELECT * FROM functions_v3]; ";

    private MockMvc mockMvc;

    @BeforeEach
    void setUp() {
        var advice = new ValidationAwareExceptionHandler();
        ReflectionTestUtils.setField(advice, "tracer", mock(Tracer.class));
        mockMvc = MockMvcBuilders.standaloneSetup(new FailingStorageController())
                .setControllerAdvice(advice)
                .build();
    }

    @ParameterizedTest
    @ValueSource(strings = {"read-timeout", "unavailable", "overloaded", "connection-failure"})
    void retriableStorageFailureIsServiceUnavailable(String failure) throws Exception {
        var response = perform(failure);

        assertThat(response.getStatus()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE.value());
        assertThat(response.getHeader(HttpHeaders.RETRY_AFTER)).isEqualTo("10");
        assertThat(response.getContentAsString())
                .contains("Storage is temporarily unavailable")
                .contains("urn:nv-boot:problem-details:service-unavailable")
                .doesNotContain("CQL");
    }

    // A write that timed out may have been applied, and a read or write failure does not clear on
    // retry, so none of these may tell the client to retry.
    @ParameterizedTest
    @ValueSource(strings = {"write-timeout", "driver-timeout", "read-failure", "wrapped"})
    void otherStorageFailureIsInternalErrorWithoutCql(String failure) throws Exception {
        var response = perform(failure);

        assertThat(response.getStatus()).isEqualTo(HttpStatus.INTERNAL_SERVER_ERROR.value());
        assertThat(response.getHeader(HttpHeaders.RETRY_AFTER)).isNull();
        assertThat(response.getContentAsString()).doesNotContain("CQL");
    }

    private MockHttpServletResponse perform(String failure) throws Exception {
        return mockMvc.perform(get("/fail/" + failure)).andReturn().getResponse();
    }

    @RestController
    static class FailingStorageController {

        @GetMapping("/fail/{failure}")
        String fail(@PathVariable String failure) {
            throw switch (failure) {
                case "read-timeout" ->
                        new CassandraReadTimeoutException(true, CQL + "read timeout", null);
                case "unavailable" ->
                        new CassandraInsufficientReplicasAvailableException(2, 1, CQL, null);
                case "overloaded" -> new TransientDataAccessResourceException(CQL + "overloaded");
                case "connection-failure" ->
                        new CassandraConnectionFailureException(Map.of(), CQL + "no node", null);
                case "write-timeout" ->
                        new CassandraWriteTimeoutException("SIMPLE", CQL + "write timeout", null);
                case "driver-timeout" -> new CassandraDriverTimeoutException(CQL + "timed out", null);
                case "read-failure" -> new DataAccessResourceFailureException(CQL + "read failure");
                case "wrapped" -> new IllegalStateException(
                        "Failed to update deployment: " + CQL + "write timeout");
                default -> new IllegalArgumentException(failure);
            };
        }
    }
}
