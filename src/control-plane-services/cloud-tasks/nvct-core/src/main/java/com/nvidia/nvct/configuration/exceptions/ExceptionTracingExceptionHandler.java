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
package com.nvidia.nvct.configuration.exceptions;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.handlers.BootMvcExceptionHandler;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.context.request.WebRequest;
import org.springframework.web.reactive.function.client.WebClientResponseException;

public class ExceptionTracingExceptionHandler extends BootMvcExceptionHandler {

    @Autowired
    private Tracer tracer;

    @Override
    protected void recordException(Exception ex) {
        NvctUtils.recordExceptionUsingCurrentSpan(tracer, ex);
    }

    @ExceptionHandler(WebClientResponseException.BadRequest.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.BadRequest ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.Unauthorized.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.Unauthorized ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new UnauthorizedException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.Forbidden.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.Forbidden ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new ForbiddenException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.NotFound.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.NotFound ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new NotFoundException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.Conflict.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.Conflict ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new ConflictException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.TooManyRequests.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.TooManyRequests ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new TooManyRequestsException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.MethodNotAllowed.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.MethodNotAllowed ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.NotAcceptable.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.NotAcceptable ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.Gone.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.Gone ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.UnsupportedMediaType.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.UnsupportedMediaType ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

    @ExceptionHandler(WebClientResponseException.UnprocessableContent.class)
    protected ResponseEntity<Object> handleException(
            WebClientResponseException.UnprocessableContent ex,
            WebRequest request) throws Exception {
        return super.handleException(
                new BadRequestException(ex.getMessage(), ex.getCause()), request);
    }

}
