//go:build ignore

// compile_proto.go compiles all proto files in the api/proto directory.
// Usage: go run scripts/compile_proto.go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	protoDir   = "api/proto"
	outputDir  = "."
	modulePath = "github.com/codetreker/syntrix"
)

var (
	verbose = flag.Bool("v", false, "enable verbose output")
	dryRun  = flag.Bool("dry-run", false, "print commands without executing")
)

func main() {
	flag.Parse()

	if _, err := os.Stat("go.mod"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: run from the Go package directory (packages/syntrix): %v\n", err)
		os.Exit(1)
	}

	// Ensure GOPATH/bin is in PATH for protoc plugins
	ensureGoBinInPath()

	// Check for required tools
	if err := checkTools(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Find all proto files
	protoFiles, err := findProtoFiles(protoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding proto files: %v\n", err)
		os.Exit(1)
	}

	if len(protoFiles) == 0 {
		fmt.Println("No proto files found in", protoDir)
		return
	}

	fmt.Printf("Found %d proto file(s) to compile\n", len(protoFiles))

	// Compile each proto file
	for _, protoFile := range protoFiles {
		if err := compileProto(protoFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error compiling %s: %v\n", protoFile, err)
			os.Exit(1)
		}
	}

	fmt.Println("Successfully compiled all proto files")
}

func checkTools() error {
	tools := []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"}

	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found in PATH. Install instructions:\n"+
				"  protoc:             https://grpc.io/docs/protoc-installation/\n"+
				"  protoc-gen-go:      go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11\n"+
				"  protoc-gen-go-grpc: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.0",
				tool)
		}
	}
	return nil
}

func findProtoFiles(dir string) ([]string, error) {
	var files []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".proto") {
			files = append(files, path)
		}
		return nil
	})

	return files, err
}

func compileProto(protoFile string) error {
	// Use module option to let protoc automatically determine output path
	// from go_package option in proto file.
	// e.g., go_package = "github.com/codetreker/syntrix/api/puller/v1;pullerv1"
	//       module = "github.com/codetreker/syntrix"
	//       output -> api/puller/v1/
	args := []string{
		"--proto_path=" + protoDir,
		"--go_out=" + outputDir,
		"--go_opt=module=" + modulePath,
		"--go-grpc_out=" + outputDir,
		"--go-grpc_opt=module=" + modulePath,
		protoFile,
	}

	if *verbose {
		fmt.Printf("Compiling: %s\n", protoFile)
		fmt.Printf("  Command: protoc %s\n", strings.Join(args, " "))
	} else {
		fmt.Printf("Compiling: %s\n", protoFile)
	}

	if *dryRun {
		return nil
	}

	cmd := exec.Command("protoc", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ensureGoBinInPath adds GOPATH/bin or ~/go/bin to PATH if not present.
// This ensures protoc can find protoc-gen-go and protoc-gen-go-grpc plugins.
func ensureGoBinInPath() {
	goBin := os.Getenv("GOPATH")
	if goBin == "" {
		// Default GOPATH is ~/go
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		goBin = filepath.Join(home, "go")
	}
	goBin = filepath.Join(goBin, "bin")

	// Check if already in PATH
	pathEnv := os.Getenv("PATH")
	pathSep := ":"
	if runtime.GOOS == "windows" {
		pathSep = ";"
	}

	for _, p := range strings.Split(pathEnv, pathSep) {
		if p == goBin {
			return // Already in PATH
		}
	}

	// Add to PATH
	os.Setenv("PATH", goBin+pathSep+pathEnv)
}
