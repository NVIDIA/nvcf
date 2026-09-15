/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured/unstructuredscheme"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type serializerImpl struct {
	decoder     runtime.Decoder
	jsonEncoder runtime.Encoder
}

func newSerializer() (*serializerImpl, error) {
	// Create scheme for structured serliazling.
	sch := runtime.NewScheme()
	metav1.AddToGroupVersion(sch, schema.GroupVersion{Version: "v1"})
	if err := scheme.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("add to scheme: %v", err)
	}
	// Add unstructured serializers for all media types.
	var cfOpts []serializer.CodecFactoryOptionsMutator
	for _, smt := range unstructuredscheme.NewUnstructuredNegotiatedSerializer().SupportedMediaTypes() {
		cfOpts = append(cfOpts, serializer.WithSerializer(func(_ runtime.ObjectCreater, _ runtime.ObjectTyper) runtime.SerializerInfo {
			return smt
		}))
	}
	codecs := serializer.NewCodecFactory(sch, cfOpts...)

	var jsonEncoder runtime.Encoder
	for _, ser := range codecs.SupportedMediaTypes() {
		if ser.MediaType == runtime.ContentTypeJSON {
			jsonEncoder = ser.StrictSerializer
			break
		}
	}
	if jsonEncoder == nil {
		return nil, fmt.Errorf("code bug: cannot find JSON encoder")
	}

	c := &serializerImpl{
		decoder:     codecs.UniversalDeserializer(),
		jsonEncoder: jsonEncoder,
	}
	return c, nil
}

func (s *serializerImpl) decode(
	logger *zap.Logger,
	in io.Reader,
) (objs []runtime.Object, derrs []error, err error) {
	logger.Info("Decoding objects")

	// Read 1Mb max per object, with some extra for metadata
	const (
		kb      = 1024
		bufSize = kb*kb + kb
	)
	b := make([]byte, bufSize)
	yr := yaml.NewDocumentDecoder(io.NopCloser(in))

	// Set a max number of objects.
	// TODO: use total size limit of 100MB instead of max objects.
	const maxObjects = 300
	var (
		parseErrs []error
	)
	for i := 0; ; i++ {
		n, err := yr.Read(b)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrShortBuffer) {
				logger.Error("Failed to read large object", zap.Error(err), zap.Int("objectIndex", i))
				parseErrs = append(parseErrs, fmt.Errorf("size of object at index %d is larger than maximum %d bytes (including object metadata)", i, bufSize))
				return nil, parseErrs, nil
			}
			return nil, nil, fmt.Errorf("read rendered Helm output: %v", err)
		}
		if n < 1 {
			logger.Info("Empty object", zap.Int("objectIndex", i), zap.Int("readBytes", n))
			continue
		}

		if i > maxObjects {
			parseErrs = append(parseErrs, fmt.Errorf("rendered chart contains more objects than the maximum allowed %d", maxObjects))
			return nil, parseErrs, nil
		}

		objData := b[:n]
		if hasTM, err := hasTypeMeta(objData); err != nil || !hasTM {
			if err != nil {
				logger.Info("Object type metadata check failed, skipping",
					zap.Int("objectIndex", i), zap.Error(err))
			} else {
				logger.Info("Object has no type metadata, skipping", zap.Int("objectIndex", i), zap.Int("readBytes", n))
			}
			continue
		}

		obj, _, err := s.decoder.Decode(objData, nil, nil)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("decode object %d: %v", i, err))
			continue
		}

		mobj, ok := obj.(metav1.Object)
		if !ok {
			parseErrs = append(parseErrs, fmt.Errorf("object %d is not a valid Kubernetes type (does not implement metav1.Object)", i))
			continue
		}

		if mobj.GetName() == "" {
			parseErrs = append(parseErrs, fmt.Errorf("object %d is missing required field metadata.name", i))
			continue
		}

		objs = append(objs, obj)
	}

	logger.Info("Decoded objects", zap.Int("numObjects", len(objs)))

	return objs, parseErrs, nil
}

func checkTypes(
	logger *zap.Logger,
	objs []runtime.Object,
	extraGVKs ...schema.GroupVersionKind,
) error {
	logger.Debug("Checking object types")

	extraGVKSet := make(map[schema.GroupVersionKind]bool, len(extraGVKs))
	for _, gvk := range extraGVKs {
		extraGVKSet[gvk] = true
	}

	var (
		unsupTypes []string
	)
	for _, obj := range objs {
		// Validate allowed types.
		switch t := obj.(type) {
		case *corev1.Pod, *appsv1.Deployment, *appsv1.ReplicaSet, *appsv1.StatefulSet,
			*batchv1.Job, *batchv1.CronJob, *corev1.Service,
			*corev1.ConfigMap, *corev1.Secret, *corev1.PersistentVolumeClaim:
		case *corev1.ServiceAccount, *rbacv1.RoleBinding, *rbacv1.Role:
			// These types are allowed for backwards-compatibility but will be disallowed in the future,
			// so log an error for observability without failing.
			logger.Info("Type allowed for backwards-compatibility that will be disallowed in the future",
				zap.String("type", createTypeString(obj)))
		default:
			if !extraGVKSet[t.GetObjectKind().GroupVersionKind()] {
				unsupTypes = append(unsupTypes, createTypeString(obj))
				continue
			}
		}

		objs = append(objs, obj)
	}

	// Always fail on unsupported types, since downstreams may not have the permissions to apply them.
	if len(unsupTypes) != 0 {
		logger.Info("Unsupported types found", zap.Strings("types", unsupTypes))
		return fmt.Errorf("unsupported types: %q", unsupTypes)
	}

	return nil
}

func (s *serializerImpl) encode(out io.Writer, objs []runtime.Object) error {
	objsRaw := make([]runtime.RawExtension, 0, len(objs))
	for _, obj := range objs {
		data, err := runtime.Encode(s.jsonEncoder, obj)
		if err != nil {
			return fmt.Errorf("encode object: %v", err)
		}
		objsRaw = append(objsRaw, runtime.RawExtension{Raw: data})
	}
	if err := json.NewEncoder(out).Encode(objsRaw); err != nil {
		return fmt.Errorf("encode objects as JSON: %v", err)
	}
	return nil
}
