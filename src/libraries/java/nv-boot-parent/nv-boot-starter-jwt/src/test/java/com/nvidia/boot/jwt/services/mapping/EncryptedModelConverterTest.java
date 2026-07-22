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

package com.nvidia.boot.jwt.services.mapping;

import static org.assertj.core.api.AssertionsForClassTypes.assertThat;

import com.fasterxml.jackson.annotation.JsonIgnore;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.jwt.configuration.EncryptedModelConverterProperties;
import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.configuration.PrivateJwksString;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverterTest.SubObjectFieldVo.SubObject;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverterTest.SubObjectFieldVo.SubObject.EvenSubberObject;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import com.nvidia.boot.jwt.services.mapping.annotation.ValueObject;
import com.nvidia.boot.jwt.services.mapping.model.KeyByOwnerAndServiceModel;
import com.nvidia.boot.jwt.services.mapping.model.KeyModel;
import com.nvidia.boot.jwt.services.mapping.model.ServiceModel;
import com.nvidia.boot.jwt.services.mapping.vo.KeyByOwnerAndServiceVo;
import com.nvidia.boot.jwt.services.mapping.vo.KeyOwnerStatus;
import com.nvidia.boot.jwt.services.mapping.vo.KeyOwnerType;
import com.nvidia.boot.jwt.services.mapping.vo.KeyStatus;
import com.nvidia.boot.jwt.services.mapping.vo.KeyVo;
import com.nvidia.boot.jwt.services.mapping.vo.ServiceVo;
import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Disabled;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Import;
import org.springframework.core.io.ClassPathResource;

@Slf4j
@SpringBootTest(
        classes = EncryptedModelConverterTest.App.class,
        webEnvironment = WebEnvironment.NONE)
public class EncryptedModelConverterTest {

    public static final String TEST_KEY = "test-A256GCM-key";

    @SpringBootApplication
    @Import(App.JwtKeysConfiguration.class)
    static class App {

        @Configuration
        static class JwtKeysConfiguration {

            private static final JweKeysMapping jweKeysMapping = JweKeysMapping
                    .builder()
                    .keysMapping(Map.of(TEST_KEY, "test-A256GCM-key"))
                    .build();

            @Bean
            public PrivateJwksString privateJwks()
                    throws IOException {
                ClassPathResource keyResource = new ClassPathResource("keys/dev_jwks_private.json");
                return new PrivateJwksString(
                        new String(keyResource.getInputStream().readAllBytes()));
            }

            @Bean
            public JweKeysMapping jweKeysMapping() {
                return jweKeysMapping;
            }

            @Bean
            public EncryptedModelConverterProperties encryptedModelConverterProperties() {
                var props = new EncryptedModelConverterProperties();
                props.setBasePackage("com.nvidia.boot");
                return props;
            }
        }
    }

    @Autowired
    private JwtService jwtService;

    @Autowired
    private EncryptedModelConverter<Object, Object> encryptedModelConverter;

    @Autowired
    private EncryptedModelConverter<ServiceModel, ServiceVo> serviceEncryptedModelConverter;

    @Autowired
    private EncryptedModelConverter<KeyModel, KeyVo> keyEncryptedModelConverter;

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    @Test
    void singleConverterWithGenerics() {
        // must be the exact same object
        assert encryptedModelConverter == (Object) keyEncryptedModelConverter;
        assert encryptedModelConverter == (Object) serviceEncryptedModelConverter;
    }

    @Test
    void serviceDao() {
        var vo = ServiceVo.builder()
                .serviceId("service id")
                .sfClientIds(Set.of("client id 1", "client id 2"))
                .maxApiKeysPerUser(2)
                .maxApiKeyTtlDays(3)
                .maxAuthzSizeChars(4)
                .minAuthzUpdateIntervalSeconds(5)
                .maxApiKeysPerAccount(6)
                .build();
        var model = serviceEncryptedModelConverter.voToModel(vo);
        var voTranslated = serviceEncryptedModelConverter.modelToVo(model);
        assertThat(vo).isEqualTo(voTranslated);
    }

    @Test
    @Disabled
    void perf() {
        Instant start = Instant.now();
        for (int i = 0; i < 100000; i++) {
            var vo = ServiceVo.builder()
                    .serviceId("service id")
                    .sfClientIds(Set.of("client id 1", "client id 2"))
                    .maxApiKeysPerUser(2)
                    .maxApiKeyTtlDays(3)
                    .maxAuthzSizeChars(4)
                    .minAuthzUpdateIntervalSeconds(5)
                    .maxApiKeysPerAccount(6)
                    .build();
            var model = serviceEncryptedModelConverter.voToModel(vo);
            var voTranslated = serviceEncryptedModelConverter.modelToVo(model);
            assert voTranslated != null;
        }
        log.info("took {}", Duration.between(start, Instant.now()));
    }

    @Test
    void keyModel() {
        var vo = KeyVo.builder()
                .serviceId("service id")
                .status(KeyStatus.ACTIVE)
                .ownerType(KeyOwnerType.USER)
                .ownerId("owner id")
                .issuerServiceId("issuer service id")
                .audienceServiceId("audience service id")
                .keyId("key id")
                .keyHash("a hash")
                .expiresAt(Instant.now())
                .deletesAt(Instant.now().plusSeconds(1234))
                .apiKeySuffix("a suffix")
                .authorization("some auth")
                .description("a description")
                .build();
        var model = keyEncryptedModelConverter.voToModel(vo);
        var voTranslated = keyEncryptedModelConverter.modelToVo(model);
        assertThat(vo).isEqualTo(voTranslated);
    }

    @Test
    void keyByOwnerAndService() {
        var vo = KeyByOwnerAndServiceVo.builder()
                .ownerType(KeyOwnerType.USER)
                .ownerID("owner id")
                .issuerServiceID("issuer service id")
                .keyId("key id")
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .updatedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(12345))
                .deletesAt(Instant.now().plusSeconds(123456))
                .keyStatus(KeyStatus.ACTIVE)
                .apiKeyHash("brown")
                .apiKeySuffix("a suffix")
                .audienceServiceId("audience service id")
                .description("a description")
                .build();
        var model = (KeyByOwnerAndServiceModel) encryptedModelConverter.voToModel(vo);
        var voTranslated = (KeyByOwnerAndServiceVo) encryptedModelConverter.modelToVo(model);
        assertThat(vo).isEqualTo(voTranslated);
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class StandardModel {

        String field1;

        String field2;
        @EncryptedFields(encryptionKeyName = TEST_KEY, valueObject = VoStandard.class)
        String encryptedField;
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    @ValueObject(model = StandardModel.class)
    public static class VoStandard {

        String field1;
        String field2;
        @JsonIgnore
        String ignoreMe;
        String encryptedField1;
        String encryptedField2;
    }

    @Test
    void ignoreVoField() {
        var vo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .ignoreMe("shouldn't make it into the model")
                .build();
        var model = (StandardModel) encryptedModelConverter.voToModel(vo);
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void addEncryptedFieldToVo()
            throws JacksonException {
        // model doesn't contain encryptedField2
        var oldVoEncryptedFields = Map.of("encryptedField1", "encrypted field value");
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void noFieldsToStart()
            throws JacksonException {
        // model will have no values in the encrypted field, so Vo encrypted fields will be empty
        Map<String, Object> oldVoEncryptedFields = Map.of();
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    @Test
    void removeEncryptedFieldFromVo()
            throws JacksonException {
        // model has an extra field not in the Vo anymore, encryptedField3
        var oldVoEncryptedFields = Map.of("encryptedField1", "encrypted field value",
                                          "encryptedField3", "encrypted field value 3");
        var model = StandardModel.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField(encryptPayload(oldVoEncryptedFields))
                .build();
        var voTranslated = (VoStandard) encryptedModelConverter.modelToVo(model);
        var expectedVo = VoStandard.builder()
                .field1("field1 value")
                .field2("field2 value")
                .encryptedField1("encrypted field value")
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }

    private String encryptPayload(Map<String, ?> voFields)
            throws JacksonException {
        return jwtService.encryptWithKeysetName(TEST_KEY,
                                                JSON_MAPPER.writeValueAsString(voFields));
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class SubObjectFieldModel {

        String field1;
        @EncryptedFields(encryptionKeyName = TEST_KEY, valueObject = SubObjectFieldVo.class)
        String encryptedField;
    }

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    @ValueObject(model = SubObjectFieldModel.class)
    public static class SubObjectFieldVo {

        @Data
        @Builder
        @NoArgsConstructor
        @AllArgsConstructor
        public static class SubObject {

            @Data
            @Builder
            @NoArgsConstructor
            @AllArgsConstructor
            public static class EvenSubberObject {

                String encryptedField3;
                String encryptedField4;
            }

            String encryptedField1;
            String encryptedField2;
            EvenSubberObject encryptedObject;
        }

        String field1;
        SubObject subObject1;
        @JsonIgnore
        SubObject subObject2;
    }

    @Test
    void encryptedSubObject() {
        var vo = SubObjectFieldVo.builder()
                .field1("field 1")
                .subObject1(SubObject.builder()
                                    .encryptedField1("encrypted field 1")
                                    .encryptedField2("encrypted field 2")
                                    .encryptedObject(EvenSubberObject.builder()
                                                             .encryptedField3("encrypted field 3")
                                                             .encryptedField4("encrypted field 4")
                                                             .build())
                                    .build())
                .subObject2(SubObject.builder()
                                    .encryptedField1("encrypted field 1")
                                    .encryptedField2("encrypted field 2")
                                    .encryptedObject(EvenSubberObject.builder()
                                                             .encryptedField3("encrypted field 3")
                                                             .encryptedField4("encrypted field 4")
                                                             .build())
                                    .build())
                .build();
        var model = (SubObjectFieldModel) encryptedModelConverter.voToModel(vo);
        var voTranslated = (SubObjectFieldVo) encryptedModelConverter.modelToVo(model);
        var expectedVo = SubObjectFieldVo.builder()
                .field1("field 1")
                .subObject1(SubObject.builder()
                                    .encryptedField1("encrypted field 1")
                                    .encryptedField2("encrypted field 2")
                                    .encryptedObject(EvenSubberObject.builder()
                                                             .encryptedField3("encrypted field 3")
                                                             .encryptedField4("encrypted field 4")
                                                             .build())
                                    .build())
                .build();
        assertThat(expectedVo).isEqualTo(voTranslated);
    }
}