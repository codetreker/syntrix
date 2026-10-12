package deployment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/codetreker/syntrix/internal/deployment/config"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Bootstrap      bool
	WritesQuiesced bool
}

type manifestYAMLError struct{ cause error }

func (e manifestYAMLError) Error() string {
	return "rendered Kubernetes manifest contains invalid YAML or duplicate keys"
}

func (e manifestYAMLError) Unwrap() error { return e.cause }

type templateError struct {
	message string
	cause   error
}

func (e templateError) Error() string { return e.message }
func (e templateError) Unwrap() error { return e.cause }

func (o Options) validate() error {
	if o.Bootstrap != o.WritesQuiesced {
		return fmt.Errorf("--bootstrap and --writes-quiesced must be specified together; attest all external writers and MongoDB tombstone TTL deletion are paused")
	}
	return nil
}

func Render(source []byte, c config.Config, options Options) ([]byte, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	model := buildModel(c, options.Bootstrap)
	functions := template.FuncMap{
		"quote":         quote,
		"indent":        indent,
		"runtimeConfig": model.runtimeConfig,
		"credentials":   model.credentials,
		"requireMode": func(mode string) (string, error) {
			if mode != c.Mode {
				return "", fmt.Errorf("template mode does not match configuration mode")
			}
			return "", nil
		},
	}
	tmpl, err := template.New("kubernetes").Option("missingkey=error").Funcs(functions).Parse(string(source))
	if err != nil {
		return nil, templateError{message: "parse Kubernetes template: invalid template syntax", cause: err}
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, model); err != nil {
		return nil, templateError{message: "render Kubernetes template: execution failed; verify template fields and deployment mode", cause: err}
	}
	if err := validateManifest(output.Bytes()); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func quote(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

func indent(spaces int, value string) string {
	prefix := strings.Repeat(" ", spaces)
	return prefix + strings.ReplaceAll(strings.TrimSuffix(value, "\n"), "\n", "\n"+prefix)
}

func validateManifest(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	documents := 0
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifestYAMLError{cause: err}
		}
		metadata, ok := document["metadata"].(map[string]any)
		if !ok {
			return fmt.Errorf("each rendered Kubernetes document must contain metadata")
		}
		for _, value := range []any{document["apiVersion"], document["kind"], metadata["name"]} {
			if text, ok := value.(string); !ok || text == "" {
				return fmt.Errorf("each rendered Kubernetes document must contain string apiVersion, kind, and metadata.name")
			}
		}
		documents++
	}
	if documents == 0 {
		return fmt.Errorf("rendered Kubernetes manifest must contain at least one document")
	}
	return nil
}
