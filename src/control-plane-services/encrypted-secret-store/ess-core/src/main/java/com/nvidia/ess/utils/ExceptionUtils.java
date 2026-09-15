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
package com.nvidia.ess.utils;

import static com.nvidia.ess.constants.Constants.MSG_FAILED_EXCEPTION_CREATION;
import static org.springframework.http.MediaType.APPLICATION_PROBLEM_JSON;

import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.exceptions.InternalErrorException;
import java.util.List;
import java.util.Objects;
import lombok.NonNull;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.web.ErrorResponseException;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;

@UtilityClass
@Slf4j
public class ExceptionUtils {

    // make public if ever needed
    private Throwable constructThrowable(Class<? extends Throwable> eClass, List<Class<?>> paramTypes, List<Object> args) {
        var paramTypesArray = paramTypes.toArray(new Class<?>[0]);
        var argsArray = args.toArray();
        try {
            return eClass.getDeclaredConstructor(paramTypesArray)
                    .newInstance(argsArray);
        } catch (ReflectiveOperationException ex) {
            log.error(MSG_FAILED_EXCEPTION_CREATION, ex.getClass().getName(), ex);
            return new InternalErrorException();
        }
    }

    public ErrorResponseException constructErrorResponseException(
            Class<? extends ErrorResponseException> eClass, String message,
            ErrorSubType errorSubType) {

        ErrorResponseException exception =
                (ErrorResponseException) constructThrowable(eClass, List.of(String.class),
                        List.of(message));
        exception.getBody().setProperty(ErrorSubType.PROBLEM_DETAILS_PROPERTY, errorSubType);

        return exception;
    }

    public Mono<Void> writeErrorResponse(@NonNull ServerWebExchange exchange,
            @NonNull ResponseEntity<Object> errorResponseEntity, @NonNull ObjectMapper objectMapper) {
        var bodyObject = errorResponseEntity.getBody();
        if (!Objects.isNull(bodyObject) && bodyObject instanceof ProblemDetail pd) {
            // `ErrorResponseException` should produce a non-null body of type `ProblemDetail`.
            try {
                // Form the response-body.
                byte[] responseBodyBytes;
                responseBodyBytes = objectMapper.writeValueAsString(pd).getBytes();
                // Set headers from the ResponseEntity into the error-response.
                var headers = errorResponseEntity.getHeaders();
                if (!Objects.isNull(headers)) {
                    headers.forEach((header, values) ->
                        exchange.getResponse().getHeaders().addAll(header, values)
                    );
                }
                // Set the status code.
                exchange.getResponse().setStatusCode(errorResponseEntity.getStatusCode());
                // Explicitly set the content-type header to application/problem+json.
                exchange.getResponse().getHeaders().setContentType(APPLICATION_PROBLEM_JSON);
                // Write the response and conclude request-processing.
                return exchange.getResponse().writeWith(
                    Mono.just(exchange.getResponse().bufferFactory().wrap(responseBodyBytes))
                );
            } catch (JacksonException ex) {
                // Report unexpected event.
                log.error("Problem serializing ProblemDetail to JSON", ex);
            }
        } else {
            // Report unexpected event.
            log.error("Null-valued or non-ProblemDetail response-body in ResponseEntity.");
        }

        // Null response body or JSON serialization error. This shouldn't happen.
        return Mono.<Void>error(new InternalErrorException());
    }
}
