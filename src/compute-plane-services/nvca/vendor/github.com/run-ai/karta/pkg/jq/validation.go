// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package jq

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/itchyny/gojq"
)

// ValidateJQExpressions recursively validates all the fields of the object tagged with 'jq:"validate"'
func ValidateJQExpressions(object any) []error {
	var errs []error
	validateJQExpressionsRecursive(reflect.ValueOf(object), "", &errs)

	return errs
}

func validateJQExpressionsRecursive(val reflect.Value, fieldPath string, errs *[]error) {
	// Handle nil pointers
	if !val.IsValid() {
		return
	}

	// Dereference pointers
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return
		}
		val = val.Elem()
	}

	switch val.Kind() {
	case reflect.Struct:
		validateJQExpressionsInStruct(val, fieldPath, errs)
	case reflect.Slice, reflect.Array:
		validateJQExpressionsInSlice(val, fieldPath, errs)
	case reflect.Map:
		validateJQExpressionsInMap(val, fieldPath, errs)
	}
}

func validateJQExpressionsInStruct(val reflect.Value, basePath string, errs *[]error) {
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Skip unexported fields
		if !field.CanInterface() {
			continue
		}

		// Build field path for error reporting
		currentPath := buildFieldPath(basePath, fieldType.Name)

		// Check if field has jq:"validate" tag and handle string, *string, []string, and map[K]string fields
		if jqTag, ok := fieldType.Tag.Lookup("jq"); ok && jqTag == "validate" {
			handleTaggedStructField(field, currentPath, errs)
		} else {
			// Recursively validate nested structures
			validateJQExpressionsRecursive(field, currentPath, errs)
		}
	}
}

func handleTaggedStructField(field reflect.Value, currentPath string, errs *[]error) {
	switch {
	case isStringField(field):
		if err := validateStringField(field, currentPath); err != nil {
			*errs = append(*errs, err)
		}
	case isStringSliceField(field):
		for j := 0; j < field.Len(); j++ {
			elem := field.Index(j)
			elemPath := fmt.Sprintf("%s[%d]", currentPath, j)
			if err := validateStringField(elem, elemPath); err != nil {
				*errs = append(*errs, err)
			}
		}
	case isStringMapField(field):
		for _, key := range field.MapKeys() {
			value := field.MapIndex(key)
			valuePath := fmt.Sprintf("%s[%v]", currentPath, key.Interface())
			if err := validateStringField(value, valuePath); err != nil {
				*errs = append(*errs, err)
			}
		}
	default:
		*errs = append(*errs, fmt.Errorf("%s: jq:'validate' tag can only be used on string, *string, []string, and map[K]string fields", currentPath))
	}
}

func isStringField(field reflect.Value) bool {
	return field.Kind() == reflect.String || (field.Kind() == reflect.Ptr && field.Type().Elem().Kind() == reflect.String)
}

func isStringSliceField(field reflect.Value) bool {
	return field.Kind() == reflect.Slice && field.Type().Elem().Kind() == reflect.String
}

func isStringMapField(field reflect.Value) bool {
	return field.Kind() == reflect.Map && field.Type().Elem().Kind() == reflect.String
}

func validateJQExpressionsInSlice(val reflect.Value, basePath string, errs *[]error) {
	for i := 0; i < val.Len(); i++ {
		indexPath := fmt.Sprintf("%s[%d]", basePath, i)
		validateJQExpressionsRecursive(val.Index(i), indexPath, errs)
	}
}

func validateJQExpressionsInMap(val reflect.Value, basePath string, errs *[]error) {
	for _, key := range val.MapKeys() {
		keyPath := fmt.Sprintf("%s[%v]", basePath, key.Interface())
		validateJQExpressionsRecursive(val.MapIndex(key), keyPath, errs)
	}
}

func validateStringField(field reflect.Value, fieldPath string) error {
	// Handle pointer to string (*string)
	if field.Kind() == reflect.Ptr {
		if field.IsNil() {
			return nil // Optional field, skip validation
		}
		field = field.Elem()
	}

	// Ensure field is a string
	if field.Kind() != reflect.String {
		return fmt.Errorf("%s: jq:'validate' tag can only be used on string or *string fields", fieldPath)
	}

	jqExpression := field.String()
	if jqExpression == "" {
		return nil // Empty jqExpression, skip validation
	}

	parsed, err := gojq.Parse(jqExpression)
	if err != nil {
		return fmt.Errorf("failed to parse JQ expression '%s' at '%s': %w", jqExpression, fieldPath, err)
	}

	err = ValidateParsedJQ(parsed)
	if err != nil {
		return fmt.Errorf("JQ expression '%s' at '%s' failed validation: %w", jqExpression, fieldPath, err)
	}

	return nil
}

// ValidateParsedJQ checks if a gojq query is read-only and safe
func ValidateParsedJQ(q *gojq.Query) error {
	if q == nil {
		return nil
	}

	if q.Term != nil {
		if q.Term.Func != nil {
			f := q.Term.Func

			// Reject mutating and recursion-related functions
			switch f.Name {
			case "del":
				return errors.New("del function is not allowed")
			case "range", "paths", "recurse", "walk", "repeat":
				return fmt.Errorf("function '%s' may produce excessive output and is not allowed", f.Name)
			}

			for _, arg := range f.Args {
				err := ValidateParsedJQ(arg)
				if err != nil {
					return err
				}
			}
		}

		if q.Term.Type == gojq.TermTypeRecurse {
			return errors.New("recursive descent operator '..' is not allowed")
		}
	}

	// If the query has an operator, validate it and its operands
	if q.Op > 0 {
		switch q.Op {
		case gojq.OpAssign, gojq.OpModify, gojq.OpUpdateAdd, gojq.OpUpdateSub, gojq.OpUpdateMul, gojq.OpUpdateDiv, gojq.OpUpdateMod, gojq.OpUpdateAlt:
			return fmt.Errorf("modifying operator '%s' is not allowed", q.Op)
		}

		err := ValidateParsedJQ(q.Left)
		if err != nil {
			return err
		}

		err = ValidateParsedJQ(q.Right)
		if err != nil {
			return err
		}

	}

	return nil
}

func buildFieldPath(basePath, fieldName string) string {
	if basePath == "" {
		return fieldName
	}
	return fmt.Sprintf("%s.%s", basePath, fieldName)
}
