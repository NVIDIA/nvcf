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
package com.nvidia.nvct;

import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ArrayNode;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.boot.audit.AuditProperties;
import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServer;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import jakarta.annotation.Nonnull;
import java.io.File;
import java.io.IOException;
import java.net.JarURLConnection;
import java.net.URISyntaxException;
import java.net.URL;
import java.nio.file.FileVisitOption;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.PosixFilePermissions;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Base64;
import java.util.Comparator;
import java.util.Enumeration;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.jar.JarEntry;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.test.util.TestPropertyValues;
import org.springframework.context.ApplicationContextInitializer;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.data.cassandra.repository.config.EnableCassandraRepositories;
import org.testcontainers.containers.ComposeContainer;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.containers.wait.strategy.WaitAllStrategy;
import org.testcontainers.utility.DockerImageName;

@Slf4j
@Configuration
@EnableCassandraRepositories(basePackages = "com.nvidia")
public class IntegrationTestConfiguration {

    /**
     * Classpath prefix for compose assets bundled in nvct-core-tests.jar (see nvct-core {@code
     * maven-resources-plugin} {@code copy-integration-local-env}).
     */
    private static final String LOCAL_ENV_CLASSPATH_PREFIX = "local_env";

    private static final String COMPOSE_FILE_NAME = "docker-compose.test.yml";

    private static final String DOCKER_COMPOSE_IMAGE = "docker:24.0.2";

    /**
     * Relative to working directory (legacy / IDE): {@code local_env/docker-compose.test.yml}.
     */
    private static final String FS_FALLBACK_COMPOSE =
            LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME;

    /**
     * If {@code false}, failure to create an extract directory under {@code target/} is a hard
     * error (set in CI via {@code -D...=false} or env). Default {@code true} keeps a
     * java.io.tmpdir fallback for constrained local environments.
     */
    private static final String ALLOW_TMPDIR_FALLBACK_PROPERTY =
            "nvct.integration.extract.allowTmpdirFallback";

    private static final String ALLOW_TMPDIR_FALLBACK_ENV = "NVCT_INTEGRATION_ALLOW_TMPDIR_FALLBACK";

    private static final List<Path> INTEGRATION_EXTRACT_ROOTS = new CopyOnWriteArrayList<>();

    private static final AtomicBoolean INTEGRATION_EXTRACT_SHUTDOWN_HOOK = new AtomicBoolean();

    private static final String KEY_SPACE = "nvct";
    private static final String CASSANDRA_SERVICE_NAME = "cassandra-1";
    private static final int CASSANDRA_PORT = 9042;

    public static final MockOAuth2TokenServer MOCK_OAUTH2_TOKEN_SERVER;  // Used in test files
    private static final String OAUTH2_TOKEN_ISSUER = "http://localhost:9092";
    private static final String OAUTH2_KEYSET_URL = "http://localhost:9092/.well-known/jwks.json";

    private static String CASSANDRA_HOST;
    private static int CASSANDRA_MAPPED_PORT;

    private static final String AUDIT_SIGNING_KID = "integration-audit-kid";
    private static final byte[] AUDIT_SIGNING_KEY_RAW = new byte[32];

    static {
        for (int i = 0; i < AUDIT_SIGNING_KEY_RAW.length; i++) {
            AUDIT_SIGNING_KEY_RAW[i] = (byte) (i + 1);
        }

        var cassandraWaitStrategy = new WaitAllStrategy(WaitAllStrategy.Mode.WITH_OUTER_TIMEOUT);
        cassandraWaitStrategy.withStartupTimeout(Duration.of(3, ChronoUnit.MINUTES));
        cassandraWaitStrategy.withStrategy(
                Wait.forLogMessage(".*Cassandra init scripts executed.*", 1));

        File composeDir = resolveComposeAssetDirectory();
        File composeFile = new File(composeDir, COMPOSE_FILE_NAME);
        if (!composeFile.isFile()) {
            throw new IllegalStateException(
                    "Compose file missing at " + composeFile.getAbsolutePath());
        }

        var composeContainer = createComposeContainer(composeFile)
                .withExposedService(CASSANDRA_SERVICE_NAME,
                                    CASSANDRA_PORT,
                                    cassandraWaitStrategy)
                .withBuild(false);

        composeContainer.start();

        CASSANDRA_HOST = composeContainer.getServiceHost(CASSANDRA_SERVICE_NAME, CASSANDRA_PORT);
        CASSANDRA_MAPPED_PORT = composeContainer.getServicePort(CASSANDRA_SERVICE_NAME,
                                                                CASSANDRA_PORT);
        composeContainer.getContainerByServiceName(CASSANDRA_SERVICE_NAME)
                .ifPresentOrElse(
                        container -> log.info(container.getLogs()),
                        () -> {
                            throw new IllegalStateException("Missing container "
                                                                    + CASSANDRA_SERVICE_NAME);
                        });


        MOCK_OAUTH2_TOKEN_SERVER = new MockOAuth2TokenServer(
                new OAuth2TokenServerConfigurationProperties(OAUTH2_TOKEN_ISSUER, OAUTH2_KEYSET_URL,
                                                             null, null, null, null));
        MOCK_OAUTH2_TOKEN_SERVER.start();
    }

    /**
     * Directory containing {@link #COMPOSE_FILE_NAME} and the {@code cassandra/} subtree required
     * by that compose file (volume mounts use paths relative to the compose file).
     */
    private static File resolveComposeAssetDirectory() {
        var cl = IntegrationTestConfiguration.class.getClassLoader();
        var composeUrl = cl.getResource(LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME);
        if (composeUrl == null) {
            var fallbackCompose = new File(FS_FALLBACK_COMPOSE);
            if (fallbackCompose.isFile()) {
                File dir = fallbackCompose.getParentFile();
                log.info("Using compose bundle from filesystem: {}", dir.getAbsolutePath());
                return materializeComposeAssets(dir);
            }
            throw new IllegalStateException(
                    "Missing classpath resource "
                            + LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME
                            + " (ensure nvct-core-tests.jar is on the test classpath and built with"
                            + " copy-integration-local-env), and no " + FS_FALLBACK_COMPOSE
                            + " in the working directory.");
        }

        try {
            if ("file".equalsIgnoreCase(composeUrl.getProtocol())) {
                var composePath = Path.of(composeUrl.toURI());
                var dir = composePath.getParent().toFile();
                log.debug("Using compose bundle from filesystem (test-classes): {}",
                          dir.getAbsolutePath());
                return materializeComposeAssets(dir);
            }

            if ("jar".equalsIgnoreCase(composeUrl.getProtocol())) {
                return extractLocalEnvFromJar(composeUrl);
            }
        } catch (IOException | URISyntaxException e) {
            throw new IllegalStateException("Failed to resolve compose bundle", e);
        }

        throw new IllegalStateException(
                "Unsupported compose URL protocol: " + composeUrl + "; expected file or jar.");
    }

    /**
     * Extract {@code local_env/**} from the tests JAR so Docker can bind-mount files by host path.
     * Uses {@code target/nvct-integration-local-env-*} under the module directory (same class of path
     * as checkout-local {@code local_env/}), not {@code java.io.tmpdir}. Nested/containerised Compose
     * in CI often fails mounts from {@code /tmp} while mounts from the job workspace succeed.
     */
    private static File extractLocalEnvFromJar(URL composeUrlInsideJar) throws IOException {

        JarURLConnection connection = (JarURLConnection) composeUrlInsideJar.openConnection();
        try (var jarFile = connection.getJarFile()) {
            var extractRoot = createIntegrationExtractDirectory();
            var prefix = LOCAL_ENV_CLASSPATH_PREFIX + "/";

            Enumeration<JarEntry> entries = jarFile.entries();
            while (entries.hasMoreElements()) {
                var entry = entries.nextElement();
                var name = entry.getName();
                if (!name.startsWith(prefix) || entry.isDirectory()) {
                    continue;
                }

                var dest = extractRoot.resolve(name.substring(prefix.length())).normalize();
                // Zip-slip guard: a crafted entry name (e.g. containing "..") must
                // never resolve outside the extraction root.
                if (!dest.startsWith(extractRoot)) {
                    throw new IOException(
                            "Refusing to extract entry outside the target directory: " + name);
                }
                Files.createDirectories(dest.getParent());
                try (var in = jarFile.getInputStream(entry)) {
                    Files.copy(in, dest, StandardCopyOption.REPLACE_EXISTING);
                }
                if (name.endsWith("entrypoint.sh")) {
                    try {
                        Files.setPosixFilePermissions(dest,
                                PosixFilePermissions.fromString("rwxr-xr-x"));
                    } catch (UnsupportedOperationException ignored) {
                        // Non-POSIX filesystems (e.g. Windows): Docker Desktop still runs via bash.
                    } catch (IOException e) {
                        throw new IllegalStateException("Could not chmod entrypoint script", e);
                    }
                }
            }

            log.info("Extracted integration compose bundle from {} to {}",
                     jarFile.getName(), extractRoot.toAbsolutePath());
            return extractRoot.toFile();
        }
    }

    /**
     * Copy a filesystem compose bundle into a directory of real files, dereferencing symlinks.
     *
     * <p>Under Bazel the test runfiles present {@code local_env/**} as symlinks into the build
     * sandbox. Docker bind-mounts of a directory follow the mount but leave those symlinks dangling
     * inside the container (their targets do not exist there), so {@code ./cassandra/schema} mounts
     * empty: {@code entrypoint.sh} finds no {@code *.cql}, the {@code nvct} keyspace is never
     * created, and Spring fails to start with {@code InvalidKeyspaceException}. Materialising to real
     * files makes the bind mounts serve actual content, the same way {@link #extractLocalEnvFromJar}
     * does for the Maven/JAR path. A no-op-ish copy on a plain checkout (real files already).
     */
    private static File materializeComposeAssets(File sourceDir) {
        try {
            var srcRoot = sourceDir.toPath();
            var extractRoot = createIntegrationExtractDirectory();
            try (var walk = Files.walk(srcRoot, FileVisitOption.FOLLOW_LINKS)) {
                for (var src : (Iterable<Path>) walk::iterator) {
                    var dest = extractRoot.resolve(srcRoot.relativize(src).toString());
                    if (Files.isDirectory(src)) {
                        Files.createDirectories(dest);
                        continue;
                    }
                    Files.createDirectories(dest.getParent());
                    Files.copy(src, dest, StandardCopyOption.REPLACE_EXISTING);
                    if ("entrypoint.sh".equals(src.getFileName().toString())) {
                        try {
                            Files.setPosixFilePermissions(dest,
                                    PosixFilePermissions.fromString("rwxr-xr-x"));
                        } catch (UnsupportedOperationException ignored) {
                            // Non-POSIX filesystem; Docker still runs the script via bash.
                        }
                    }
                }
            }
            log.info("Materialized integration compose bundle from {} to {}", srcRoot,
                     extractRoot.toAbsolutePath());
            return extractRoot.toFile();
        } catch (IOException e) {
            throw new IllegalStateException(
                    "Failed to materialize compose bundle from " + sourceDir, e);
        }
    }

    /**
     * Creates a directory for JAR-extracted compose assets, preferring {@code target/} under the
     * module. Registers recursive deletion on JVM exit (unlike {@link File#deleteOnExit()} on a
     * directory, which does not remove contents).
     */
    private static Path createIntegrationExtractDirectory() throws IOException {
        var target = Path.of(System.getProperty("user.dir"), "target");
        try {
            Files.createDirectories(target);
            var dir = Files.createTempDirectory(target, "nvct-integration-local-env-");
            registerExtractRootForCleanup(dir);
            return dir;
        } catch (IOException e) {
            if (!isTmpdirFallbackAllowed()) {
                throw new IllegalStateException(
                        "Could not create integration extract directory under " + target.toAbsolutePath()
                                + ". Set " + ALLOW_TMPDIR_FALLBACK_PROPERTY + "=true or "
                                + ALLOW_TMPDIR_FALLBACK_ENV + "=true to allow java.io.tmpdir, or fix "
                                + "permissions on target/.",
                        e);
            }
            log.warn("Could not extract under {}; using java.io.tmpdir: {}",
                     target.toAbsolutePath(), e.toString());
            var fallback = Files.createTempDirectory("nvct-integration-local-env");
            registerExtractRootForCleanup(fallback);
            return fallback;
        }
    }

    private static boolean isTmpdirFallbackAllowed() {
        var fromEnv = System.getenv(ALLOW_TMPDIR_FALLBACK_ENV);
        if (fromEnv != null && !fromEnv.isBlank()) {
            return Boolean.parseBoolean(fromEnv);
        }
        return Boolean.parseBoolean(System.getProperty(ALLOW_TMPDIR_FALLBACK_PROPERTY, "true"));
    }

    private static void registerExtractRootForCleanup(Path root) {
        if (INTEGRATION_EXTRACT_SHUTDOWN_HOOK.compareAndSet(false, true)) {
            Runtime.getRuntime().addShutdownHook(
                    new Thread(IntegrationTestConfiguration::deleteIntegrationExtractRoots,
                               "nvct-integration-extract-cleanup"));
        }
        INTEGRATION_EXTRACT_ROOTS.add(root);
    }

    private static void deleteIntegrationExtractRoots() {
        for (var root : INTEGRATION_EXTRACT_ROOTS) {
            deleteRecursively(root);
        }
    }

    private static void deleteRecursively(Path root) {
        if (root == null || !Files.exists(root)) {
            return;
        }
        try (Stream<Path> walk = Files.walk(root)) {
            walk.sorted(Comparator.reverseOrder()).forEach(
                    p -> {
                        try {
                            Files.deleteIfExists(p);
                        } catch (IOException ignored) {
                            // best-effort (shutdown hook)
                        }
                    });
        } catch (IOException ignored) {
            // walk failed — best-effort
        }
    }

    // This is needed for tests to run consistently locally and also in the CI pipeline.
    // Testcontainers 2.x uses the host's Docker Compose CLI for the File-only constructor. The
    // Maven image used in the CI pipeline can access the Docker daemon, but it does not include
    // the Docker Compose CLI. So, use local Docker Compose CLI when available. Otherwise, run
    // Compose using a pinned Docker image for reproducible local and CI test execution.
    private static ComposeContainer createComposeContainer(File composeFile) {
        if (isLocalComposeAvailable()) {
            return new ComposeContainer(composeFile);
        }

        return new ComposeContainer(DockerImageName.parse(DOCKER_COMPOSE_IMAGE), composeFile);
    }

    private static boolean isLocalComposeAvailable() {
        try {
            var process = new ProcessBuilder("docker", "compose", "version")
                    .redirectErrorStream(true)
                    .start();
            boolean completed = process.waitFor(5, TimeUnit.SECONDS);
            if (!completed) {
                process.destroyForcibly();
                return false;
            }
            return process.exitValue() == 0;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (IOException e) {
            return false;
        }
    }

    public static class Initializer implements
            ApplicationContextInitializer<ConfigurableApplicationContext> {

        @Override
        public void initialize(@Nonnull ConfigurableApplicationContext applicationContext) {
            TestPropertyValues.of(
                    "spring.cassandra.contact-points=" + CASSANDRA_HOST,
                    "spring.cassandra.port=" + CASSANDRA_MAPPED_PORT,
                    "spring.cassandra.local-datacenter=datacenter1",
                    "spring.cassandra.keyspace-name=" + KEY_SPACE,
                    "spring.cassandra.ssl.enabled=false",
                    "spring.cassandra.ssl.bundle=",
                    "spring.security.oauth2.resourceserver.jwt.issuer-uri=" + OAUTH2_TOKEN_ISSUER,
                    "spring.security.oauth2.resourceserver.jwt.jwk-set-uri=" + OAUTH2_KEYSET_URL,
                    "spring.security.oauth2.resourceserver.jwt.jws-algorithms=ES256"
            ).applyTo(applicationContext);
        }
    }

    /**
     * Mock clock so it can be set to appropriate value for functional testing.
     */
    @Primary
    @Bean("mockClock")
    public Clock getMockClock() {
        var clock = mock(Clock.class);
        when(clock.instant()).thenReturn(Instant.now());
        return clock;
    }

    @Bean
    public AuditProperties auditProperties(JsonMapper jsonMapper) {
        ArrayNode keys = jsonMapper.createArrayNode();
        ObjectNode entry = jsonMapper.createObjectNode();
        entry.put("kid", AUDIT_SIGNING_KID);
        entry.put("key", Base64.getEncoder().encodeToString(AUDIT_SIGNING_KEY_RAW));
        keys.add(entry);

        ObjectNode root = jsonMapper.createObjectNode();
        root.set("keys", keys);

        var props = new AuditProperties();
        props.setHmacKid(AUDIT_SIGNING_KID);
        try {
            props.setHmacKeys(Base64.getEncoder()
                                      .encodeToString(jsonMapper.writeValueAsBytes(root)));
        } catch (JacksonException e) {
            throw new IllegalStateException("Failed to serialize HMAC keys configuration", e);
        }
        return props;
    }
}
