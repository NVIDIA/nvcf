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
package com.nvidia.icms.uec;

import com.nvidia.icms.uec.internal.v2.DescriptiveUnifiedError;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import org.springframework.http.HttpStatus;

public class IcmsHttpUnifiedErrorException  extends UnifiedErrorException{

    private final HttpStatus httpStatus;

    public IcmsHttpUnifiedErrorException(HttpStatus httpStatus, String message) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, httpStatus, message, null, null);
    }

    public IcmsHttpUnifiedErrorException(HttpStatus httpStatus, String message, UnifiedErrorData unifiedErrorData) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, httpStatus, message, null, unifiedErrorData);
    }


    public IcmsHttpUnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, HttpStatus httpStatus) {
        this(unifiedError, httpStatus, unifiedError.errorDescription(), null, null);
    }

    public IcmsHttpUnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, HttpStatus httpStatus, UnifiedErrorData unifiedErrorData) {
        this(unifiedError, httpStatus, unifiedError.errorDescription(), null, unifiedErrorData);
    }


    public IcmsHttpUnifiedErrorException(HttpStatus httpStatus, String message, Throwable cause) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, httpStatus, message, cause, null);
    }

    public IcmsHttpUnifiedErrorException(HttpStatus httpStatus, String message, Throwable cause, UnifiedErrorData unifiedErrorData) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, httpStatus, message, cause, unifiedErrorData);
    }


    public IcmsHttpUnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, HttpStatus httpStatus, String message) {
        this(unifiedError, httpStatus, message, null, null);
    }

    public IcmsHttpUnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, HttpStatus httpStatus, String message, UnifiedErrorData unifiedErrorData) {
        this(unifiedError, httpStatus, message, null, unifiedErrorData);
    }


    public IcmsHttpUnifiedErrorException(
            @NotNull DescriptiveUnifiedError unifiedError, HttpStatus httpStatus, String message, Throwable cause, @Nullable UnifiedErrorData unifiedErrorData) {
        super(unifiedError, message, cause, unifiedErrorData);
        this.httpStatus = httpStatus;
    }

    public HttpStatus getHttpStatus() {
        return httpStatus;
    }

}
