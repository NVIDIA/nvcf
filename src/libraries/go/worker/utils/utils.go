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

package utils

import (
	"context"
	"net"
	"reflect"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-viper/mapstructure/v2"
	"github.com/senseyeio/duration"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/semaphore"
)

func AcquireUpToMax(ctx context.Context, currentProcessing *semaphore.Weighted) (uint32, error) {
	// grab anything available to start
	acquired := currentProcessing.AcquireMax()
	if acquired > 0 {
		return uint32(acquired), nil
	}

	// nothing was available, block until something opens up
	err := currentProcessing.Acquire(ctx, 1)
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func GetWriteBufferSize(conn net.Conn) int {
	if sys, ok := conn.(syscall.Conn); ok {
		c, _ := sys.SyscallConn()
		if c != nil {
			value := 0
			_ = c.Control(func(fd uintptr) {
				value, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
			})
			zap.L().Info("write buffer size", zap.String("size", humanize.Bytes(uint64(value))))
			return value
		}
	}
	return 0
}

func ConvertISO8601Duration(durationStr string) (time.Duration, error) {
	// Parse for a valid ISO 8601 duration string (eg: 24H)
	convertedDuration, err := duration.ParseISO8601(durationStr)
	if err != nil {
		return time.Duration(0), err
	}
	// take current time as reference
	refTime := time.Now()
	// convertedDuration.shift(refTime) means refTime + 24H. Then subtract the refTime
	// This will give back 24 hours as output but in 24h0m0s time.Duration format
	shiftedDuration := convertedDuration.Shift(refTime).Sub(refTime)
	return shiftedDuration, nil
}

func StringToDurationHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
		if f.Kind() == reflect.String && t == reflect.TypeOf(time.Duration(0)) {
			durationStr := data.(string)
			// check if the durationStr is a valid ISO 8601 duration string
			if _, err := duration.ParseISO8601(durationStr); err == nil {
				return ConvertISO8601Duration(durationStr)
			}
			// if not, try to parse it as a regular duration string
			return time.ParseDuration(durationStr)
		}
		return data, nil
	}
}

// EffectiveEnvironment returns the environment to use, preferring icmsEnvironment over spotEnvironment.
//
// Deprecated: Configs now normalize in setup (ICMSEnvironment is set from SpotEnvironment when unset).
// Use the config's ICMSEnvironment field after setup instead of calling this.
func EffectiveEnvironment(icmsEnvironment, spotEnvironment string) string {
	if icmsEnvironment != "" {
		return icmsEnvironment
	}
	return spotEnvironment
}
