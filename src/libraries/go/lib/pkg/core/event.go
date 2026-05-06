/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package core

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Event struct {
	Kind          string
	ObjectMetaKey string
	Object        interface{}
}

func (e *Event) String() string {
	objKey := ""
	switch obj := e.Object.(type) {
	case metav1.Object:
		objKey = fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
	case *time.Time:
		objKey = obj.String()
	case *ObjectUpdate:
		newObj := obj.NewObj
		switch o := newObj.(type) {
		case metav1.Object:
			objKey = fmt.Sprintf("%s/%s ObjectUpdate", o.GetNamespace(), o.GetName())
		default:
			objKey = "Unknown ObjectUpdate"
		}
	default:
		objKey = "Unknown"
	}
	return fmt.Sprintf("(%s, %T, %s, %s)", e.Kind, e.Object, objKey, e.ObjectMetaKey)
}

type ObjectUpdate struct {
	NewObj interface{}
	OldObj interface{}
}

func NewUpdateEvent(kind string, oldObj, newObj interface{}) *Event {
	return &Event{
		Object: &ObjectUpdate{
			NewObj: newObj,
			OldObj: oldObj,
		},
		Kind: kind,
	}
}
