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

package com.nvidia.nvcf.configuration;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.exceptions.ServiceUnavailableException;
import com.nvidia.nvcf.configuration.exceptions.ExceptionTracingExceptionHandler;
import io.grpc.Status;
import org.junit.jupiter.api.Test;
import org.springframework.dao.DataAccessException;
import org.springframework.data.cassandra.CassandraReadTimeoutException;
import org.springframework.grpc.server.advice.GrpcExceptionHandler;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.context.request.WebRequest;

class GrpcConfigurationTest {

    private static final String CQL = "Query; CQL [SELECT * FROM functions_v3]; read timeout";

    private final GrpcConfiguration grpcConfiguration = new GrpcConfiguration();

    @Test
    void retriableStorageFailureIsUnavailable() {
        var status = grpcConfiguration.handleStorageUnavailable(
                new CassandraReadTimeoutException(false, CQL, null));

        assertThat(status.getCode()).isEqualTo(Status.Code.UNAVAILABLE);
        assertThat(status.getDescription()).isEqualTo("Storage is temporarily unavailable");
    }

    @Test
    void serviceUnavailableIsUnavailable() {
        var status = grpcConfiguration.handleErrorResponseException(
                new ServiceUnavailableException("Storage is temporarily unavailable"));

        assertThat(status.getCode()).isEqualTo(Status.Code.UNAVAILABLE);
        assertThat(status.getDescription()).isEqualTo("Storage is temporarily unavailable");
    }

    @Test
    void grpcAndMvcTreatTheSameStorageFailuresAsRetriable() throws Exception {
        var grpcTypes = GrpcConfiguration.class
                .getMethod("handleStorageUnavailable", DataAccessException.class)
                .getAnnotation(GrpcExceptionHandler.class).value();
        var mvcTypes = ExceptionTracingExceptionHandler.class
                .getDeclaredMethod("handleStorageUnavailable", DataAccessException.class,
                                   WebRequest.class)
                .getAnnotation(ExceptionHandler.class).value();

        assertThat(grpcTypes).isNotEmpty().containsExactlyInAnyOrder(mvcTypes);
    }
}
