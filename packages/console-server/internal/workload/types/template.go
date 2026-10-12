package types

type Template struct {
	ID            string            `json:"id" yaml:"id"`
	Image         string            `json:"image" yaml:"image"`
	Port          int32             `json:"port" yaml:"port"`
	Env           map[string]string `json:"env,omitempty" yaml:"env"`
	CPURequest    string            `json:"cpu_request" yaml:"cpu_request"`
	CPULimit      string            `json:"cpu_limit" yaml:"cpu_limit"`
	MemoryRequest string            `json:"memory_request" yaml:"memory_request"`
	MemoryLimit   string            `json:"memory_limit" yaml:"memory_limit"`
	ProbePath     string            `json:"probe_path" yaml:"probe_path"`
}
