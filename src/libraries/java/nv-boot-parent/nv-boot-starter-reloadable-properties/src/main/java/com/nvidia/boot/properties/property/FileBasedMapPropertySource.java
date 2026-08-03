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

package com.nvidia.boot.properties.property;

import java.io.File;
import java.util.Map;
import org.springframework.core.env.MapPropertySource;

/**
 * MapPropertySource backed by a file, tracking lastModified for change detection.
 */
public class FileBasedMapPropertySource extends MapPropertySource {

    private final File file;
    private final long lastModified;

    public FileBasedMapPropertySource(
            String name,
            Map<String, Object> source,
            File file,
            long lastModified) {
        super(name, source);
        this.file = file;
        this.lastModified = lastModified;
    }

    public File getFile() {
        return file;
    }

    public long getLastModified() {
        return lastModified;
    }
}
