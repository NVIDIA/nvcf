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
import com.nvidia.icms.uec.internal.v2.exception.UnifiedErrorProvider;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Objects;


public class UnifiedErrorException extends RuntimeException implements UnifiedErrorProvider {

    private final DescriptiveUnifiedError unifiedError;
    private final UnifiedErrorData unifiedErrorData;

    public UnifiedErrorException(String message) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, message, null, null);
    }

    public UnifiedErrorException(String message, UnifiedErrorData unifiedErrorData) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, message, null, unifiedErrorData);
    }


    public UnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError) {
        this(unifiedError, unifiedError.errorDescription(), null, null);
    }

    public UnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, UnifiedErrorData unifiedErrorData) {
        this(unifiedError, unifiedError.errorDescription(), null, unifiedErrorData);
    }


    public UnifiedErrorException(String message, Throwable cause) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, message, cause, null);
    }

    public UnifiedErrorException(String message, Throwable cause, UnifiedErrorData unifiedErrorData) {
        this(IcmsUnifiedError.ICMS_UNDEFINED, message, cause, unifiedErrorData);
    }


    public UnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, String message) {
        this(unifiedError, message, null, null);
    }

    public UnifiedErrorException(@NotNull DescriptiveUnifiedError unifiedError, String message, UnifiedErrorData unifiedErrorData) {
        this(unifiedError, message, null, unifiedErrorData);
    }

    public UnifiedErrorException(
            @NotNull DescriptiveUnifiedError unifiedError, String message, Throwable cause, @Nullable UnifiedErrorData unifiedErrorData) {
        super(message, cause);
        this.unifiedError = unifiedError;

        this.unifiedErrorData = Objects.requireNonNullElseGet(unifiedErrorData,
                                                              UnifiedErrorData::new);
    }


    @Override
    public DescriptiveUnifiedError unifiedError() {
        return unifiedError;
    }

    public UnifiedErrorData unifiedErrorData() {
        return unifiedErrorData;
    }


}
