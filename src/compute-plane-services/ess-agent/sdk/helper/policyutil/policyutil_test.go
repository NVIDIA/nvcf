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

package policyutil

import "testing"

func TestSanitizePolicies(t *testing.T) {
	expected := []string{"foo", "bar"}
	actual := SanitizePolicies([]string{"foo", "bar"}, false)
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}

	// If 'default' is already added, do not remove it.
	expected = []string{"foo", "bar", "default"}
	actual = SanitizePolicies([]string{"foo", "bar", "default"}, false)
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}
}

func TestParsePolicies(t *testing.T) {
	expected := []string{"foo", "bar", "default"}
	actual := ParsePolicies("foo,bar")
	// add default if not present.
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}

	// do not add default more than once.
	actual = ParsePolicies("foo,bar,default")
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}

	// handle spaces and tabs.
	actual = ParsePolicies(" foo ,	bar	,   default")
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}

	// ignore all others if root is present.
	expected = []string{"root"}
	actual = ParsePolicies("foo,bar,root")
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}

	// with spaces and tabs.
	expected = []string{"root"}
	actual = ParsePolicies("foo ,bar, root		")
	if !EquivalentPolicies(expected, actual) {
		t.Fatalf("bad: expected:%s\ngot:%s\n", expected, actual)
	}
}

func TestEquivalentPolicies(t *testing.T) {
	a := []string{"foo", "bar"}
	var b []string
	if EquivalentPolicies(a, b) {
		t.Fatal("bad")
	}

	b = []string{"foo"}
	if EquivalentPolicies(a, b) {
		t.Fatal("bad")
	}

	b = []string{"bar", "foo"}
	if !EquivalentPolicies(a, b) {
		t.Fatal("bad")
	}

	b = []string{"foo", "default", "bar"}
	if !EquivalentPolicies(a, b) {
		t.Fatal("bad")
	}
}
