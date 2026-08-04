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
package com.nvidia.icms.integration;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;

import com.datastax.driver.core.Cluster;
import com.datastax.driver.core.Session;
import com.github.tomakehurst.wiremock.client.MappingBuilder;
import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServer;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.boot.mock.oauth2.TokenEndpointResponseTransformer;
import com.nvidia.icms.IcmsTestApp;
import com.nvidia.icms.extension.CassandraCleanerExtension;
import com.nvidia.icms.util.CassandraTestConfiguration;
import com.nvidia.icms.util.JwtKeyUtils;
import jakarta.annotation.Nonnull;
import java.io.File;
import java.io.IOException;
import java.net.JarURLConnection;
import java.net.URISyntaxException;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.PosixFilePermissions;
import java.time.Duration;
import java.time.temporal.ChronoUnit;
import java.util.Comparator;
import java.util.Enumeration;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.jar.JarEntry;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.util.TestPropertyValues;
import org.springframework.context.ApplicationContextInitializer;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Import;
import org.springframework.test.context.ActiveProfiles;
import org.springframework.test.context.ContextConfiguration;
import org.testcontainers.containers.ComposeContainer;
import org.testcontainers.containers.ContainerState;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.containers.wait.strategy.WaitAllStrategy;
import org.testcontainers.utility.DockerImageName;

@SpringBootTest(
        classes = IcmsTestApp.class,
        properties = {"management.server.port=", "spring.profiles.active=test"})
@AutoConfigureMockMvc
@ContextConfiguration(initializers = {IntegrationTest.Initializer.class})
@ExtendWith(CassandraCleanerExtension.class)
@ActiveProfiles("test")
@Slf4j
@Import({CassandraTestConfiguration.class})
public class IntegrationTest {
    public static final ContainerState AWS_LOCALSTACK_CONTAINER;
    public static final ContainerState NATS_LOCALSTACK_CONTAINER;
    public static final ContainerState CASSANDRA_CONTAINER;
    public static final MockOAuth2TokenServer MOCK_OAUTH2_TOKEN_SERVER; // Can be used in test files
    public static final Session CQL_SESSION;
    public static final int NATS_LOCALSTACK_PORT;
    public static final String NATS_URL;

    /**
     * Classpath prefix for compose assets bundled in icms-core-tests.jar (see icms-core {@code
     * maven-resources-plugin} {@code copy-integration-local-env}).
     */
    private static final String LOCAL_ENV_CLASSPATH_PREFIX = "local_env";

    private static final String COMPOSE_FILE_NAME = "docker-compose.test.yml";

    /**
     * Pinned image bundling the Docker Compose CLI, used to run Compose in a container when a local
     * Compose CLI is unavailable (e.g. the CI Maven image can reach the Docker daemon but has no
     * {@code docker compose} binary).
     */
    private static final String DOCKER_COMPOSE_IMAGE = "docker:24.0.2";

    /**
     * Relative to working directory (legacy / IDE): {@code local_env/docker-compose.test.yml}.
     */
    private static final String FS_FALLBACK_COMPOSE = LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME;

    /**
     * If {@code false}, failure to create an extract directory under {@code target/} is a hard
     * error (set in CI via {@code -D...=false} or env). Default {@code true} keeps a
     * java.io.tmpdir fallback for constrained local environments.
     */
    private static final String ALLOW_TMPDIR_FALLBACK_PROPERTY =
            "icms.integration.extract.allowTmpdirFallback";

    private static final String ALLOW_TMPDIR_FALLBACK_ENV = "ICMS_INTEGRATION_ALLOW_TMPDIR_FALLBACK";

    private static final List<Path> INTEGRATION_EXTRACT_ROOTS = new CopyOnWriteArrayList<>();

    private static final AtomicBoolean INTEGRATION_EXTRACT_SHUTDOWN_HOOK = new AtomicBoolean();

    private static final String KEY_SPACE = "test";

    private static final String CASSANDRA_SERVICE_NAME = "cassandra-1";
    private static final int CASSANDRA_PORT = 9042;

    private static final String AWS_LOCALSTACK_SERVICE_NAME = "aws-1";
    private static final int AWS_LOCALSTACK_PORT = 4566;

    private static final String NATS_SERVICE_NAME = "nats-1";
    private static final int NATS_PORT = 4222;

    private static final String OAUTH2_TOKEN_ISSUER = "http://localhost:8082";
    private static final String OAUTH2_KEYSET_URL = "http://localhost:8082/.well-known/jwks.json";

    private static ContainerDetails AWS_LOCALSTACK_CONTAINER_DETAILS;
    private static ContainerDetails CASSANDRA_CONTAINER_DETAILS;
    private static ContainerDetails NATS_CONTAINER_DETAILS;

    static {
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

        var composeContainer =
                createComposeContainer(composeFile)
                        .withExposedService(
                                CASSANDRA_SERVICE_NAME,
                                CASSANDRA_PORT,
                                cassandraWaitStrategy
                        )
                        .withExposedService(
                                AWS_LOCALSTACK_SERVICE_NAME,
                                AWS_LOCALSTACK_PORT
                        )
                        .withExposedService(
                                NATS_SERVICE_NAME,
                                NATS_PORT
                        )
                        .withBuild(false);
        composeContainer.start();

        AWS_LOCALSTACK_CONTAINER_DETAILS = getContainerDetails(composeContainer,
                                                               AWS_LOCALSTACK_SERVICE_NAME,
                                                               AWS_LOCALSTACK_PORT);
        AWS_LOCALSTACK_CONTAINER = AWS_LOCALSTACK_CONTAINER_DETAILS.container();

        NATS_CONTAINER_DETAILS = getContainerDetails(composeContainer,
                                                     NATS_SERVICE_NAME,
                                                     NATS_PORT);
        NATS_LOCALSTACK_CONTAINER = NATS_CONTAINER_DETAILS.container();
        NATS_LOCALSTACK_PORT = NATS_CONTAINER_DETAILS.mappedPort();
        NATS_URL = "nats://%s:%d".formatted(NATS_CONTAINER_DETAILS.host(),
                                            NATS_CONTAINER_DETAILS.mappedPort());

        CASSANDRA_CONTAINER_DETAILS = getContainerDetails(composeContainer,
                                                          CASSANDRA_SERVICE_NAME,
                                                          CASSANDRA_PORT);
        CASSANDRA_CONTAINER = CASSANDRA_CONTAINER_DETAILS.container();
        var cluster = Cluster.builder()
                .addContactPoint(CASSANDRA_CONTAINER_DETAILS.host())
                .withPort(CASSANDRA_CONTAINER_DETAILS.mappedPort())
                .withCredentials("cassandra", "cassandra")
                .withoutJMXReporting()
                .build();
        CQL_SESSION = cluster.connect();

        MOCK_OAUTH2_TOKEN_SERVER = new MockOAuth2TokenServer(
                new OAuth2TokenServerConfigurationProperties(OAUTH2_TOKEN_ISSUER, OAUTH2_KEYSET_URL,
                                                             null, null, null, null));
        // Serve the static JWKS matching JwtKeyUtils' signing key so the resource server can verify
        // bearer tokens minted by the controller tests. The default mock stub publishes a random key,
        // which would reject those tokens (HTTP 401). The /token endpoint keeps the default transformer
        // for the OAuth2 client providers (ngc/fnds/api-keys).
        MOCK_OAUTH2_TOKEN_SERVER.start(buildOAuth2Stubs());
    }

    /**
     * Directory containing {@link #COMPOSE_FILE_NAME} and the {@code aws/}, {@code cassandra/},
     * {@code nats/} subtrees required by that compose file (volume mounts use paths relative to the
     * compose file).
     */
    private static File resolveComposeAssetDirectory() {
        ClassLoader cl = IntegrationTest.class.getClassLoader();
        URL composeUrl = cl.getResource(LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME);
        if (composeUrl == null) {
            File fallbackCompose = new File(FS_FALLBACK_COMPOSE);
            if (fallbackCompose.isFile()) {
                Path dir = fallbackCompose.getParentFile().toPath();
                log.info("Using compose bundle from filesystem: {}", dir.toAbsolutePath());
                try {
                    return materializeLocalEnvDirectory(dir);
                } catch (IOException e) {
                    throw new IllegalStateException("Failed to materialize compose bundle from " + dir, e);
                }
            }
            throw new IllegalStateException(
                    "Missing classpath resource " + LOCAL_ENV_CLASSPATH_PREFIX + "/" + COMPOSE_FILE_NAME
                            + " (ensure icms-core-tests.jar is on the test classpath and built with "
                            + "copy-integration-local-env), and no " + FS_FALLBACK_COMPOSE
                            + " in the working directory.");
        }

        try {
            if ("file".equalsIgnoreCase(composeUrl.getProtocol())) {
                Path composePath = Path.of(composeUrl.toURI()).toRealPath();
                Path dir = composePath.getParent();
                log.info("Using compose bundle from filesystem: {}", dir.toAbsolutePath());
                return materializeLocalEnvDirectory(dir);
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

    private static File materializeLocalEnvDirectory(Path sourceRoot) throws IOException {
        var extractRoot = createIntegrationExtractDirectory();
        try (Stream<Path> paths = Files.walk(sourceRoot)) {
            for (var source : paths.toList()) {
                var relative = sourceRoot.relativize(source);
                var dest = extractRoot.resolve(relative);
                if (Files.isDirectory(source)) {
                    Files.createDirectories(dest);
                    continue;
                }

                Files.createDirectories(dest.getParent());
                Files.copy(source, dest, StandardCopyOption.REPLACE_EXISTING);
                if (Files.isExecutable(source) || source.getFileName().toString().endsWith(".sh")) {
                    setExecutable(dest);
                }
            }
        }

        log.info("Materialized integration compose bundle from {} to {}",
                 sourceRoot.toAbsolutePath(), extractRoot.toAbsolutePath());
        return extractRoot.toFile();
    }

    /**
     * Extract {@code local_env/**} from the tests JAR so Docker can bind-mount files by host path.
     * Uses {@code target/icms-integration-local-env-*} under the module directory (same class of path
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

                var dest = extractRoot.resolve(name.substring(prefix.length()));
                Files.createDirectories(dest.getParent());
                try (var in = jarFile.getInputStream(entry)) {
                    Files.copy(in, dest, StandardCopyOption.REPLACE_EXISTING);
                }
                if (name.endsWith(".sh")) {
                    setExecutable(dest);
                }
            }

            log.info("Extracted integration compose bundle from {} to {}",
                     jarFile.getName(), extractRoot.toAbsolutePath());
            return extractRoot.toFile();
        }
    }

    private static void setExecutable(Path path) throws IOException {
        try {
            Files.setPosixFilePermissions(path, PosixFilePermissions.fromString("rwxr-xr-x"));
        } catch (UnsupportedOperationException ignored) {
            // Non-POSIX filesystems (e.g. Windows): Docker Desktop still runs via bash/sh.
        } catch (IOException e) {
            throw new IllegalStateException("Could not chmod shell script " + path, e);
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
            var dir = Files.createTempDirectory(target, "icms-integration-local-env-");
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
            var fallback = Files.createTempDirectory("icms-integration-local-env-");
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
                    new Thread(IntegrationTest::deleteIntegrationExtractRoots,
                               "icms-integration-extract-cleanup"));
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

    /**
     * Builds the Compose container so tests run consistently locally and in CI. Testcontainers 2.x
     * uses the host's Docker Compose CLI for the {@code File}-only constructor. The CI Maven image
     * can reach the Docker daemon but does not ship the Docker Compose CLI, so use the local CLI
     * when available and otherwise run Compose from a pinned image ({@link #DOCKER_COMPOSE_IMAGE}).
     */
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
            String output = new String(process.getInputStream().readAllBytes(), StandardCharsets.UTF_8).trim();
            if (!completed) {
                process.destroyForcibly();
                log.warn("Timed out while checking local Docker Compose; PATH={}", System.getenv("PATH"));
                return false;
            }
            if (process.exitValue() == 0) {
                log.info("Using local Docker Compose: {}", output);
                return true;
            }
            log.warn("Local Docker Compose probe failed with exit code {}: {}; PATH={}",
                     process.exitValue(), output, System.getenv("PATH"));
            return false;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.warn("Interrupted while checking local Docker Compose", e);
            return false;
        } catch (IOException e) {
            log.warn("Local Docker Compose is unavailable; PATH={}", System.getenv("PATH"), e);
            return false;
        }
    }

    public static class Initializer implements
            ApplicationContextInitializer<ConfigurableApplicationContext> {

        @Override
        public void initialize(@Nonnull ConfigurableApplicationContext applicationContext) {
            var awsEndpoint = "http://" + AWS_LOCALSTACK_CONTAINER_DETAILS.host()
                    + ":" + AWS_LOCALSTACK_CONTAINER_DETAILS.mappedPort();

            TestPropertyValues.of(
                    "icms.aws.endpoint=" + awsEndpoint,
                    "icms.aws.sqs.endpoint=" + awsEndpoint +
                            "/000000000000/gdn-spot-instance-requests-global.fifo",
                    "icms.nats.nats-url=nats://%s:%d".formatted(NATS_CONTAINER_DETAILS.host(),
                                                           NATS_CONTAINER_DETAILS.mappedPort()),
                    "spring.cassandra.contact-points=" + CASSANDRA_CONTAINER_DETAILS.host(),
                    "spring.cassandra.port=" + CASSANDRA_CONTAINER_DETAILS.mappedPort(),
                    "spring.cassandra.local-datacenter=datacenter1",
                    "spring.cassandra.keyspace-name=" + KEY_SPACE,
                    "spring.cassandra.ssl.enabled=false",
                    "spring.security.oauth2.resourceserver.jwt.issuer-uri=" + OAUTH2_TOKEN_ISSUER,
                    "spring.security.oauth2.resourceserver.jwt.jwk-set-uri=" + OAUTH2_KEYSET_URL,
                    "spring.security.oauth2.resourceserver.jwt.jws-algorithms=ES256",
                    "spring.security.oauth2.client.provider.ngc.token-uri="
                                + OAUTH2_TOKEN_ISSUER + "/token",
                    "spring.security.oauth2.client.provider.fnds.token-uri="
                                + OAUTH2_TOKEN_ISSUER + "/token",
                    "spring.security.oauth2.client.provider.api-keys.token-uri="
                                + OAUTH2_TOKEN_ISSUER + "/token"
                    ).applyTo(applicationContext);
        }
    }

    /**
     * Stubs for the mock OAuth2 server: a JWKS endpoint serving {@link JwtKeyUtils}' public key (so
     * tokens minted by the controller tests verify) and a {@code /token} endpoint backed by the
     * default {@link TokenEndpointResponseTransformer} for the OAuth2 client providers.
     */
    private static List<MappingBuilder> buildOAuth2Stubs() {
        var jwks = get(urlPathEqualTo("/.well-known/jwks.json"))
                .willReturn(aResponse()
                                    .withStatus(200)
                                    .withHeader("Content-Type", "application/json")
                                    .withBody(JwtKeyUtils.getPublicJwksJson()));
        var token = post(urlPathMatching("/token"))
                .willReturn(aResponse()
                                    .withStatus(200)
                                    .withHeader("Content-Type", "application/json")
                                    .withTransformers(TokenEndpointResponseTransformer.NAME));
        return List.of(jwks, token);
    }

    public record ContainerDetails(ContainerState container, String host, int mappedPort) {}

    private static ContainerDetails getContainerDetails(
            ComposeContainer composeContainer,
            String serviceName,
            int servicePort) {
        var host = composeContainer.getServiceHost(serviceName, servicePort);
        var mappedPort = composeContainer.getServicePort(serviceName, servicePort);
        var container = composeContainer.getContainerByServiceName(serviceName)
                .orElseThrow(() -> new IllegalStateException("Missing container " + serviceName));
        log.info(container.getLogs());
        return new ContainerDetails(container, host, mappedPort);
    }
}
