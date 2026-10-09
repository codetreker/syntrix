package types

import (
	"encoding/json"
	"errors"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a wrapper around time.Duration that supports JSON/YAML string marshaling.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = Duration(time.Duration(value))
		return nil
	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(tmp)
		return nil
	default:
		return errors.New("invalid duration")
	}
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var i int64
	if err := value.Decode(&i); err == nil {
		*d = Duration(time.Duration(i))
		return nil
	}

	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	tmp, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(tmp)
	return nil
}
