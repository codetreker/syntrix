// Package syntrix provides go:generate directives for code generation.
//
// Run "make -C packages/syntrix generate" from the repository root to regenerate
// all generated code.
//
// Prerequisites:
//   - protoc: https://grpc.io/docs/protoc-installation/
//   - protoc-gen-go: go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
//   - protoc-gen-go-grpc: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.0
//
//go:generate go run scripts/compile_proto.go
package syntrix
