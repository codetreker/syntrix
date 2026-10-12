package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetreker/syntrix/internal/deployment"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "render-kubernetes" {
		return fmt.Errorf("usage: syntrix-cli render-kubernetes --template FILE --config FILE --output FILE [--bootstrap --writes-quiesced]")
	}
	flags := flag.NewFlagSet("render-kubernetes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options deployment.FileOptions
	flags.StringVar(&options.TemplatePath, "template", "", "Kubernetes template file")
	flags.StringVar(&options.ConfigPath, "config", "", "Deployment configuration YAML")
	flags.StringVar(&options.OutputPath, "output", "", "Rendered manifest file; explicitly use - for stdout")
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
	return deployment.Generate(options, stdout)
}
