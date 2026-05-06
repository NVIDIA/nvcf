# `parsec`

Deterministic JSON Schema compiler for LLM tool interfaces and grammar-based
constrained decoding.

## Overview

parsec transforms arbitrary JSON schemas into deterministic context-free
grammars (CFGs) suitable for streaming LLM token validation. It implements a
multi-pass compiler architecture inspired by GCC and LLVM, featuring:

- **Strongly-typed Intermediate Representation (IR)** for safe schema manipulation
- **24 optimization passes** with fixed-point iteration
- **5 union disambiguation strategies** for unambiguous grammar generation
- **Provenance tracking** for precise error rebasing to original schemas
- **Conservative DoS limits** protecting against adversarial inputs

### Why this exists

LLM tool execution and grammar-based decoding require schemas with predictable,
context-free structure. Vanilla JSON Schema is far more permissive: features
like unconstrained `$ref`, overlapping `patternProperties`, or `dependencies`
introduce ambiguity or unbounded key spaces that prevent streaming validation.

parsec enforces a curated subset and normalizes schemas before compilation,
ensuring every union can be disambiguated at the token level without
backtracking.

## Installation

Requires Go 1.25 or newer (pinned via `mise`).

### CLI

```bash
go install github.com/nvidia-lpu/parsec/cmd/parsec@latest
```

Ensure `$GOBIN` (or `$GOPATH/bin`) is on your `PATH`, then verify with
`parsec -version`.

### Library

```bash
go get github.com/nvidia-lpu/parsec@latest
```

Import `github.com/nvidia-lpu/parsec/jsonschema` for the compiler API.

## Architecture

parsec follows a classic compiler pipeline with six stages:

```
┌─────────────────────────────────────────────────────────────────┐
│  Input: JSON Schema (map[string]any)                            │
└────────────────────────┬────────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [1] Parse → Typed IR (ir.Schema)                              │
│      • Convert JSON to strongly-typed nodes                    │
│      • Preserve property insertion order                       │
│      • Track origin pointers for error rebasing                │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [2] Resolve → Expand local $ref pointers                      │
│      • Memoized resolution with cycle detection                │
│      • Configurable depth limits (default 512)                 │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [3] Normalize → Semantic-preserving rewrites                  │
│      • Propagate allOf constraints                             │
│      • Lower OpenAPI nullable to type unions                   │
│      • Expand discriminator mappings                           │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [4] Lower Conditionals → if/then/else to anyOf                │
│      • Pattern ^value$ → const literal                         │
│      • Numeric range partitioning                              │
│      • Complement synthesis for finite domains                 │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [5] Optimize → 24 IR optimization passes (optional)           │
│      • Constant folding, dead code elimination                 │
│      • Common subexpression elimination                        │
│      • Fixed-point iteration (max 10 rounds)                   │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌────────────────────────────────────────────────────────────────┐
│  [6] Validate → Deterministic CFG compatibility                │
│      • Union disambiguation verification                       │
│      • Pattern disjointness checking                           │
│      • Report generation with decisions                        │
└────────────────────────┬───────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────────┐
│  Output: Normalized Schema + ValidationReport                   │
└─────────────────────────────────────────────────────────────────┘
```

## Quick start

```go
package main

import (
	"fmt"
	"log"

	"github.com/nvidia-lpu/parsec/jsonschema"
)

func main() {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"shape": map[string]any{
				"anyOf": []any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind": map[string]any{"const": "circle"},
							"r":    map[string]any{"type": "number"},
						},
						"required":             []any{"kind", "r"},
						"additionalProperties": false,
					},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind": map[string]any{"const": "square"},
							"w":    map[string]any{"type": "number"},
						},
						"required":             []any{"kind", "w"},
						"additionalProperties": false,
					},
				},
			},
		},
		"required":             []any{"shape"},
		"additionalProperties": false,
	}

	normalized, report, err := jsonschema.PrepareJSONSchema(
		"draw_shape",
		raw,
		jsonschema.DefaultPreprocessOptions,
		jsonschema.DefaultValidateOptions,
	)
	if err != nil {
		if ve, ok := jsonschema.AsValidationError(err); ok {
			fmt.Println("pointer:", ve.JSONPointer())
			fmt.Println("reason :", ve.Reason)
		}
		log.Fatal(err)
	}

	// Inspect union disambiguation decisions
	for _, decision := range report.Decisions {
		fmt.Printf("union at %s uses %s\n", decision.Pointer, decision.Strategy)
	}

	_ = normalized // LLGuidance-ready schema
}
```

`PrepareJSONSchema` rebases all pointers to the original schema, so
`ve.JSONPointer()` and `UnionDecision.Pointer` map directly to the caller's
document.

When parsing author-provided JSON, prefer `jsonschema.ParseOrderedJSON` to
retain property insertion order through the pipeline.

## Command-line interface

### Basic usage

```sh
parsec schema.json              # Validate and print normalized schema
parsec -                        # Read from stdin
cat payload.json | parsec -name my_tool -
```

The CLI auto-detects OpenAI payloads (`response_format.json_schema` and
`tools[].function.parameters`) and extracts schemas automatically.

### All flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-name` | string | `"schema"` | Schema name for error messages |
| `-print-normalized` | bool | `true` | Print the normalized schema |
| `-view` | string | `"json"` | Output format: `json` or `ir` (internal representation) |
| `-optimize` | bool | `false` | Run IR optimizer passes |
| `-lenient` | bool | `false` | Relax validation (match LLGuidance lenient mode) |
| `-strict` | bool | `false` | Fail if union decisions are empty (debug aid) |
| `-verbose` | bool | `false` | Include full pipeline provenance in output |
| `-output` | string | `""` | Write normalized JSON to file |
| `-maskbench` | string | `""` | Run MaskBench corpus analysis on path |
| `-maskbench-timeout` | duration | `30s` | Per-schema timeout for MaskBench (0 to disable) |
| `-cpuprofile` | string | `""` | Write CPU profile to file (pprof) |
| `-memprofile` | string | `""` | Write heap profile to file (pprof) |
| `-version` | bool | `false` | Print version and exit |

### MaskBench corpus analysis

```sh
parsec -maskbench jsonschemabench/maskbench/data
```

Aggregates coverage statistics for the
[MaskBench](https://github.com/guidance-ai/jsonschemabench/tree/main/maskbench)
corpus, showing supported schema counts, test coverage, and common rejection
codes.

## Configuration

### PreprocessOptions

Controls schema normalization before validation.

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `CoerceOneOf` | bool | `false` | Convert `oneOf` to `anyOf` when provably equivalent |
| `LowerNullable` | bool | `true` | Replace OpenAPI `nullable:true` with JSON Schema unions |
| `StripDiscriminator` | bool | `true` | Remove OpenAPI discriminator metadata after rewriting |
| `StripVendorExtensions` | bool | `true` | Drop unknown `x-*` fields |
| `TrackOrigins` | bool | `true` | Annotate nodes with `x-origin-pointer` for rebasing |
| `RunOptimizer` | bool | `false` | Apply IR optimization passes |
| `MaxSchemaArrayLen` | int | `10000` | Maximum elements in schema arrays |
| `MaxReferenceDepth` | int | `512` | Maximum `$ref` resolution depth |

### ValidateOptions

Controls validation strictness and limits.

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `MaxSchemaMapKeys` | int | `10000` | Maximum keys per object schema |
| `MaxSchemaDepth` | int | `512` | Maximum schema nesting depth |
| `MaxUnionVariants` | int | `10000` | Maximum branches per union |
| `RequireClosedObjects` | bool | `true` | Require `additionalProperties: false` |
| `Lenient` | bool | `false` | Relax keyword/format restrictions |
| `EnforceKnownFormats` | bool | `true` | Reject unknown string formats |
| `FormatAllowlist` | map | `nil` | Additional allowed formats beyond built-ins |

Use `jsonschema.DefaultPreprocessOptions` and `jsonschema.DefaultValidateOptions`
for production settings.

## Validation report

`ValidationReport` documents how unions were disambiguated:

```go
type ValidationReport struct {
    // Strategy flags (at least one will be true for valid schemas with unions)
    PrimitiveTypeUnion    bool  // Unions use distinct primitive types (string vs number)
    TokenDisjointUnion    bool  // Unions distinguished by first JSON token
    Discriminator         bool  // Object unions use const/enum discriminator property
    RequiredKeyUniqueness bool  // Object unions have mutually-exclusive required keys
    KeySetExclusion       bool  // Object unions use additionalProperties:false with disjoint keys

    // Per-union decisions for tooling/observability
    Decisions  []UnionDecision

    // Pipeline transformation history for debugging
    Provenance []ProvenanceStep
}

type UnionDecision struct {
    Pointer       string                 // JSON Pointer to union location
    Strategy      string                 // Disambiguation strategy used
    Discriminator *DiscriminatorDecision // Details when strategy is "discriminator"
}

type DiscriminatorDecision struct {
    Property string   // Discriminator property name
    Values   []string // Discriminator values for each branch
}
```

### Union disambiguation strategies

parsec verifies that every union can be resolved unambiguously during streaming:

| Strategy | Description | Example |
|----------|-------------|---------|
| `primitive-type-union` | Branches have distinct JSON types | `["string", "integer", "null"]` |
| `token-disjoint` | First token differs between branches | `{"type":"object"}` vs `[...]` |
| `discriminator` | Shared property with distinct const/enum | `{"status":"pending"}` vs `{"status":"done"}` |
| `required-key-uniqueness` | Mutually exclusive required keys | `{required:["a"]}` vs `{required:["b"]}` |
| `key-set-exclusion` | Closed objects with disjoint key sets | Different property sets with `additionalProperties:false` |

## Error handling

All validation failures return `*ValidationError`:

```go
if err != nil {
    if ve, ok := jsonschema.AsValidationError(err); ok {
        fmt.Println("kind:", ve.Kind)           // Error category
        fmt.Println("code:", ve.Code)           // Machine-readable subcategory
        fmt.Println("path:", ve.Path)           // Human-readable path
        fmt.Println("pointer:", ve.JSONPointer()) // RFC 6901 JSON Pointer
        fmt.Println("reason:", ve.Reason)       // Detailed explanation
        fmt.Println("provenance:", ve.Provenance) // Pipeline transformation history
    }
}
```

### Error kinds

| Kind | Description |
|------|-------------|
| `top_level` | Root schema structure issues |
| `unsupported_keyword` | Forbidden JSON Schema keyword used |
| `type_array` | Type array issues (duplicates, integer/number overlap) |
| `anyOf` | anyOf ambiguity or structural problems |
| `oneOf` | oneOf ambiguity or structural problems |
| `allOf` | allOf conflicts during constraint merging |
| `conditional` | if/then/else structure issues |
| `properties` | Property definition problems |
| `required` | Required array validation failures |
| `additional_properties` | additionalProperties constraint issues |
| `items` | Array items keyword problems |
| `ref` | $ref resolution or usage issues |
| `union` | Union disambiguation failures |
| `unsatisfiable` | Schema provably rejects all values |

### Error codes

Machine-readable codes for programmatic handling:

- `duplicate_primitive_types` — Union has overlapping primitive types
- `integer_number_overlap` — Union mixes integer and number ambiguously
- `enum_empty` — Empty enum array
- `enum_duplicate_value` — Duplicate values in enum
- `pattern_unsupported_feature` — Regex uses unsupported features
- `pattern_invalid_regex` — Invalid regex pattern
- `unsupported_format` — Unknown string format

### Error rebasing

After preprocessing, map errors back to original schema locations:

```go
err = jsonschema.RebaseValidationErrorToOrigin(normalized, err)
```

## Supported keywords

parsec targets the deterministic subset of JSON Schema Draft 2020-12:

### Core
- `type` — Primitive type constraints
- `const`, `enum` — Literal value constraints
- `anyOf`, `oneOf`, `allOf` — Combinators (oneOf requires coercion or disambiguation)
- `$ref` — Local JSON Pointers only (`#/...`)
- `$defs`, `definitions` — Definition anchors (preserved when referenced)
- `if`/`then`/`else` — Conditionals (lowered to unions when deterministic)

### Objects
- `properties`, `required`
- `additionalProperties` (boolean or schema)
- `patternProperties` (patterns must be disjoint)
- `minProperties`, `maxProperties`

### Arrays
- `items`, `prefixItems`, `additionalItems`
- `minItems`, `maxItems`
- Tuple arrays with explicit item schemas

### Strings
- `minLength`, `maxLength`
- `pattern` (no lookarounds or backreferences)
- `format` — Supported: `date-time`, `time`, `date`, `duration`, `email`,
  `hostname`, `ipv4`, `ipv6`, `uuid`

### Numbers
- `minimum`, `maximum`
- `exclusiveMinimum`, `exclusiveMaximum`
- `multipleOf`

### Local $ref handling

- In-document JSON Pointers (`#/...`) only; external URIs rejected
- Bare references (no siblings) preserved for recursive schemas
- References with metadata merged via `allOf`
- Cycles detected and reported as `ErrKindRef`
- Definition holders preserved when any `$ref` remains

## Unsupported keywords

Keywords rejected for breaking deterministic streaming:

| Keyword | Reason |
|---------|--------|
| `not` | Requires lookahead/backtracking |
| `contains` | Unbounded array search |
| `dependencies` | Legacy; use conditional schemas |
| `dependentSchemas` | Multi-pass validation required |
| `dependentRequired` | Multi-pass validation required |
| `unevaluatedProperties` | Requires tracking evaluated paths |
| `unevaluatedItems` | Requires tracking evaluated paths |
| `propertyNames` | Unbounded key validation |
| `uniqueItems` | Requires full array buffering |

Boolean schemas (`true`/`false`) are disallowed. Property schemas must be
objects.

## Conditional lowering

The preprocessor converts discriminator-style conditionals to deterministic
unions:

### Supported guards

Guards must target required properties with deterministic constraints:

- **Literal discriminators**: `const`, `enum`
- **Exact patterns**: `^literal$` (rewritten to `const`)
- **String length**: `minLength`, `maxLength`
- **Array size**: `minItems`, `maxItems`
- **Numeric ranges**: `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`
  (single intervals or finite `anyOf` unions)

### Branch handling

- `then`/`else` composed via `allOf` with guard constraints
- Missing `else` synthesizes complement when domain is finite
- Branches with identical non-guard schemas are merged

### Limitations

Guards that exceed `properties`/`required`, use unsupported regex features, or
have unbounded domains are left intact and rejected by validation.

## Optimizer

When `PreprocessOptions.RunOptimizer` is enabled, parsec runs 24 optimization
passes in fixed-point iteration (max 10 rounds):

| Pass | Effect |
|------|--------|
| `constant_folding` | Simplify constant/enum constraints |
| `inline_small_schemas` | Expand single-use definitions |
| `collapse_ref_aliases` | Remove unnecessary indirections |
| `dead_code_elimination` | Remove unreferenced `$defs` |
| `eliminate_redundant_allOf` | Collapse trivial `allOf` wrappers |
| `merge_object_allof` | Flatten object-only `allOf` chains |
| `intersect_allof_types` | Merge type constraints across `allOf` |
| `partition_enum_unions` | Merge disjoint enum branches |
| `extract_literal_enums` | Lift literal branches into enums |
| `precompute_discriminators` | Extract discriminator hints early |
| `promote_discriminator_properties` | Hoist discriminator properties |
| `align_required_order` | Align `required` arrays with property order |
| `canonicalize_numeric_ranges` | Merge numeric constraints |
| `deduplicate_string_patterns` | Hoist shared string patterns |
| `simplify_property_dependencies` | Simplify property dependencies |
| `normalize_metadata` | Drop stale metadata |
| `refine_array_bounds` | Tighten tuple bounds |
| `simplify_unions` | Compact redundant union branches |
| `simplify_not_literals` | Fold negated literals |
| `simplify_not_constraints` | Simplify negated type constraints |
| `hoist_common_properties` | Extract common union properties |
| `algebraic_simplifications` | Apply algebraic identities |
| `common_subexpression_elimination` | Deduplicate identical subtrees |

### Optimizer behavior notes

- Passes execute until schema fingerprint stabilizes (SHA256 convergence check)
- Object `allOf` chains merge even with conflicting properties (wrapped in nested `allOf`)
- Simple `not` clauses (`{"not":{"type":"null"}}`) fold directly into parent

### Benchmarking

```sh
go test -bench=OptimizeIR -run=^$ ./jsonschema
go test -bench=OptimizeIR -benchtime=5s ./jsonschema  # Longer run
```

## Intermediate Representation

The `jsonschema/ir` package provides strongly-typed schema representation:

```go
import "github.com/nvidia-lpu/parsec/jsonschema/ir"

// Parse JSON to IR
schema, err := ir.FromJSON(jsonValue)

// Traverse with path tracking
ir.Walk(schema, func(path []string, node *ir.Schema) error {
    // path like: ["$", "properties", "user", "anyOf", "0"]
    return nil
})

// Transform nodes
transformed := ir.Transform(schema, ir.FuncPostTransformer{
    Pre: func(s *ir.Schema) (*ir.Schema, bool) {
        return s, true // continue to children
    },
    Post: func(s *ir.Schema) *ir.Schema {
        return processNode(s)
    },
})

// Convert back to JSON
jsonValue := ir.ToJSON(schema)
```

### Key IR types

- `Schema` — Root node with all JSON Schema keywords
- `TypeSet` — Efficient bitset for primitive types
- `Object`, `Array`, `String`, `Number` — Type-specific constraints
- `Metadata` — Origin tracking and vendor extensions
- `SchemaFacts` — Cached type analysis for performance

## Utility packages

### orderedmap

Order-preserving map for deterministic JSON output:

```go
import "github.com/nvidia-lpu/parsec/orderedmap"

om := orderedmap.New()
om.Set("b", 2)
om.Set("a", 1)
// Iteration order: b, a (insertion order preserved)
```

### jsonpointer

RFC 6901 JSON Pointer utilities:

```go
import "github.com/nvidia-lpu/parsec/jsonpointer"

pointer := jsonpointer.Encode([]string{"properties", "user", "name"})
// Returns: "/properties/user/name"

tokens, err := jsonpointer.Parse("/properties/user/name")
// Returns: ["properties", "user", "name"]

fragment := jsonpointer.Fragment(tokens)
// Returns: "#/properties/user/name"
```

## Design constraints

- **Root must be object**: Schemas must have `type: "object"` at root
- **No root combinators**: Cannot start with `anyOf`, `oneOf`, `enum`, or `not`
- **Explicit disambiguation**: All unions must resolve via one of five strategies
- **Disjoint patterns**: `patternProperties` patterns must not overlap
- **Finite unions**: Union ambiguity (e.g., multiple null-accepting branches)
  rejected with actionable errors
- **DoS protection**: Default limits prevent adversarial schemas

## Performance

parsec is optimized for production use:

- **Schema facts caching**: Primitive type analysis cached globally
- **Fast-path decoding**: Simple `{"type": "string"}` schemas bypass full parser
- **Preallocation**: Maps and slices sized to avoid reallocation
- **Closure elimination**: Hot paths use struct fields instead of closures
- **Bitset operations**: Type checks use efficient uint8 bitsets

### Benchmarks

```sh
go test -bench=. -run=^$ ./jsonschema        # All benchmarks
go test -bench=Validate ./jsonschema         # Validation only
go test -bench=OptimizeIR ./jsonschema       # Optimizer only
```

## Testing

parsec maintains high test coverage with multiple strategies:

- **Unit tests**: Comprehensive keyword and edge case coverage
- **Property-based tests**: Random schema generation with [rapid](https://pgregory.net/rapid/)
- **Golden tests**: 100+ schema transformation snapshots
- **Benchmarks**: Performance regression detection

```sh
mise r test           # Run full test suite with race detector
mise r test:no-race   # Skip race detector for faster iteration
mise r test:sanity    # Run tests and lint concurrently
```

## Contributing

This module follows the Google Go Style Guide. Before opening a PR:

```bash
mise r fmt    # Format and auto-fix lint issues
mise r lint   # Run golangci-lint and go vet
mise r test   # Run tests with race detector
```

CI mirrors these tasks. Release automation uses `release-please`.

## License

See [LICENSE](LICENSE) for details.
