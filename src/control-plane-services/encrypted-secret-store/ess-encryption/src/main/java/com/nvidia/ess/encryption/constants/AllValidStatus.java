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
package com.nvidia.ess.encryption.constants;

import java.util.Collections;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.experimental.UtilityClass;

@UtilityClass
public class AllValidStatus {

    private static Set<EncryptionKeyStatus> listAllValidStatus() {
        return Collections.unmodifiableSet(Set.of(
            EncryptionKeyStatus.CREATION_VALIDATED,
            EncryptionKeyStatus.VALIDATED
        ));
    }

    private static final Set<EncryptionKeyStatus> ALL_VALID_STATUS = listAllValidStatus();

    public static Set<EncryptionKeyStatus> allValidStatus() {
        return ALL_VALID_STATUS;
    }

    private static Set<String> listAllValidStatusStrings() {
        return listAllValidStatus().stream().map(EncryptionKeyStatus::name).collect(Collectors.toUnmodifiableSet());
    }

    private static final Set<String> ALL_VALID_STATUS_STRINGS = listAllValidStatusStrings();

    public static Set<String> allValidStatusStrings() {
        return ALL_VALID_STATUS_STRINGS;
    }
}
