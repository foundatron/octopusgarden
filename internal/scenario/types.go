package scenario

// Scenario represents a holdout validation scenario loaded from YAML.
type Scenario struct {
	ID                   string   `yaml:"id"`
	Description          string   `yaml:"description"`
	Type                 string   `yaml:"type"`   // "api" only for MVP
	Weight               *float64 `yaml:"weight"` // nil means not set, defaults to 1.0
	Setup                []Step   `yaml:"setup"`
	Steps                []Step   `yaml:"steps"`
	SatisfactionCriteria string   `yaml:"satisfaction_criteria"`
}

// Step represents a single action within a scenario.
type Step struct {
	Description string    `yaml:"description"`
	Request     Request   `yaml:"request"`
	Expect      string    `yaml:"expect"` // natural language, judged by LLM
	Capture     []Capture `yaml:"capture"`
}

// Request describes an HTTP request to execute.
type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
}

// Capture defines a variable to extract from a response.
type Capture struct {
	Name     string `yaml:"name"`     // variable name
	JSONPath string `yaml:"jsonpath"` // path into response body
}
