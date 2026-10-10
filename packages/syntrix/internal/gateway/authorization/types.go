package authorization

import "time"

type RuleSet struct {
	Database string                `json:"database" yaml:"database"`
	Version  string                `json:"rules_version" yaml:"rules_version"`
	Service  string                `json:"service" yaml:"service"`
	Match    map[string]MatchBlock `json:"match" yaml:"match"`
}

type MatchBlock struct {
	Allow map[string]string     `json:"allow" yaml:"allow"`
	Match map[string]MatchBlock `json:"match" yaml:"match"`
}

type Request struct {
	Auth     Authenticated `json:"auth"`
	Resource *Resource     `json:"resource,omitempty"`
	Time     time.Time     `json:"time"`
}

type Authenticated struct {
	UID      interface{}            `json:"userId"`
	Username string                 `json:"username,omitempty"`
	Roles    []string               `json:"roles"`
	DBAdmin  []string               `json:"db_admin,omitempty"`
	Claims   map[string]interface{} `json:"claims,omitempty"`
}

type Resource struct {
	Data map[string]interface{} `json:"data"`
	ID   string                 `json:"id"`
}
