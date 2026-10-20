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

package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var jsonFieldNamesCache sync.Map

// UnmodeledMembers returns the members of the JSON object in data that the
// struct type of model does not declare. Names are compared case-insensitively,
// matching how encoding/json binds object keys to struct fields. It returns
// nil when data is not a JSON object or every member is declared.
func UnmodeledMembers(data []byte, model any) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &members); err != nil {
		return nil
	}

	known := jsonFieldNames(reflect.TypeOf(model))
	var unmodeled map[string]json.RawMessage
	for key, value := range members {
		if containsFold(known, key) {
			continue
		}
		if unmodeled == nil {
			unmodeled = make(map[string]json.RawMessage)
		}
		unmodeled[key] = value
	}
	return unmodeled
}

// MergeJSONMembers adds extra members to the JSON object in encoded. Members
// already present in encoded win, so typed fields cannot be overridden.
func MergeJSONMembers(encoded []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return encoded, nil
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		return nil, err
	}
	existing := make([]string, 0, len(members))
	for key := range members {
		existing = append(existing, key)
	}
	for key, value := range extra {
		if !containsFold(existing, key) {
			members[key] = value
		}
	}
	return json.Marshal(members)
}

// MemberDecoder marks a struct whose UnmarshalJSON decodes its declared
// members by name like encoding/json, so AmbiguousMemberPath still inspects them.
type MemberDecoder interface {
	DecodesMembers()
}

// DecodeMembers decodes the JSON object in data into the struct that target
// points to, one declared member at a time. A member that fails to decode
// keeps its zero value and is returned in failed, keyed by its spelling in
// data. If the member's JSON name is listed in checked, DecodeMembers returns
// its error instead. Data that is not a JSON object or null is an error.
func DecodeMembers(data []byte, target any, checked ...string) (failed map[string]json.RawMessage, err error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}

	value := reflect.ValueOf(target).Elem()
	for i := range value.NumField() {
		name := jsonFieldName(value.Type().Field(i))
		if name == "" {
			continue
		}
		key, ok := memberKey(members, name)
		if !ok {
			continue
		}
		field := value.Field(i)
		if err := json.Unmarshal(members[key], field.Addr().Interface()); err != nil {
			field.SetZero()
			if slices.Contains(checked, name) {
				var typeErr *json.UnmarshalTypeError
				if errors.As(err, &typeErr) {
					typeErr.Field = strings.TrimSuffix(name+"."+typeErr.Field, ".")
				}
				// Not wrapped: echo builds its 400 message from *json.UnmarshalTypeError by type assertion.
				return nil, err
			}
			if failed == nil {
				failed = make(map[string]json.RawMessage)
			}
			failed[key] = members[key]
		}
	}
	return failed, nil
}

// memberKey matches like encoding/json: an exact name first, then any letter case.
func memberKey(members map[string]json.RawMessage, name string) (string, bool) {
	if _, ok := members[name]; ok {
		return name, true
	}
	for key := range members {
		if strings.EqualFold(key, name) {
			return key, true
		}
	}
	return "", false
}

var (
	jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	memberDecoderType   = reflect.TypeOf((*MemberDecoder)(nil)).Elem()
	// Types with custom decoding, mapped to the struct they decode objects into.
	customObjectTypes = map[reflect.Type]reflect.Type{
		reflect.TypeOf(ChatMessageContent{}):                reflect.TypeOf([]outboundContentPart{}),
		reflect.TypeOf(ChatCompletionToolChoiceField{}):     reflect.TypeOf(ChatToolChoice{}),
		reflect.TypeOf(ChatCompletionFunctionChoiceField{}): reflect.TypeOf(ChatFunctionChoice{}),
	}
)

// AmbiguousMemberPath returns the path of the first member in data that
// spells a field declared by model's struct type with more than one letter
// case, for example "messages" and "MESSAGES". encoding/json binds the last
// such member while a case-sensitive backend reads the exact name, so the
// gateway and the backend would act on different values. Objects decoded by a
// custom unmarshaler are not inspected. It returns "" when nothing is
// ambiguous.
func AmbiguousMemberPath(data []byte, model any) string {
	return ambiguousMemberPath(data, reflect.TypeOf(model), "")
}

func ambiguousMemberPath(data []byte, t reflect.Type, path string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if mapped, ok := customObjectTypes[t]; ok {
		t = mapped
	} else if pt := reflect.PointerTo(t); pt.Implements(jsonUnmarshalerType) && !pt.Implements(memberDecoderType) {
		return ""
	}

	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return ""
		}
		for i, item := range items {
			if found := ambiguousMemberPath(item, t.Elem(), path+"["+strconv.Itoa(i)+"]"); found != "" {
				return found
			}
		}
	case reflect.Struct:
		var members map[string]json.RawMessage
		if err := json.Unmarshal(data, &members); err != nil {
			return ""
		}
		for i := range t.NumField() {
			field := t.Field(i)
			name := jsonFieldName(field)
			if name == "" {
				continue
			}
			var matched []json.RawMessage
			for key, value := range members {
				if strings.EqualFold(key, name) {
					matched = append(matched, value)
				}
			}
			memberPath := name
			if path != "" {
				memberPath = path + "." + name
			}
			if len(matched) > 1 {
				return memberPath
			}
			if len(matched) == 1 {
				if found := ambiguousMemberPath(matched[0], field.Type, memberPath); found != "" {
					return found
				}
			}
		}
	}
	return ""
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" || !field.IsExported() || field.Anonymous {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return field.Name
	}
	return name
}

func jsonFieldNames(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if cached, ok := jsonFieldNamesCache.Load(t); ok {
		return cached.([]string)
	}

	var names []string
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			names = append(names, jsonFieldNames(field.Type)...)
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}

	jsonFieldNamesCache.Store(t, names)
	return names
}

func containsFold(names []string, key string) bool {
	for _, name := range names {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}
