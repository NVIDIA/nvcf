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

package mscontroller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	serializerjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sutilyaml "k8s.io/apimachinery/pkg/util/yaml"
	k8syaml "sigs.k8s.io/yaml"
)

type flexibleDecoder struct {
	extraGVKs sets.Set[schema.GroupVersionKind]
	typed     runtime.Decoder
}

func newFlexibleDecoder(scheme *runtime.Scheme, extraGVKs ...schema.GroupVersionKind) runtime.Decoder {
	return &flexibleDecoder{
		extraGVKs: sets.New(extraGVKs...),
		typed:     serializer.NewCodecFactory(scheme).UniversalDeserializer(),
	}
}

func (d *flexibleDecoder) Decode(data []byte, inGVK *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	out, outGVK, err := d.typed.Decode(data, inGVK, into)
	switch {
	case err == nil:
		return out, outGVK, nil
	case runtime.IsNotRegisteredError(err):
		// Input data must be JSON. It always is when from ReVal.
		if !k8sutilyaml.IsJSONBuffer(data) {
			if data, err = k8syaml.YAMLToJSON(data); err != nil {
				return nil, nil, err
			}
		}
		gvk, err := serializerjson.DefaultMetaFactory.Interpret(data)
		if err != nil {
			return nil, nil, err
		}
		// Only extra types are allowed here.
		if !d.extraGVKs.Has(*gvk) {
			return nil, gvk, err
		}
		// into must be Unstructured to inform the decoder.
		if into != nil {
			if _, ok := into.(*unstructured.Unstructured); !ok {
				return nil, gvk, err
			}
			return d.typed.Decode(data, gvk, into)
		}
		u := &unstructured.Unstructured{}
		if _, gvk, err = d.typed.Decode(data, gvk, u); err != nil {
			return nil, gvk, err
		}
		return u, gvk, nil
	}
	return nil, inGVK, err
}
