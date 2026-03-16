package lint

//go:generate go run ./gen

// Rule describes a single lint rule for documentation and sync-testing.
type Rule struct {
	ID          string // e.g. "S001", "SC001"
	Level       Level  // Error or Warning
	Summary     string // one-line description for docs
	Detail      string // optional longer explanation
	MsgContains string // substring that the diagnostic message must contain (for sync tests)
}

// SpecRules enumerates every diagnostic that CheckSpec can produce.
var SpecRules = []Rule{
	{
		ID:          "S001",
		Level:       Error,
		Summary:     "spec file must not be empty",
		MsgContains: "spec file is empty",
	},
	{
		ID:          "S002",
		Level:       Error,
		Summary:     "spec must have a level-1 heading (title)",
		MsgContains: "no level-1 heading found",
	},
	{
		ID:          "S003",
		Level:       Warning,
		Summary:     "title should have description text after it",
		MsgContains: "no description text after title heading",
	},
	{
		ID:          "S004",
		Level:       Warning,
		Summary:     "sections should not be empty",
		MsgContains: "has no content",
	},
	{
		ID:          "S005",
		Level:       Warning,
		Summary:     "headings at the same level should be unique",
		MsgContains: "duplicate heading",
	},
	{
		ID:          "S006",
		Level:       Warning,
		Summary:     "fenced code blocks should be closed",
		MsgContains: "unclosed fenced code block",
	},
}

// ScenarioRules enumerates every diagnostic that CheckScenario/CheckScenarioDir can produce.
var ScenarioRules = []Rule{
	{
		ID:          "SC001",
		Level:       Error,
		Summary:     "scenario file must not be empty",
		MsgContains: "scenario file is empty",
	},
	{
		ID:          "SC002",
		Level:       Error,
		Summary:     "file must be valid YAML",
		MsgContains: "invalid YAML",
	},
	// SC003 reserved — the "empty YAML document" guard in lintScenarioContent is
	// defense-in-depth; yaml.Unmarshal always produces a content node.
	{
		ID:          "SC004",
		Level:       Error,
		Summary:     "scenario root must be a YAML mapping",
		MsgContains: "scenario must be a YAML mapping",
	},
	{
		ID:          "SC005",
		Level:       Error,
		Summary:     "id field is required",
		MsgContains: "missing required field: id",
	},
	{
		ID:          "SC006",
		Level:       Error,
		Summary:     "id must not be empty",
		MsgContains: "id must not be empty",
	},
	{
		ID:          "SC007",
		Level:       Error,
		Summary:     "id must be lowercase alphanumeric with hyphens/underscores",
		Detail:      "Must match ^[a-z0-9][a-z0-9_-]*$.",
		MsgContains: "must match pattern ^[a-z0-9]",
	},
	{
		ID:          "SC008",
		Level:       Warning,
		Summary:     "type should be one of the recognized values",
		Detail:      "Recognized types: functional, api.",
		MsgContains: "not one of: functional",
	},
	{
		ID:          "SC009",
		Level:       Warning,
		Summary:     "weight must be a number",
		MsgContains: "weight must be a number",
	},
	{
		ID:          "SC010",
		Level:       Warning,
		Summary:     "weight must be positive",
		MsgContains: "weight must be positive",
	},
	{
		ID:          "SC011",
		Level:       Error,
		Summary:     "steps field is required",
		MsgContains: "missing required field: steps",
	},
	{
		ID:          "SC012",
		Level:       Error,
		Summary:     "steps must be a non-empty array",
		MsgContains: "steps must be a non-empty array",
	},
	{
		ID:          "SC013",
		Level:       Error,
		Summary:     "setup must be an array",
		MsgContains: "setup must be an array",
	},
	{
		ID:          "SC014",
		Level:       Error,
		Summary:     "each step must be a YAML mapping",
		MsgContains: "step must be a mapping",
	},
	{
		ID:          "SC015",
		Level:       Error,
		Summary:     "step must have exactly one step type (request, exec, browser, grpc, ws, or tui)",
		MsgContains: "exactly one of request, exec, browser, grpc, ws, or tui is required",
	},
	{
		ID:          "SC016",
		Level:       Warning,
		Summary:     "judged steps should have an expect field",
		MsgContains: "step missing expect field",
	},
	{
		ID:          "SC017",
		Level:       Warning,
		Summary:     "judged steps should have a description",
		MsgContains: "step missing description field",
	},
	{
		ID:          "SC018",
		Level:       Error,
		Summary:     "request must be a YAML mapping",
		MsgContains: "request must be a mapping",
	},
	{
		ID:          "SC019",
		Level:       Error,
		Summary:     "request must have a method",
		MsgContains: "request missing required field: method",
	},
	{
		ID:          "SC020",
		Level:       Error,
		Summary:     "HTTP method must be valid",
		Detail:      "Allowed: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS.",
		MsgContains: "invalid HTTP method",
	},
	{
		ID:          "SC021",
		Level:       Error,
		Summary:     "request must have a path",
		MsgContains: "request missing required field: path",
	},
	{
		ID:          "SC022",
		Level:       Error,
		Summary:     "capture must be an array",
		MsgContains: "capture must be an array",
	},
	{
		ID:          "SC023",
		Level:       Error,
		Summary:     "capture entries must be YAML mappings",
		MsgContains: "capture entry must be a mapping",
	},
	{
		ID:          "SC024",
		Level:       Error,
		Summary:     "capture must have a name field",
		MsgContains: "capture missing required field: name",
	},
	{
		ID:          "SC025",
		Level:       Error,
		Summary:     "capture name must not be empty",
		MsgContains: "capture name must not be empty",
	},
	{
		ID:          "SC026",
		Level:       Error,
		Summary:     "capture name must be a valid identifier",
		Detail:      "Must match ^[a-zA-Z_][a-zA-Z0-9_]*$.",
		MsgContains: "must match pattern ^[a-zA-Z_]",
	},
	{
		ID:          "SC027",
		Level:       Warning,
		Summary:     "capture name shadows an earlier capture",
		MsgContains: "shadows earlier capture",
	},
	{
		ID:          "SC028",
		Level:       Error,
		Summary:     "capture must have jsonpath or source",
		MsgContains: "capture requires at least one of jsonpath or source",
	},
	{
		ID:          "SC029",
		Level:       Error,
		Summary:     "jsonpath must use valid $.field.sub syntax",
		Detail:      "Must start with $. followed by dot-separated field names.",
		MsgContains: "invalid jsonpath",
	},
	{
		ID:          "SC030",
		Level:       Warning,
		Summary:     "variable reference has no matching capture",
		Detail:      "Variables use {name} syntax and must be captured in a prior step.",
		MsgContains: "referenced but never captured",
	},
	{
		ID:          "SC031",
		Level:       Error,
		Summary:     "scenario ids must be unique across a directory",
		MsgContains: "duplicate scenario id",
	},
	{
		ID:          "SC032",
		Level:       Error,
		Summary:     "step must not have multiple step types",
		MsgContains: "step has multiple step types; exactly one of request, exec, browser, grpc, ws, or tui is required",
	},
	{
		ID:          "SC033",
		Level:       Error,
		Summary:     "exec must be a YAML mapping",
		MsgContains: "exec must be a mapping",
	},
	{
		ID:          "SC034",
		Level:       Error,
		Summary:     "exec must have a command field",
		MsgContains: "exec missing required field: command",
	},
	{
		ID:          "SC035",
		Level:       Error,
		Summary:     "exec command must not be empty",
		MsgContains: "exec command must not be empty",
	},
	{
		ID:          "SC036",
		Level:       Error,
		Summary:     "exec env must be a YAML mapping",
		MsgContains: "exec env must be a mapping",
	},
	{
		ID:          "SC037",
		Level:       Error,
		Summary:     "exec timeout must be a valid Go duration",
		MsgContains: "not a valid duration",
	},
	{
		ID:          "SC038",
		Level:       Error,
		Summary:     "capture source invalid for step type",
		MsgContains: "invalid source",
	},
	{
		ID:          "SC039",
		Level:       Error,
		Summary:     "source not supported on this step type",
		MsgContains: "source is not supported on",
	},
	{
		ID:          "SC040",
		Level:       Error,
		Summary:     "browser must be a YAML mapping",
		MsgContains: "browser must be a mapping",
	},
	{
		ID:          "SC041",
		Level:       Error,
		Summary:     "browser must have an action field",
		MsgContains: "browser missing required field: action",
	},
	{
		ID:          "SC042",
		Level:       Error,
		Summary:     "browser action must be navigate, click, fill, or assert",
		MsgContains: "invalid browser action",
	},
	{
		ID:          "SC043",
		Level:       Error,
		Summary:     "browser navigate requires url",
		MsgContains: "navigate action requires url",
	},
	{
		ID:          "SC044",
		Level:       Error,
		Summary:     "browser click requires selector",
		MsgContains: "click action requires selector",
	},
	{
		ID:          "SC045",
		Level:       Error,
		Summary:     "browser fill requires selector",
		MsgContains: "fill action requires selector",
	},
	{
		ID:          "SC046",
		Level:       Error,
		Summary:     "browser fill requires value",
		MsgContains: "fill action requires value",
	},
	{
		ID:          "SC047",
		Level:       Error,
		Summary:     "browser assert requires selector",
		MsgContains: "assert action requires selector",
	},
	{
		ID:          "SC048",
		Level:       Warning,
		Summary:     "browser assert should have assertion fields",
		MsgContains: "no assertion fields",
	},
	{
		ID:          "SC049",
		Level:       Error,
		Summary:     "browser timeout must be a valid Go duration",
		MsgContains: "browser timeout: invalid duration",
		Detail:      "Uses same format as exec timeout (e.g. 10s, 30s).",
	},
	// gRPC step rules.
	{
		ID:          "SC050",
		Level:       Error,
		Summary:     "grpc must be a YAML mapping",
		MsgContains: "grpc must be a mapping",
	},
	{
		ID:          "SC051",
		Level:       Error,
		Summary:     "grpc service is required",
		MsgContains: "grpc missing required field: service",
	},
	{
		ID:          "SC052",
		Level:       Error,
		Summary:     "grpc method is required",
		MsgContains: "grpc missing required field: method",
	},
	{
		ID:          "SC053",
		Level:       Error,
		Summary:     "grpc timeout must be a valid Go duration",
		MsgContains: "grpc timeout",
	},
	{
		ID:          "SC054",
		Level:       Error,
		Summary:     "grpc stream messages must be an array",
		MsgContains: "grpc stream messages must be an array",
	},
	{
		ID:          "SC055",
		Level:       Error,
		Summary:     "grpc receive timeout must be a valid Go duration",
		MsgContains: "grpc receive timeout",
	},
	// WebSocket step rules.
	{
		ID:          "SC060",
		Level:       Error,
		Summary:     "ws must be a YAML mapping",
		MsgContains: "ws must be a mapping",
	},
	{
		ID:          "SC061",
		Level:       Warning,
		Summary:     "ws step missing url; no prior connection may exist",
		MsgContains: "ws step missing url",
	},
	{
		ID:          "SC062",
		Level:       Error,
		Summary:     "ws receive timeout must be a valid Go duration",
		MsgContains: "ws receive timeout: invalid duration",
	},
	{
		ID:          "SC063",
		Level:       Error,
		Summary:     "ws receive count must be a positive integer",
		MsgContains: "ws receive count must be a positive integer",
	},
	// TUI step rules.
	{
		ID:          "SC070",
		Level:       Error,
		Summary:     "tui must be a YAML mapping",
		MsgContains: "tui must be a mapping",
	},
	{
		ID:          "SC071",
		Level:       Error,
		Summary:     "tui step requires command or at least one interaction field",
		MsgContains: "tui step requires command",
	},
	{
		ID:          "SC072",
		Level:       Error,
		Summary:     "tui command must not be empty",
		MsgContains: "tui command must not be empty",
	},
	{
		ID:          "SC073",
		Level:       Error,
		Summary:     "tui timeout must be a valid Go duration",
		MsgContains: "tui timeout: invalid duration",
	},
}
