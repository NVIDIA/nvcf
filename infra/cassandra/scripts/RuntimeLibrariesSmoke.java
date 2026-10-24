// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import ch.qos.logback.classic.LoggerContext;
import ch.qos.logback.core.status.Status;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.datatype.jsr310.JavaTimeModule;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Arrays;
import java.util.Map;
import net.jpountz.lz4.LZ4Factory;
import org.slf4j.LoggerFactory;

public class RuntimeLibrariesSmoke {
    /**
     * Verifies Jackson round trips, LZ4 interoperability, and the selected Logback configuration.
     *
     * @param args unused command-line arguments
     * @throws Exception if a library operation fails
     */
    public static void main(String[] args) throws Exception {
        if (!Files.isRegularFile(Path.of(System.getProperty("logback.configurationFile")))) {
            throw new AssertionError("Missing shipped Logback configuration");
        }
        ObjectMapper mapper = new ObjectMapper().registerModule(new JavaTimeModule());
        Instant now = Instant.parse("2026-01-01T00:00:00Z");
        if (!now.equals(mapper.readValue(mapper.writeValueAsString(now), Instant.class))) {
            throw new AssertionError("Jackson Java time round trip failed");
        }
        Map<?, ?> value = mapper.readValue("{\"value\":42}", Map.class);
        if (!mapper.readTree(mapper.writeValueAsString(value)).get("value").isInt()) {
            throw new AssertionError("Jackson databind round trip failed");
        }

        byte[] input = "Cassandra compression compatibility ".repeat(100).getBytes(StandardCharsets.UTF_8);
        LZ4Factory[] factories = {LZ4Factory.nativeInstance(), LZ4Factory.safeInstance()};
        for (LZ4Factory encoder : factories) {
            byte[] compressed = encoder.fastCompressor().compress(input);
            for (LZ4Factory decoder : factories) {
                byte[] restored = decoder.safeDecompressor().decompress(compressed, input.length);
                if (!Arrays.equals(input, restored)) {
                    throw new AssertionError("LZ4 native/Java interoperability failed");
                }
                ByteBuffer source = ByteBuffer.allocateDirect(input.length);
                source.put(input).flip();
                ByteBuffer encoded = ByteBuffer.allocateDirect(encoder.fastCompressor().maxCompressedLength(input.length));
                encoder.fastCompressor().compress(source, encoded);
                encoded.flip();
                ByteBuffer decoded = ByteBuffer.allocateDirect(input.length);
                decoder.safeDecompressor().decompress(encoded, decoded);
                decoded.flip();
                byte[] result = new byte[decoded.remaining()];
                decoded.get(result);
                if (!Arrays.equals(input, result)) {
                    throw new AssertionError("LZ4 direct buffer interoperability failed");
                }
            }
        }

        LoggerFactory.getLogger(RuntimeLibrariesSmoke.class).info("Runtime library smoke check");
        LoggerContext context = (LoggerContext) LoggerFactory.getILoggerFactory();
        for (Status status : context.getStatusManager().getCopyOfStatusList()) {
            if (status.getEffectiveLevel() >= Status.ERROR) {
                throw new AssertionError("Logback configuration failed: " + status);
            }
        }
        if (!context.getLogger("ROOT").iteratorForAppenders().hasNext()) {
            throw new AssertionError("Logback configuration has no root appenders");
        }
        context.stop();
        System.out.println("Jackson, LZ4 JNI/Java and Logback checks passed");
    }
}
