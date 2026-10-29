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

import java.time.Duration;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;

/**
 * A dependency such as the database is temporarily unavailable. Sets {@code Retry-After} so
 * clients back off, unless created with {@link #withoutRetryAfter}; the default covers a typical
 * node failover.
 */
public class ServiceUnavailableException extends BootResponseException {

    public static final Duration DEFAULT_RETRY_AFTER = Duration.ofSeconds(10);

    public ServiceUnavailableException(String message) {
        this(message, null, DEFAULT_RETRY_AFTER);
    }

    public ServiceUnavailableException(String message, Throwable cause) {
        this(message, cause, DEFAULT_RETRY_AFTER);
    }

    public ServiceUnavailableException(String message, Throwable cause, Duration retryAfter) {
        super(HttpStatus.SERVICE_UNAVAILABLE, message, cause, ServiceUnavailableException.class);
        if (retryAfter != null) {
            getHeaders().set(HttpHeaders.RETRY_AFTER,
                             Long.toString(Math.max(1, retryAfter.toSeconds())));
        }
    }

    /**
     * Omits {@code Retry-After}, for failures where a blind retry is unsafe, such as a create
     * whose outcome is unknown: retrying it can create a second resource.
     */
    public static ServiceUnavailableException withoutRetryAfter(String message, Throwable cause) {
        return new ServiceUnavailableException(message, cause, null);
    }
}
