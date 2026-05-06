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

package http

import (
	"bytes"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestLeveledLogger(t *testing.T) {
	l := logrus.New()
	buf := &bytes.Buffer{}
	l.Out = buf
	l.SetLevel(logrus.DebugLevel)
	ll := leveledLogger{logger: logrus.NewEntry(l)}

	ll.Debug("foo", "a", "b")
	assert.Contains(t, buf.String(), "level=debug msg=foo a=b")
	buf.Truncate(0)
	ll.Info("foo", "a", "b")
	assert.Contains(t, buf.String(), "level=info msg=foo a=b")
	buf.Truncate(0)
	ll.Warn("foo", "a", "b")
	assert.Contains(t, buf.String(), "level=warning msg=foo a=b")
	buf.Truncate(0)
	ll.Error("foo", "a", "b", "err", "something")
	assert.Contains(t, buf.String(), "level=error msg=foo a=b err=something")
	buf.Truncate(0)
	// Uneven kvs length should not panic
	ll.Error("foo", "a", "b", "c")
	assert.Contains(t, buf.String(), "level=error msg=foo a=b c=")
	buf.Truncate(0)
}
