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

import java.util.Optional;

/**
 * Utility class that helps to extract {@link DescriptiveUnifiedError}
 * from a hierarchy of {@link Exception}.
 * Since Spring sometimes wraps original exceptions in framework specific exceptions
 * we want to save the "busy work" of unwrapping them manually and use this class instead.
 */
public class UnifiedErrorProviderUtils {

    public static final int DEFAULT_DEPTH = 4;

    private UnifiedErrorProviderUtils() {
    }

    public static Optional<DescriptiveUnifiedError> findError(Throwable throwable) {
        return findError(throwable, DEFAULT_DEPTH);
    }

    public static Optional<DescriptiveUnifiedError> findError(Throwable throwable, int depth) {
        var current = throwable;
        var remainingDepth = depth;
        while (current != null && remainingDepth > 0) {
            if (current instanceof UnifiedErrorProvider uecProvider) {
                var uec = uecProvider.unifiedError();
                if (uec != null) {
                    return Optional.of(uec);
                }
            }
            current = current.getCause();
            remainingDepth--;
        }

        return Optional.empty();
    }
}
