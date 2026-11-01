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

package k8sutil

import (
	"encoding/base64"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

func GetObjectFromEncodedString(artSpecBase64 string, objType reflect.Type, decoder ...runtime.Decoder) (runtime.Object, error) {
	objYaml, err := base64.StdEncoding.DecodeString(artSpecBase64)
	if err != nil {
		return nil, fmt.Errorf("error while decoding Base64 string. err: %s", err)
	}

	// Use custom decoder if provided
	decode := scheme.Codecs.UniversalDeserializer().Decode
	if len(decoder) > 0 && decoder[0] != nil {
		decode = decoder[0].Decode
	}

	obj, _, err := decode(objYaml, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("error while decoding YAML object. err: %s", err)
	}
	o := reflect.TypeOf(obj)
	switch o {
	case objType:
		return obj, nil
	default:
		return nil, fmt.Errorf("expecting %v but received type %v in message", objType, o)
	}
}
