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
package com.nvidia.ess.encryption.crypto.key.validation;

import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import lombok.EqualsAndHashCode;
import lombok.Getter;
import lombok.NonNull;
import lombok.ToString;
import org.springframework.util.Assert;

@Getter
@ToString
@EqualsAndHashCode
public class KeyValidationResult {
    private final boolean valid;
    private final KeyFetchError validationError;

    private KeyValidationResult(boolean valid, KeyFetchError validationError) {
        this.valid = valid;
        this.validationError = validationError;
    }

    public static KeyValidationResult success() {
        return new KeyValidationResult(true, null);
    }

    public static KeyValidationResult failure(@NonNull KeyFetchError e) {
        Assert.notNull(e, "cannot be null");
        Assert.notNull(e.getErrorException(), "cannot be null");
        return new KeyValidationResult(false, e);
    }
}
