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
package com.nvidia.icms.uec.internal.v2.exception;

import com.nvidia.icms.uec.internal.v2.DescriptiveUnifiedError;
import lombok.NonNull;

/**
 * Base class for exceptions that should have UEC.
 */
public class UnifiedErrorException extends RuntimeException implements UnifiedErrorProvider {

    private final DescriptiveUnifiedError unifiedError;

    public UnifiedErrorException(
            String message,
            @NonNull DescriptiveUnifiedError unifiedError) {
        this(message, unifiedError, null);
    }

    public UnifiedErrorException(
            String message,
            @NonNull DescriptiveUnifiedError unifiedError,
            Throwable cause) {
        super(message, cause);
        this.unifiedError = unifiedError;
    }

    @Override
    public DescriptiveUnifiedError unifiedError() {
        return unifiedError;
    }
}
