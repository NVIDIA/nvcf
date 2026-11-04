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

package com.nvidia.boot.properties.util;

import java.io.File;
import java.io.IOException;
import org.springframework.core.io.Resource;

/**
 * Checks if a resource is a physical file on the filesystem.
 */
public final class IsPhysicalFilePredicate {

    private IsPhysicalFilePredicate() {
    }

    /**
     * Checks if the given resource is a file.
     * Note: {@link Resource#isFile()} does not guarantee {@link Resource#getFile()} will succeed.
     */
    public static boolean test(Resource resource) {
        try {
            File file = resource.getFile();
            return file.isFile() && file.exists();
        } catch (IOException e) {
            return false;
        }
    }
}
