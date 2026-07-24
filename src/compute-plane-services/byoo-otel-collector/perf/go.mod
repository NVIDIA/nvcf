module github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf

go 1.25.0

require (
	github.com/NVIDIA/nvcf/src/libraries/go/lib v0.0.0
	k8s.io/api v0.34.2
	k8s.io/apimachinery v0.34.2
	sigs.k8s.io/yaml v1.6.0
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-containerregistry v0.21.5 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/utils v0.0.0-20250604170112-4c0f3b243397 // indirect
	sigs.k8s.io/json v0.0.0-20241014173422-cfa47c3a1cc8 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.3.0 // indirect
)

// The perf suite consumes the shared SIS translation library directly so that
// it renders exactly the workload shape NVCA produces in production.
replace github.com/NVIDIA/nvcf/src/libraries/go/lib => ../../../libraries/go/lib

// Mirror the k8s pins used by the shared library. replace directives in
// dependencies are not inherited, so we repeat them here for the main module.
replace (
	k8s.io/api => k8s.io/api v0.34.2
	k8s.io/apimachinery => k8s.io/apimachinery v0.34.2
	k8s.io/client-go => k8s.io/client-go v0.34.2
)
