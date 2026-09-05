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
package com.nvidia.icms.errors;

import com.amazonaws.services.sns.model.AmazonSNSException;
import com.amazonaws.services.sqs.model.AmazonSQSException;
import com.amazonaws.services.sqs.model.QueueDeletedRecentlyException;
import com.datastax.oss.driver.api.core.DriverException;
import com.datastax.oss.driver.api.core.DriverTimeoutException;
import com.nvidia.icms.inbound.rest.exception.OptimisticLockException;
import com.nvidia.icms.outbound.exception.SnsPublishException;
import com.nvidia.icms.outbound.exception.SqsMessageSenderClientException;
import com.nvidia.icms.outbound.exception.ApiKeysException;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.util.GsonCompatMapper;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.Order;
import org.springframework.dao.DataAccessException;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.MissingServletRequestParameterException;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.method.annotation.MethodArgumentTypeMismatchException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.context.annotation.RequestScope;

import static com.nvidia.icms.service.telemetry.model.Events.INTERNAL_SERVER_ERROR_EVENT;


@Slf4j
@ControllerAdvice
@RequestScope
@Order(Ordered.HIGHEST_PRECEDENCE)
public class GlobalErrorHandler {

    private static final String INTERNAL_SERVER_ERROR = "Internal Server Error";

    private static final String QUEUE_DELETED_RECENTLY_EXCEPTION = "You must wait 65 seconds after "
            + "unregistering the GPU(s) before you can register again";

    @Autowired
    private TelemetryEventClient telemetryEventClient;

    @ExceptionHandler(IcmsConflictException.class)
    public ResponseEntity<Object> conflictException(IcmsConflictException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getBody().getDetail());

        return new ResponseEntity<>(error, HttpStatus.CONFLICT);
    }

    @ExceptionHandler(QueueDeletedRecentlyException.class)
    public ResponseEntity<Object> handleQueueDeletedRecentlyException(
            QueueDeletedRecentlyException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(QUEUE_DELETED_RECENTLY_EXCEPTION);

        // AWS won't allow to create recently deleted queue for 60 sec, adding 5 sec as buffer
        HttpHeaders headers = new HttpHeaders();
        headers.add(HttpHeaders.RETRY_AFTER, "65");

        return new ResponseEntity<>(error, headers, HttpStatus.TOO_MANY_REQUESTS);
    }

    @ExceptionHandler(IcmsInternalServerException.class)
    public ResponseEntity<Object> internalServerException(IcmsInternalServerException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    // This is to handle the exceptions which we get while parsing http request
    // For ex. if incorrect value is given for an enum
    @ExceptionHandler(HttpMessageNotReadableException.class)
    public ResponseEntity<Object> handleMessageNotReadable(
            HttpMessageNotReadableException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getMessage());

        return new ResponseEntity<>(error, HttpStatus.BAD_REQUEST);
    }

    // This is to handle the exceptions which we get while parsing http request
    // For ex. if incorrect value is given for an enum
    @ExceptionHandler(MethodArgumentNotValidException.class)
    public ResponseEntity<Object> handleMethodArgumentInvalid(
            MethodArgumentNotValidException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        String errMsg = exception.getMessage();
        var fieldError = exception.getBindingResult().getFieldError();
        if (fieldError != null) {
            errMsg = fieldError.getDefaultMessage();
        }
        error.setError(errMsg);

        return new ResponseEntity<>(error, HttpStatus.BAD_REQUEST);
    }

    @ExceptionHandler(IcmsBadRequestException.class)
    public ResponseEntity<Object> icmsBadRequestException(IcmsBadRequestException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getBody().getDetail());

        return new ResponseEntity<>(error, HttpStatus.BAD_REQUEST);
    }

    @ExceptionHandler(MissingServletRequestParameterException.class)
    public ResponseEntity<Object> missingServletRequestParameterException(
            MissingServletRequestParameterException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getMessage());

        return new ResponseEntity<>(error, HttpStatus.BAD_REQUEST);
    }

    /**
     * Handles type conversion failures for path variables and request params (e.g. invalid UUID format).
     */
    @ExceptionHandler(MethodArgumentTypeMismatchException.class)
    public ResponseEntity<Object> handleMethodArgumentTypeMismatch(
            MethodArgumentTypeMismatchException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        String message = exception.getName() + " has invalid value: " + exception.getValue();
        error.setError(message);

        return new ResponseEntity<>(error, HttpStatus.BAD_REQUEST);
    }

    @ExceptionHandler(PreConditionFailedException.class)
    public ResponseEntity<Object> preConditionFailedException(
            PreConditionFailedException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getBody().getDetail());

        return new ResponseEntity<>(error, HttpStatus.PRECONDITION_FAILED);
    }

    @ExceptionHandler(IcmsForbiddenException.class)
    public ResponseEntity<Object> icmsForbiddenException(
            IcmsForbiddenException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getMessage());

        return new ResponseEntity<>(error, HttpStatus.FORBIDDEN);
    }

    @ExceptionHandler(IcmsNotFoundException.class)
    public ResponseEntity<Object> icmsNotFoundException(
            IcmsNotFoundException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getBody().getDetail());

        return new ResponseEntity<>(error, HttpStatus.NOT_FOUND);
    }

    // DataAccessException is the parent exception to all database related runtime exceptions
    @ExceptionHandler(DriverException.class)
    public ResponseEntity<Object> handleDriverException(DriverException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    // DriverException is the base class for all exceptions thrown by the driver.
    @ExceptionHandler(DataAccessException.class)
    public ResponseEntity<Object> handleDriverException(DataAccessException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    @ExceptionHandler(DriverTimeoutException.class)
    public ResponseEntity<Object> handleDriverTimeoutException(DriverTimeoutException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }


    // Handles all the AWS SQS and SNS Exception
    @ExceptionHandler(AmazonSQSException.class)
    public ResponseEntity<Object> handleAmazonSQSException(AmazonSQSException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    @ExceptionHandler(AmazonSNSException.class)
    public ResponseEntity<Object> handleAmazonSNSException(AmazonSNSException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);

        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    // Handle ApiKeys exceptions
    @ExceptionHandler(ApiKeysException.class)
    public ResponseEntity<Object> handleApiKeysException(ApiKeysException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);
        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    // If an exception handler is not setup for AuthenticationException, the
    // generic exception handler for Exception defined in ErrorHandler(base class)
    // will be picked. And, it will result in 500(INTERNAL_SERVER_ERROR) instead of
    // 401(UNAUTHORIZED) as the status code.
    @ExceptionHandler(AuthenticationException.class)
    public void handleAuthenticationException(
            HttpServletRequest req, HttpServletResponse res, AuthenticationException exception)
            throws IOException {
        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError("Authentication failure - " + exception.getMessage());
        res.setStatus(HttpServletResponse.SC_UNAUTHORIZED);
        res.setContentType(MediaType.APPLICATION_JSON_VALUE);
        res.getWriter().write(GsonCompatMapper.toJson(error));
    }

    @ExceptionHandler(IcmsAuthenticationException.class)
    public void handleIcmsAuthenticationException(
            HttpServletRequest req, HttpServletResponse res, IcmsAuthenticationException exception)
            throws IOException {
        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError("Authentication failure - " + exception.getMessage());
        res.setStatus(HttpServletResponse.SC_UNAUTHORIZED);
        res.setContentType(MediaType.APPLICATION_JSON_VALUE);
        res.getWriter().write(GsonCompatMapper.toJson(error));
    }

    // If an exception handler is not setup for AccessDeniedException, the
    // generic exception handler for Exception defined in ErrorHandler(base class)
    // will be picked. And, it will result in 500(INTERNAL_SERVER_ERROR) instead of
    // 403(FORBIDDEN) as the status code.
    @ExceptionHandler(AccessDeniedException.class)
    public ResponseEntity<Object> handleAccessDeniedException(
            HttpServletResponse res, AccessDeniedException exception) {
        CustomErrorResponse error = new CustomErrorResponse();
        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        error.setError(exception.getMessage());
        return new ResponseEntity<>(error, HttpStatus.FORBIDDEN);
    }

    // Generic error handler for all exceptions
    @ExceptionHandler(RuntimeException.class)
    public ResponseEntity<Object> handleRuntimeException(
            RuntimeException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);
        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    @ExceptionHandler(OptimisticLockException.class)
    public ResponseEntity<Object> handleOptimisticLockException(
            OptimisticLockException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);
        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    @ExceptionHandler(SqsMessageSenderClientException.class)
    public ResponseEntity<Object> handleSqsMessageSenderClientException(
            SqsMessageSenderClientException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);
        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    // Handle custom SNS exception
    @ExceptionHandler(SnsPublishException.class)
    public ResponseEntity<Object> handleSnsPublishException(SnsPublishException exception) {

        sendTelemetryEvent(exception, INTERNAL_SERVER_ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(INTERNAL_SERVER_ERROR);
        return new ResponseEntity<>(error, HttpStatus.INTERNAL_SERVER_ERROR);
    }

    @ExceptionHandler(IcmsHttpUnifiedErrorException.class)
    public ResponseEntity<Object> handleIcmsHttpUnifiedErrorException(IcmsHttpUnifiedErrorException exception) {

        sendTelemetryEvent(exception, Events.ERROR_EVENT.toString());
        CustomErrorResponse error = new CustomErrorResponse();
        error.setError(exception.getMessage());

        return new ResponseEntity<>(error, exception.getHttpStatus());
    }


    private void sendTelemetryEvent(Exception exception, String eventName) {

        try {
            /*
            The zeroth element of the array (assuming the array's length is non-zero) represents the top of the stack,
            which is the last method invocation in the sequence.
            Typically, this is the point at which this throwable was created and thrown.
             */

            StackTraceElement[] stackTrace = exception.getStackTrace();
            String className = "";
            String methodName = "";

            if (stackTrace.length > 0) {
                StackTraceElement origin = stackTrace[0];
                className = origin.getClassName();
                methodName = origin.getMethodName();
            }

            // Logging error
            log.error("{}: ERROR_MESSAGE {}, className: {}, methodName: {}, exception: ", eventName,
                    exception.getMessage(), className, methodName, exception);

            // Sending telemetry event
            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TelemetryEventClient.EventMetaData.ERROR_CLASS_NAME.getName(), className);
            metadata.put(TelemetryEventClient.EventMetaData.ERROR_METHOD_NAME.getName(),
                    methodName);

            List<GenericMetric> genericMetricList = List.of(new GenericMetric()
                    .withError(String.format("Exception: %s className: %s methodName: %s",
                            exception.getMessage(), className, methodName))
                    .withMetadata(metadata)
                    .withEventName(eventName));

            telemetryEventClient.triggerEvent(genericMetricList);
        } catch (Exception ex) {
            log.error("Exception occurred while sending telemetry event for error handling {}",
                    ex.getMessage());
        }
    }
}
