// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package smithy

// Document provides access to loosely structured data in a document-like
// format.
//
// Deprecated: See the github.com/aws/smithy-go/document package.
type Document interface {
	UnmarshalDocument(interface{}) error
	GetValue() (interface{}, error)
}
