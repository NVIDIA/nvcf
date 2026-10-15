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
package com.nvidia.nvcf.configuration;

import com.fasterxml.jackson.annotation.JsonInclude;
import java.nio.ByteBuffer;
import java.util.Base64;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.http.ProblemDetail;
import org.springframework.http.converter.json.ProblemDetailJacksonMixin;
import tools.jackson.core.JsonGenerator;
import tools.jackson.core.JsonParser;
import tools.jackson.core.StreamWriteFeature;
import tools.jackson.databind.DeserializationContext;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.MapperFeature;
import tools.jackson.databind.SerializationContext;
import tools.jackson.databind.ValueDeserializer;
import tools.jackson.databind.cfg.EnumFeature;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.module.SimpleModule;
import tools.jackson.databind.ser.std.StdScalarSerializer;
import tools.jackson.module.blackbird.BlackbirdModule;

@Slf4j
@Configuration
public class JacksonConfiguration {

    @Bean
    @Primary
    public JsonMapper jsonMapper() {
        return JsonMapper.builder()
                .addModule(new BlackbirdModule())
                .addModule(byteBufferModule())
                .addMixIn(ProblemDetail.class, ProblemDetailJacksonMixin.class)
                .changeDefaultPropertyInclusion(v -> v.withValueInclusion(JsonInclude.Include.NON_NULL))
                .enable(StreamWriteFeature.STRICT_DUPLICATE_DETECTION)
                .enable(MapperFeature.ACCEPT_CASE_INSENSITIVE_ENUMS)
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .enable(EnumFeature.READ_UNKNOWN_ENUM_VALUES_USING_DEFAULT_VALUE)
                .build();
    }

    /**
     * Creates a Jackson module for transparent ByteBuffer serialization/deserialization
     * using Base64 encoding. This enables audit logs to be created by serializing ByteBuffer
     * fields to Base64 encoded strings. Also, this enables audit logs to be correctly
     * deserialized back to ByteBuffer objects.
     */
    private SimpleModule byteBufferModule() {
        var module = new SimpleModule("ByteBufferModule");
        module.addSerializer(ByteBuffer.class, new ByteBufferSerializer());
        module.addDeserializer(ByteBuffer.class, new ByteBufferDeserializer());
        return module;
    }

    /**
     * Serializes ByteBuffer to a Base64-encoded string.
     */
    static class ByteBufferSerializer extends StdScalarSerializer<ByteBuffer> {
        ByteBufferSerializer() {
            super(ByteBuffer.class);
        }

        @Override
        public void serialize(
                ByteBuffer buffer,
                JsonGenerator gen,
                SerializationContext serializers) {
            if (buffer == null) {
                gen.writeNull();
                return;
            }
            var duplicate = buffer.duplicate();
            var bytes = new byte[duplicate.remaining()];
            duplicate.get(bytes);
            gen.writeString(Base64.getEncoder().encodeToString(bytes));
        }
    }

    /**
     * Deserializes a Base64-encoded string back to ByteBuffer.
     */
    static class ByteBufferDeserializer extends ValueDeserializer<ByteBuffer> {
        @Override
        public ByteBuffer deserialize(JsonParser p, DeserializationContext ctxt) {
            var base64 = p.getValueAsString();
            if (StringUtils.isBlank(base64)) {
                return null;
            }
            var bytes = Base64.getDecoder().decode(base64);
            return ByteBuffer.wrap(bytes);
        }
    }
}
