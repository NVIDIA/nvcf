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

// DEPRECATED: this has been moved to go-secure-stdlib and will be removed
package parseutil

import (
	"time"

	extparseutil "github.com/hashicorp/go-secure-stdlib/parseutil"
	sockaddr "github.com/hashicorp/go-sockaddr"
)

func ParseCapacityString(in interface{}) (uint64, error) {
	return extparseutil.ParseCapacityString(in)
}

func ParseDurationSecond(in interface{}) (time.Duration, error) {
	return extparseutil.ParseDurationSecond(in)
}

func ParseAbsoluteTime(in interface{}) (time.Time, error) {
	return extparseutil.ParseAbsoluteTime(in)
}

func ParseInt(in interface{}) (int64, error) {
	return extparseutil.ParseInt(in)
}

func ParseBool(in interface{}) (bool, error) {
	return extparseutil.ParseBool(in)
}

func ParseString(in interface{}) (string, error) {
	return extparseutil.ParseString(in)
}

func ParseCommaStringSlice(in interface{}) ([]string, error) {
	return extparseutil.ParseCommaStringSlice(in)
}

func ParseAddrs(addrs interface{}) ([]*sockaddr.SockAddrMarshaler, error) {
	return extparseutil.ParseAddrs(addrs)
}
