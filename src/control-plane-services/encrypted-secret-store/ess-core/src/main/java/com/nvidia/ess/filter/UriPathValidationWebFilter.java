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
package com.nvidia.ess.filter;

import static com.nvidia.ess.constants.Constants.MSG_ILLEGAL_URI;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.exceptions.InternalErrorException;
import com.nvidia.ess.utils.ExceptionUtils;
import com.nvidia.ess.validator.NotBlankAndUriSafeValidationHelper;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import tools.jackson.databind.ObjectMapper;

@Slf4j
@Component
public class UriPathValidationWebFilter implements WebFilter {

    @Setter(onMethod_ = {@Autowired})
    private NotBlankAndUriSafeValidationHelper helper;

    @Setter(onMethod_ = {@Autowired})
    private CustomExceptionHandler errorHandler;

    @Setter(onMethod_ = {@Autowired})
    private ObjectMapper objectMapper;

    @Override
    public Mono<Void> filter(ServerWebExchange exchange, WebFilterChain chain) {

        // notBlankAndUriSafe({decoded-uri-path}, {raw-uri-path})
        if (helper.notBlankAndUriSafe(exchange.getRequest().getURI().getPath(),
                    exchange.getRequest().getURI().getRawPath())) {
            // Validation successful on URI.
            // Delegate to the next filter in the chain.
            return chain.filter(exchange);
        }

        // Validation failed on URI.
        // Shortcircuit request-processing and push an error-response to client.

        // Explicitly call the ExceptionHandler to construct a ResponseEntity as it is a
        // @ControllerAdvice and won't be invoked automatically (this request failed before
        // being routed to a controller).
        return errorHandler.handleException(new BadRequestException(MSG_ILLEGAL_URI), exchange)
                .flatMap(responseEntity -> ExceptionUtils.writeErrorResponse(exchange, responseEntity, objectMapper))
            .onErrorMap(ex -> !(ex instanceof BootResponseException), ex -> {
                log.error("Unhandled non-BootResponseException: ", ex);
                return new InternalErrorException();
            });
    }
  
}
