package deploymentcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/codetreker/syntrix/deployment"
	"github.com/codetreker/syntrix/deployment/config"
)

type FileOptions struct {
	TemplatePath, ConfigPath, OutputPath string
	deployment.Options
}

func Generate(options FileOptions, stdout io.Writer) error {
	if options.TemplatePath == "" || options.ConfigPath == "" || options.OutputPath == "" {
		return fmt.Errorf("--template, --config, and --output are required; use --output - explicitly for stdout")
	}
	if err := options.Options.Validate(); err != nil {
		return err
	}
	if options.OutputPath != "-" {
		for _, input := range []string{options.TemplatePath, options.ConfigPath} {
			if err := distinctPaths(input, options.OutputPath); err != nil {
				return err
			}
		}
	}
	file, err := os.Open(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("open deployment configuration: %w", err)
	}
	c, loadErr := config.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		return loadErr
	}
	if closeErr != nil {
		return fmt.Errorf("close deployment configuration: %w", closeErr)
	}
	source, err := os.ReadFile(options.TemplatePath)
	if err != nil {
		return fmt.Errorf("read Kubernetes template: %w", err)
	}
	manifest, err := deployment.RenderSource(source, c, options.Options)
	if err != nil {
		return err
	}
	if options.OutputPath == "-" {
		if stdout == nil {
			return fmt.Errorf("stdout writer is required for --output -")
		}
		if n, err := stdout.Write(manifest); err != nil {
			return fmt.Errorf("write rendered manifest to stdout: %w", err)
		} else if n != len(manifest) {
			return fmt.Errorf("write rendered manifest to stdout: %w", io.ErrShortWrite)
		}
		return nil
	}
	return writeAtomic(options.OutputPath, manifest)
}

func distinctPaths(input, output string) error {
	inputPath, err := filepath.Abs(input)
	if err != nil {
		return fmt.Errorf("resolve input path: %w", err)
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if inputPath == outputPath {
		return fmt.Errorf("output must not overwrite the template or deployment configuration")
	}
	inputInfo, inputErr := os.Stat(input)
	outputInfo, outputErr := os.Stat(output)
	if inputErr == nil && outputErr == nil && os.SameFile(inputInfo, outputInfo) {
		return fmt.Errorf("output must not overwrite the template or deployment configuration through a link")
	}
	return nil
}

func writeAtomic(path string, data []byte) (returnErr error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".kubernetes-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	name := file.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary manifest: %w", err))
		}
	}()
	if err := persistManifest(file, data); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace rendered manifest: %w", err)
	}
	return nil
}

type syncWriteCloser interface {
	io.WriteCloser
	Sync() error
}

func persistManifest(file syncWriteCloser, data []byte) (returnErr error) {
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close temporary manifest: %w", err))
		}
	}()
	if n, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary manifest: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write temporary manifest: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary manifest: %w", err)
	}
	return nil
}
