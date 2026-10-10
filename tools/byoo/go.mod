module byoo

go 1.25.0

require (
	github.com/NVIDIA/nvcf/src/libraries/go/lib v0.0.0-20261009233424-e19ac175fc72
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/NVIDIA/nvcf/src/libraries/go/lib => ../../src/libraries/go/lib
