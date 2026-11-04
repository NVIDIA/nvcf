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

import static java.nio.file.Files.readAllBytes;

import java.io.File;
import java.io.FileOutputStream;
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.file.Paths;
import java.util.jar.JarOutputStream;
import java.util.zip.ZipEntry;
import lombok.SneakyThrows;

/**
 * Test utilities for creating and loading JARs. Used by integration tests to simulate
 * classpath-only resources (e.g. properties file inside a JAR, not on filesystem).
 */
public final class TestJarUtils {

    private TestJarUtils() {
    }

    /**
     * Loads a JAR into the current thread's context class loader.
     * After loading, the JAR's classes and resources can be accessed.
     * <p>
     * Creates a new class loader with the given JAR and the current context class loader
     * as parent, then replaces the thread's context class loader. Spring delegates
     * resource loading to the thread context class loader by default.
     *
     * @param file path to the JAR
     */
    @SneakyThrows
    public static void loadJar(File file) {
        var currentThread = Thread.currentThread();
        var originalCL = currentThread.getContextClassLoader();
        currentThread.setContextClassLoader(new TestJarUtilsClassLoader(file, originalCL));
    }

    /**
     * Creates a JAR containing the given properties file.
     *
     * @param inputPropertiesPath  path to the source properties file
     * @param outputPropertiesPath  path of the file inside the JAR
     * @return the created JAR file
     */
    @SneakyThrows
    public static File createJar(String inputPropertiesPath, String outputPropertiesPath) {
        var location = File.createTempFile("testJar", ".jar");

        try (var fileOut = new FileOutputStream(location);
             var jarOut = new JarOutputStream(fileOut)) {
            jarOut.putNextEntry(new ZipEntry(outputPropertiesPath));
            jarOut.write(readAllBytes(Paths.get(inputPropertiesPath)));
            jarOut.closeEntry();
        }

        location.deleteOnExit();
        return location;
    }

    static class TestJarUtilsClassLoader extends URLClassLoader {
        TestJarUtilsClassLoader(File jarFile, ClassLoader parent) throws Exception {
            super(new URL[] {jarFile.toURI().toURL()}, parent);
        }
    }
}
