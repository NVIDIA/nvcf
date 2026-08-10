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
package com.nvidia.icms.outbound.exception;

import org.springframework.security.core.AuthenticationException;

/**
 * ApiKeysException will be thrown from security layer hence it should extend AuthenticationException
 */
public class ApiKeysException extends AuthenticationException {

    private final boolean retryable;

    public ApiKeysException(String message) {
        super(message);
        this.retryable = false;
    }

    public ApiKeysException(String message, boolean retryable) {
        super(message);
        this.retryable = retryable;
    }

    public ApiKeysException(String message, Throwable throwable) {
        super(message, throwable);
        this.retryable = false;
    }

    public boolean isRetryable() {
        return retryable;
    }
}
