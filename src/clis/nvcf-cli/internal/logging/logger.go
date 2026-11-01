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

package logging

import (
	"fmt"
	"io"
	"log"
	"os"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

// Logger provides colored console output similar to the NVCF script
type Logger struct {
	colorEnabled bool
}

var jsonOutputEnabled bool
var stdLogOutput io.Writer

// SetJSONOutput configures logging for JSON output mode.
func SetJSONOutput(enabled bool) {
	jsonOutputEnabled = enabled
	if stdLogOutput == nil {
		stdLogOutput = log.Writer()
	}
	if enabled {
		DefaultLogger.colorEnabled = false
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(stdLogOutput)
	}
}

// NewLogger creates a new logger instance
func NewLogger() *Logger {
	return &Logger{
		colorEnabled: supportsColor(),
	}
}

// supportsColor checks if the terminal supports color output
func supportsColor() bool {
	// Check if we're in a terminal and not redirecting output
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

// colorize applies color to text if color is enabled
func (l *Logger) colorize(color, text string) string {
	if !l.colorEnabled {
		return text
	}
	return color + text + ColorReset
}

func outputWriter() io.Writer {
	if jsonOutputEnabled {
		return os.Stderr
	}
	return os.Stdout
}

// Info prints an info message in blue
func (l *Logger) Info(format string, args ...interface{}) {
	if jsonOutputEnabled {
		return
	}
	message := fmt.Sprintf(format, args...)
	prefix := l.colorize(ColorBlue, "[INFO]")
	fmt.Fprintf(outputWriter(), "%s %s\n", prefix, message)
}

// Success prints a success message in green
func (l *Logger) Success(format string, args ...interface{}) {
	if jsonOutputEnabled {
		return
	}
	message := fmt.Sprintf(format, args...)
	prefix := l.colorize(ColorGreen, "[SUCCESS]")
	fmt.Fprintf(outputWriter(), "%s %s\n", prefix, message)
}

// Warning prints a warning message in yellow
func (l *Logger) Warning(format string, args ...interface{}) {
	if jsonOutputEnabled {
		return
	}
	message := fmt.Sprintf(format, args...)
	prefix := l.colorize(ColorYellow, "[WARNING]")
	fmt.Fprintf(outputWriter(), "%s %s\n", prefix, message)
}

// Error prints an error message in red
func (l *Logger) Error(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	prefix := l.colorize(ColorRed, "[ERROR]")
	fmt.Fprintf(os.Stderr, "%s %s\n", prefix, message)
}

// Debug prints a debug message in purple (only when debug is enabled)
func (l *Logger) Debug(format string, args ...interface{}) {
	if jsonOutputEnabled {
		return
	}
	message := fmt.Sprintf(format, args...)
	prefix := l.colorize(ColorPurple, "[DEBUG]")
	fmt.Fprintf(outputWriter(), "%s %s\n", prefix, message)
}

// Plain prints a message without any prefix or color
func (l *Logger) Plain(format string, args ...interface{}) {
	if jsonOutputEnabled {
		return
	}
	message := fmt.Sprintf(format, args...)
	fmt.Fprintln(outputWriter(), message)
}

// PrintJSON prints JSON with syntax highlighting (basic)
func (l *Logger) PrintJSON(jsonStr string) {
	if jsonOutputEnabled {
		fmt.Fprintln(os.Stdout, jsonStr)
		return
	}
	if l.colorEnabled {
		// Basic JSON colorization - could be enhanced further
		fmt.Fprintln(outputWriter(), l.colorize(ColorCyan, jsonStr))
	} else {
		fmt.Fprintln(outputWriter(), jsonStr)
	}
}

// Global logger instance
var DefaultLogger = NewLogger()

// Convenience functions using the default logger
func Info(format string, args ...interface{})    { DefaultLogger.Info(format, args...) }
func Success(format string, args ...interface{}) { DefaultLogger.Success(format, args...) }
func Warning(format string, args ...interface{}) { DefaultLogger.Warning(format, args...) }
func Error(format string, args ...interface{})   { DefaultLogger.Error(format, args...) }
func Debug(format string, args ...interface{})   { DefaultLogger.Debug(format, args...) }
func Plain(format string, args ...interface{})   { DefaultLogger.Plain(format, args...) }
func PrintJSON(jsonStr string)                   { DefaultLogger.PrintJSON(jsonStr) }
