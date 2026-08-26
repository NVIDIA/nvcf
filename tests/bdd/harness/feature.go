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

package harness

import (
	"io"
	"os"
	"testing"

	"github.com/cucumber/godog"
)

// FeatureRunOptions carries Godog process mechanics shared by install and
// smoke entry points. Step registration remains owned by the caller.
type FeatureRunOptions struct {
	Name   string
	Path   string
	Tags   string
	Format string
	Output io.Writer
}

// RunFeature executes exactly one feature with strict, serial, fail-fast
// semantics. It centralizes Godog mechanics without selecting features or
// registering the repository's step vocabulary.
func RunFeature(t *testing.T, options FeatureRunOptions, initializer func(*godog.ScenarioContext)) {
	t.Helper()
	format := options.Format
	if format == "" {
		format = "pretty"
	}
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	status := godog.TestSuite{
		Name:                options.Name,
		ScenarioInitializer: initializer,
		Options: &godog.Options{
			Format:        format,
			Output:        output,
			Paths:         []string{options.Path},
			Tags:          options.Tags,
			Strict:        true,
			StopOnFailure: true,
			Concurrency:   1,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("%s status = %d", options.Name, status)
	}
}
