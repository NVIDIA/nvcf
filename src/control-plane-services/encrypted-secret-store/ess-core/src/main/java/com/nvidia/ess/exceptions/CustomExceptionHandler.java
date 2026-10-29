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
package com.nvidia.ess.exceptions;

import static com.nvidia.ess.constants.Constants.SERVER_EXCHANGE_REJECTED;
import static com.nvidia.ess.constants.Constants.UNKNOWN_NAMESPACE;
import static com.nvidia.ess.constants.Constants.X_ESS_AGENT_ID_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_REQUEST_ID_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.EXHAUSTED_RETRIES_KEY;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.handlers.BootReactiveExceptionHandler;
import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.config.properties.LoggingProperties.ErrorLogging;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.LoggingUtils;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import jakarta.validation.ConstraintViolation;
import jakarta.validation.ConstraintViolationException;
import java.util.Optional;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.collections4.SetUtils;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.security.web.server.firewall.ServerExchangeRejectedException;
import org.springframework.util.ObjectUtils;
import org.springframework.validation.FieldError;
import org.springframework.validation.ObjectError;
import org.springframework.web.ErrorResponseException;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.support.WebExchangeBindException;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@Configuration
@ControllerAdvice
@Slf4j
public class CustomExceptionHandler extends BootReactiveExceptionHandler {

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    private LoggingProperties loggingProperties;

    // add exception to Trace
    @Nonnull
    @Override
    protected Mono<ResponseEntity<Object>> handleExceptionInternal(
            @Nonnull Exception ex, @Nullable Object body, @Nullable HttpHeaders headers,
            @Nonnull HttpStatusCode statusCode, @Nonnull ServerWebExchange exchange) {

        String namespace;
        try {
            namespace = StringUtils.defaultIfBlank(
                    exchange.getRequest().getHeaders().getFirst(X_ESS_NAMESPACE_HEADER),
                    UNKNOWN_NAMESPACE);
        } catch (ServerExchangeRejectedException e) {
            // Bad value of `X-ESS-Namespace` header (which might be the error being handled in this call to
            // the exception-handler in the first place).
            //
            // Catch the exception here in order to avoid a recursive error-handling scenario, log an error
            // to the console and default the namespace to "UNKNOWN".
            //
            log.error("Illegal value of `X-ESS-Namespace:` header found while handling exception.", ex);
            namespace = UNKNOWN_NAMESPACE;
        }

        // https://opentelemetry.io/docs/specs/semconv/http/http-spans/#status
        if (statusCode.is4xxClientError()) {
            telemetryComponents.recordExceptionWithoutErrorStatus(exchange, ex);
        } else {
            telemetryComponents.recordException(exchange, ex);
        }

        logError(statusCode, ex, exchange);
        if (ex instanceof RetriesExhaustedException exRetriesExhausted) {
            telemetryComponents.setSpanAttribute(exchange, EXHAUSTED_RETRIES_KEY, true);
            customMetricsRegistry.recordExhaustedRetryableError(namespace, exRetriesExhausted.getSummary().getProblemBrief());
        }

        // add cause message to the body
        if (ex instanceof ErrorResponseException errorResponseException) {
            var cause = errorResponseException.getCause();
            if (cause != null && cause.getMessage() != null) {
                errorResponseException.getBody().setProperty("cause", cause.getMessage());
            }
        }

        return super.handleExceptionInternal(ex, body, headers, statusCode, exchange);
    }


    @Nonnull
    @Override
    protected Mono<ResponseEntity<Object>> createResponseEntity(
            @Nullable Object body, @Nullable HttpHeaders headers,
            @Nonnull HttpStatusCode statusCode,
            @Nonnull ServerWebExchange exchange) {
        if (body instanceof ProblemDetail pb) {
            pb.setDetail(addTracingInformation(pb.getDetail(), exchange));
        }

        return super.createResponseEntity(body, headers, statusCode, exchange);
    }

    private static String addTracingInformation(String errorMessage, ServerWebExchange exchange) {
        String requestId = exchange.getResponse().getHeaders().toSingleValueMap().getOrDefault(X_ESS_REQUEST_ID_HEADER, "[not set]");
        StringBuilder newErrorMessage = new StringBuilder(StringUtils.defaultString(errorMessage));
        if (Strings.CS.endsWith(errorMessage, ".")) {
            newErrorMessage.append(" Request Id: ").append(requestId);
        } else {
            newErrorMessage.append(". Request Id: ").append(requestId);
        }

        String agentId = exchange.getResponse().getHeaders().getFirst(X_ESS_AGENT_ID_HEADER);
        if (agentId != null) {
            if (Strings.CS.endsWith(errorMessage, ".")) {
                newErrorMessage.append(" Agent Id: ").append(agentId);
            } else {
                newErrorMessage.append(". Agent Id: ").append(agentId);
            }
        }
        return newErrorMessage.toString();
    }

    public static String toCleanMessage(WebExchangeBindException ex) {
        StringBuilder sb = new StringBuilder("Validation failed with ")
                .append(ex.getErrorCount()).append(" error(s): ");
        for (ObjectError error : ex.getAllErrors()) {
            sb.append('[');
            if (error instanceof FieldError fieldError) {
                sb.append("Field error in object '")
                        .append(fieldError.getObjectName())
                        .append("' on field '")
                        .append(fieldError.getField())
                        .append("': rejected value [")
                        .append(ObjectUtils.nullSafeConciseToString(
                                fieldError.getRejectedValue()))
                        .append("]; ")
                        .append(fieldError.getDefaultMessage());
            } else {
                sb.append(error);
            }
            sb.append("] ");
        }
        sb.deleteCharAt(sb.length() - 1);
        return sb.toString();
    }


    public static String toCleanMessage(ConstraintViolationException ex) {
        var constraintViolations = SetUtils.emptyIfNull(ex.getConstraintViolations());
        StringBuilder sb = new StringBuilder("Validation failed with ")
                .append(constraintViolations.size()).append(" error(s): ");
        for (ConstraintViolation<?> violation : constraintViolations) {
            sb.append('[');
            sb.append("Constraint violation error in '")
                    .append(violation.getPropertyPath())
                    .append("': rejected value [")
                    .append(ObjectUtils.nullSafeConciseToString(
                            violation.getInvalidValue()))
                    .append("]; ")
                    .append(violation.getMessage());
            sb.append("] ");
        }
        sb.deleteCharAt(sb.length() - 1);
        return sb.toString();
    }


    @Override
    protected Mono<ResponseEntity<Object>> handleWebExchangeBindException(
            WebExchangeBindException ex, HttpHeaders headers,
            HttpStatusCode status,
            ServerWebExchange exchange) {
        var problemDetail = ProblemDetail.forStatusAndDetail(status, toCleanMessage(ex));
        return handleExceptionInternal(ex, problemDetail, headers, status, exchange);
    }

    @ExceptionHandler({ServerExchangeRejectedException.class})
    public Mono<ResponseEntity<Object>> handleServerExchangeRejectedException(
            ServerExchangeRejectedException ex, ServerWebExchange exchange) {
        return super.handleException(new BadRequestException(SERVER_EXCHANGE_REJECTED, ex), exchange);
    }

    @ExceptionHandler({ConstraintViolationException.class})
    protected Mono<ResponseEntity<Object>> handleExceptionCatchAll(ConstraintViolationException ex,
            ServerWebExchange exchange) {
        return handleExceptionInternal(ex,
                ProblemDetail.forStatusAndDetail(HttpStatus.BAD_REQUEST, toCleanMessage(ex)),
                exchange.getResponse().getHeaders(), HttpStatus.BAD_REQUEST, exchange);
    }

    private void logError(HttpStatusCode code, Exception ex, ServerWebExchange exchange) {
        Optional<ErrorLogging> optErrorLogging = loggingProperties.getErrors()
                .stream()
                .filter(errorLogging -> code.value() == errorLogging.getHttpStatusCode())
                .findFirst();
        String errorMessage = addTracingInformation(ex.getMessage(), exchange);
        optErrorLogging.ifPresentOrElse(
                errorLogging -> LoggingUtils.customErrorLog(log, errorLogging.getLogLevel(), errorMessage, ex),
                () -> log.error(errorMessage, ex));
    }
}
