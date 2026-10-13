package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetreker/syntrix/deployment"
	"github.com/codetreker/syntrix/internal/deploymentcli"
)

var version = "development"
var gitCommit = "unknown"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "deployment-contract" {
		if len(args) != 1 {
			return fmt.Errorf("deployment-contract does not accept arguments")
		}
		if len(version) > 64 || len(gitCommit) > 64 {
			return fmt.Errorf("deployment contract build identity exceeds 64 bytes")
		}
		data, err := json.Marshal(struct {
			Contract  int      `json:"managed_runtime_contract"`
			Version   string   `json:"version"`
			GitCommit string   `json:"git_commit"`
			Modes     []string `json:"modes"`
			Phases    []string `json:"render_phases"`
		}{deployment.ManagedRuntimeContract, version, gitCommit, []string{"standalone", "distributed"}, []string{"bootstrap", "acceptance", "full"}})
		if err != nil {
			return fmt.Errorf("encode deployment contract: %w", err)
		}
		data = append(data, '\n')
		if n, err := stdout.Write(data); err != nil {
			return fmt.Errorf("write deployment contract: %w", err)
		} else if n != len(data) {
			return fmt.Errorf("write deployment contract: %w", io.ErrShortWrite)
		}
		return nil
	}
	if len(args) == 0 || args[0] != "render-kubernetes" {
		return fmt.Errorf("usage: syntrix-cli deployment-contract | render-kubernetes --template FILE --config FILE --output FILE [--bootstrap --writes-quiesced | --acceptance-only]")
	}
	flags := flag.NewFlagSet("render-kubernetes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options deploymentcli.FileOptions
	flags.StringVar(&options.TemplatePath, "template", "", "Kubernetes template file")
	flags.StringVar(&options.ConfigPath, "config", "", "Deployment configuration YAML")
	flags.StringVar(&options.OutputPath, "output", "", "Rendered manifest file; explicitly use - for stdout")
	flags.BoolVar(&options.AcceptanceOnly, "acceptance-only", false, "Hold internal writers through indexed-query acceptance; attest external writers and cleanup remain paused")
	flags.BoolVar(&options.Bootstrap, "bootstrap", false, "Render stopped workloads and an isolated index-bootstrap Pod")
	flags.BoolVar(&options.WritesQuiesced, "writes-quiesced", false, "Attest all external writers and MongoDB tombstone TTL deletion are paused")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("render-kubernetes does not accept positional arguments")
	}
	return deploymentcli.Generate(options, stdout)
}
