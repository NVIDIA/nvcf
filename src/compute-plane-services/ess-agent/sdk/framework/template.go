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

package framework

import (
	"bufio"
	"bytes"
	"strings"
	"text/template"

	"github.com/hashicorp/errwrap"
)

func executeTemplate(tpl string, data interface{}) (string, error) {
	// Define the functions
	funcs := map[string]interface{}{
		"indent": funcIndent,
	}

	// Parse the help template
	t, err := template.New("root").Funcs(funcs).Parse(tpl)
	if err != nil {
		return "", errwrap.Wrapf("error parsing template: {{err}}", err)
	}

	// Execute the template and store the output
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", errwrap.Wrapf("error executing template: {{err}}", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

func funcIndent(count int, text string) string {
	var buf bytes.Buffer
	prefix := strings.Repeat(" ", count)
	scan := bufio.NewScanner(strings.NewReader(text))
	for scan.Scan() {
		buf.WriteString(prefix + scan.Text() + "\n")
	}

	return strings.TrimRight(buf.String(), "\n")
}
