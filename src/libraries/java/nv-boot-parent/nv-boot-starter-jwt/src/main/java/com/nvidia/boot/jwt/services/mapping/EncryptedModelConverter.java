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

import static com.google.common.collect.ImmutableBiMap.toImmutableBiMap;
import static com.nvidia.boot.jwt.services.mapping.ClassPathUtils.findClassesByAnnotation;
import static java.util.stream.Collectors.toUnmodifiableMap;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.BeanDescription;
import tools.jackson.databind.JavaType;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.cfg.MapperConfig;
import tools.jackson.databind.introspect.AnnotatedField;
import tools.jackson.databind.introspect.AnnotatedMember;
import tools.jackson.databind.introspect.BeanPropertyDefinition;
import tools.jackson.databind.introspect.JacksonAnnotationIntrospector;
import com.google.common.annotations.Beta;
import com.google.common.collect.BiMap;
import com.nvidia.boot.jwt.exceptions.ModelConversionException;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import com.nvidia.boot.jwt.services.mapping.annotation.ValueObject;
import java.util.Map;
import java.util.Map.Entry;
import java.util.Set;
import java.util.function.Function;
import java.util.function.Predicate;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

/**
 * this module is in BETA. breaking changes may happen.
 */
@Beta
@Slf4j
public class EncryptedModelConverter<Model, Vo> {

    private final BiMap<Class<?>, Class<?>> modelToVoClass;
    private final Map<Class<?>, BeanPropertyDefinition> encryptedFieldCache;
    private final Map<Entry<Class<?>, Class<?>>, Map<BeanPropertyDefinition, BeanPropertyDefinition>> mapModelPropsToVoPropsCache;
    private final Map<Entry<Class<?>, Class<?>>, JsonMapper> encryptedFieldsMapperCache;
    private final JwtService jwtService;
    private final JsonMapper jsonMapper;

    public EncryptedModelConverter(
            JwtService jwtService,
            JsonMapper genericEncryptedJsonMapper,
            String basePackage) {
        this.jwtService = jwtService;
        this.jsonMapper = genericEncryptedJsonMapper;
        // even though the class is generic, spring instantiates one copy of the class for all
        // generic type autowiring. this means we need to handle all Model Vo combos in one
        // instance.
        // prepopulate object mappings for perf and to fail-fast
        this.modelToVoClass = findClassesByAnnotation(basePackage, ValueObject.class)
                .stream()
                .collect(toImmutableBiMap(vo -> vo.getAnnotation(ValueObject.class).model(),
                                          Function.identity()));
        if (modelToVoClass.isEmpty()) {
            throw new ModelConversionException(
                    "could not find any model mappings! "
                            + "this is usually an error. "
                            + "if it is not an error, "
                            + "disable the EncryptedModelConverter service.");
        }
        this.encryptedFieldCache = modelToVoClass.keySet().stream()
                .collect(toUnmodifiableMap(Function.identity(), this::getEncryptedFieldRaw));
        this.mapModelPropsToVoPropsCache = modelToVoClass.entrySet().stream()
                .collect(toUnmodifiableMap(Function.identity(),
                                           entry -> mapModelPropsToVoPropsRaw(entry.getKey(),
                                                                              entry.getValue())));
        this.encryptedFieldsMapperCache = modelToVoClass.entrySet().stream()
                .collect(toUnmodifiableMap(Function.identity(),
                                           entry -> getEncryptedFieldsMapperRaw(entry.getKey(),
                                                                                entry.getValue())));
    }

    public Vo modelToVo(Model model) {
        var encryptedField = getEncryptedField(model.getClass());
        Class<?> valueObjectClass = encryptedField.getField()
                .getAnnotation(EncryptedFields.class)
                .valueObject();

        // decrypt JWE
        String data = (String) encryptedField.getAccessor().getValue(model);
        String decryptedData = jwtService.decrypt(data);

        // map values from encrypted field onto new Value Object
        Vo valueObject;
        try {
            //noinspection unchecked
            valueObject = (Vo) jsonMapper.readValue(decryptedData, valueObjectClass);
        } catch (JacksonException e) {
            throw new ModelConversionException("cannot map decrypted fields onto Vo", e);
        }

        // copy non encrypted values from Model onto ValueObject
        var modelPropsToValueProps = mapModelPropsToVoProps(model.getClass(), valueObjectClass);
        modelPropsToValueProps.forEach((modelProp, voProp) -> {
            Object modelPropValue = modelProp.getAccessor().getValue(model);
            voProp.getNonConstructorMutator().setValue(valueObject, modelPropValue);
        });
        return valueObject;
    }

    public Model voToModel(Vo valueObject) {
        Class<?> valueType = valueObject.getClass();
        Class<?> modelType = getModelTypeFromValueType(valueType);

        String encryptedData = encryptVoFields(valueObject, modelType);

        // add all other fields to model
        //noinspection unchecked
        Model model = (Model) jsonMapper.convertValue(valueObject, modelType);
        // add encrypted data to model
        var encryptedField = getEncryptedField(modelType);
        encryptedField.getNonConstructorMutator().setValue(model, encryptedData);
        return model;
    }

    private Class<?> getModelTypeFromValueType(Class<?> valueType) {
        var ret = modelToVoClass.inverse().get(valueType);
        if (ret == null) {
            throw new ModelConversionException(
                    "unable to find model mapping for Vo %s".formatted(valueType.getSimpleName()));
        }
        return ret;
    }

    private String encryptVoFields(Vo valueObject, Class<?> modelType) {
        var encryptedFieldsMapper = getEncryptedFieldsMapper(modelType, valueObject.getClass());
        String dataToEncrypt;
        try {
            dataToEncrypt = encryptedFieldsMapper.writeValueAsString(valueObject);
        } catch (JacksonException e) {
            throw new ModelConversionException("failed to serialise encrypted Vo fields", e);
        }

        // encrypt JWE
        var encryptedField = getEncryptedField(modelType);
        String keySetName = encryptedField.getField()
                .getAnnotation(EncryptedFields.class)
                .encryptionKeyName();
        return jwtService.encryptWithKeysetName(keySetName, dataToEncrypt);
    }

    private JsonMapper getEncryptedFieldsMapper(Class<?> modelType, Class<?> valueObjectType) {
        var ret = encryptedFieldsMapperCache.get(Map.entry(modelType, valueObjectType));
        if (ret == null) {
            throw new ModelConversionException(
                    "unable to find encrypted fields mapper for Model %s".formatted(
                            modelType.getSimpleName()));
        }
        return ret;
    }

    /**
     * @return custom mapper that only reads fields meant to be encrypted from the Vo
     */
    private JsonMapper getEncryptedFieldsMapperRaw(Class<?> modelType, Class<?> valueObjectType) {
        var voFieldsToModelFields = mapVoPropsToModelProps(modelType, valueObjectType);
        var voPropNamesToModelProps = voFieldsToModelFields.entrySet().stream()
                .collect(toUnmodifiableMap(prop -> prop.getKey().getField().getName(),
                                           Entry::getValue));

        // build a list of Vo fields that have no mapping from the Vo to the Model
        var voDescription = getBeanDescriptionForSerialization(valueObjectType);
        var voFieldsForEncryption = voDescription.findProperties()
                .stream()
                .map(BeanPropertyDefinition::getName)
                .filter(Predicate.not(voPropNamesToModelProps::containsKey))
                .collect(Collectors.toUnmodifiableSet());

        // we will ignore the fields that have a mapping because we assume that all fields
        // without a mapping should be included in the encrypted model field.
        // this mapping also respects jackson annotations, such as @JsonIgnore
        return jsonMapper.rebuild()
                .annotationIntrospector(new OnlyMapEncryptedProperties(modelToVoClass.values(),
                                                                          voFieldsForEncryption))
                .build();
    }

    private Map<BeanPropertyDefinition, BeanPropertyDefinition> mapModelPropsToVoProps(
            Class<?> modelClass, Class<?> valueObjectClass) {
        var ret = mapModelPropsToVoPropsCache.get(Map.entry(modelClass, valueObjectClass));
        if (ret == null) {
            throw new ModelConversionException(
                    "unable to find model props mapping for Model %s".formatted(
                            modelClass.getSimpleName()));
        }
        return ret;
    }

    /**
     * order matters, since it uses deserialisation config for Model serialisation config for Vo
     */
    private Map<BeanPropertyDefinition, BeanPropertyDefinition> mapModelPropsToVoPropsRaw(
            Class<?> modelClass, Class<?> valueObjectClass) {
        var encryptedField = getEncryptedField(modelClass);
        var modelDescription = getBeanDescriptionForDeserialization(modelClass);
        var valueObjectDescription = getBeanDescriptionForSerialization(valueObjectClass);
        var valueObjectPropsByName = valueObjectDescription.findProperties()
                .stream()
                .collect(toUnmodifiableMap(BeanPropertyDefinition::getName, Function.identity()));
        return modelDescription.findProperties()
                .stream()
                .filter(modelProp -> !modelProp.getName().equals(encryptedField.getName()))
                .peek(modelProp -> {
                    if (!valueObjectPropsByName.containsKey(modelProp.getName())) {
                        throw new ModelConversionException(String.format(
                                "could not find field %s while mapping Model %s to Vo %s",
                                modelProp.getName(), modelClass.getSimpleName(),
                                valueObjectClass.getSimpleName()));
                    }
                })
                .collect(toUnmodifiableMap(Function.identity(), modelProp ->
                        valueObjectPropsByName.get(modelProp.getName())));
    }

    /**
     * order matters, since it uses deserialisation config for Vo serialisation config for Model
     */
    private Map<BeanPropertyDefinition, BeanPropertyDefinition> mapVoPropsToModelProps(
            Class<?> modelClass, Class<?> valueObjectClass) {
        var modelDescription = getBeanDescriptionForSerialization(modelClass);
        var valueObjectDescription = getBeanDescriptionForDeserialization(valueObjectClass);
        var modelPropsByName = modelDescription.findProperties()
                .stream()
                .collect(toUnmodifiableMap(BeanPropertyDefinition::getName, Function.identity()));
        return valueObjectDescription.findProperties()
                .stream()
                .filter(voProp -> modelPropsByName.containsKey(voProp.getName()))
                .collect(toUnmodifiableMap(Function.identity(),
                                           voProp -> modelPropsByName.get(voProp.getName())));
    }

    private BeanPropertyDefinition getEncryptedField(Class<?> modelClass) {
        var ret = encryptedFieldCache.get(modelClass);
        if (ret == null) {
            throw new ModelConversionException(
                    "unable to find encrypted field for model %s".formatted(
                            modelClass.getSimpleName()));
        }
        return ret;
    }

    private BeanPropertyDefinition getEncryptedFieldRaw(Class<?> modelClass) {
        var modelDescription = getBeanDescriptionForDeserialization(modelClass);
        var encryptedFields = modelDescription.findProperties().stream()
                .filter(prop -> prop.getField().hasAnnotation(EncryptedFields.class))
                .toList();
        if (encryptedFields.size() != 1) {
            throw new IllegalArgumentException(modelClass.getSimpleName()
                                                       + " must have exactly one EncryptedFields annotated field");
        }
        var encryptedField = encryptedFields.get(0);
        if (!encryptedField.getPrimaryType().isTypeOrSubTypeOf(String.class)) {
            throw new IllegalArgumentException(modelClass.getSimpleName()
                                                       + " EncryptedFields are only valid for a String type");
        }
        return encryptedField;
    }

    private BeanDescription getBeanDescriptionForDeserialization(Class<?> clazz) {
        JavaType modelType = jsonMapper.constructType(clazz);
        var config = jsonMapper.deserializationConfig();
        var introspector = config.classIntrospectorInstance().forOperation(config);
        var annotated = introspector.introspectClassAnnotations(modelType);
        return introspector.introspectForDeserialization(modelType, annotated);
    }

    private BeanDescription getBeanDescriptionForSerialization(Class<?> clazz) {
        JavaType modelType = jsonMapper.constructType(clazz);
        var config = jsonMapper.serializationConfig();
        var introspector = config.classIntrospectorInstance().forOperation(config);
        var annotated = introspector.introspectClassAnnotations(modelType);
        return introspector.introspectForSerialization(modelType, annotated);
    }

    @RequiredArgsConstructor
    private static class OnlyMapEncryptedProperties extends JacksonAnnotationIntrospector {

        private final Set<Class<?>> voClasses;
        private final Set<String> voFieldsForEncryption;

        @Override
        public boolean hasIgnoreMarker(MapperConfig<?> config, AnnotatedMember member) {
            if (member instanceof AnnotatedField field) {
                // we only want to filter Vo fields if the field is actually from a Vo class
                if (voClasses.contains(field.getDeclaringClass())) {
                    // if the Vo field is not marked for encryption,
                    // don't include it for encryption
                    if (!voFieldsForEncryption.contains(field.getName())) {
                        return true;
                    }
                }
            }
            return super.hasIgnoreMarker(config, member);
        }
    }
}
