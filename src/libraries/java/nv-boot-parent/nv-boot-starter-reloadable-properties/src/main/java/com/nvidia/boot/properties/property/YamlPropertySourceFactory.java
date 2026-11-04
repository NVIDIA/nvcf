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

import static java.nio.charset.StandardCharsets.UTF_8;

import com.google.common.io.CharStreams;
import com.nvidia.boot.properties.util.IsPhysicalFilePredicate;
import java.io.IOException;
import java.io.InputStreamReader;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import org.springframework.boot.env.OriginTrackedMapPropertySource;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.PropertySource;
import org.springframework.core.io.ByteArrayResource;
import org.springframework.core.io.FileSystemResourceLoader;
import org.springframework.core.io.Resource;

/**
 * Creates MapPropertySource from YAML/JSON files.
 */
public class YamlPropertySourceFactory {

    /**
     * Creates a property source from the given file path.
     *
     * <p>
     * To specify an absolute path resource use "file:" prefix.
     * To specify a classpath resource use "classpath:" prefix.
     * To specify a relative path resource use no prefix.
     *
     * @param name name of the property source
     * @param file path to the file
     * @return MapPropertySource (FileBasedMapPropertySource for physical files)
     */
    public MapPropertySource createPropertySource(String name, String file) {
        try {
            var resource = new FileSystemResourceLoader().getResource(file);

            var propertySource = IsPhysicalFilePredicate.test(resource)
                    ? createFileBasedPropertySource(name, resource)
                    : createRegularPropertySource(name, resource);
            validatePropertySource(propertySource);
            return propertySource;
        } catch (Exception ex) {
            var mesg = "Failed to create property source from [" + file + "]";
            throw new IllegalArgumentException(mesg, ex);
        }
    }

    private static MapPropertySource createRegularPropertySource(
            String name,
            Resource resource) throws IOException {
        return new MapPropertySource(name, createStorage(name, resource));
    }

    private static FileBasedMapPropertySource createFileBasedPropertySource(
            String name,
            Resource fileResource) throws IOException {
        var lastModifiedBefore = fileResource.lastModified();
        var storage = createStorage(name, fileResource);
        var lastModifiedAfter = fileResource.lastModified();

        // If last modified timestamps are different - there is no guaranteed way to say which one
        // was actually read by us. Do one more attempt.
        if (lastModifiedBefore != lastModifiedAfter) {
            return createFileBasedPropertySource(name, fileResource);
        }

        return new FileBasedMapPropertySource(
                name,
                storage,
                fileResource.getFile(),
                lastModifiedBefore
        );
    }

    private static Map<String, Object> createStorage(
            String name,
            Resource resource) throws IOException {
        try (var reader = new InputStreamReader(resource.getInputStream(), UTF_8)) {
            var content = CharStreams.toString(reader);
            var storage = new HashMap<String, Object>();

            loadPropertySources(name, content)
                    .stream()
                    .map(OriginTrackedMapPropertySource.class::cast)
                    .forEach(source -> fillStorage(storage, source));

            return storage;
        }
    }

    private static List<PropertySource<?>> loadPropertySources(
            String name,
            String content) throws IOException {
        try {
            var resource = new ByteArrayResource(content.getBytes(UTF_8));
            return new YamlPropertySourceLoader().load(name, resource);
        } catch (Exception ex) {
            throw new RuntimeException("Malformed file content", ex);
        }
    }

    private static void fillStorage(
            Map<String, Object> storage,
            OriginTrackedMapPropertySource source) {
        for (var propertyName : source.getPropertyNames()) {
            if (storage.containsKey(propertyName)) {
                throw new IllegalArgumentException("Duplicate key [" + propertyName + "]");
            }
            var value = source.getProperty(propertyName);
            storage.put(propertyName, Objects.requireNonNull(value));
        }
    }

    private static void validatePropertySource(MapPropertySource propertySource) {
        var propertyNames = propertySource.getPropertyNames();
        if (propertyNames.length == 0) {
            var mesg = "Property source [" + propertySource.getName() + "] is empty.";
            throw new IllegalArgumentException(mesg);
        }
    }
}
