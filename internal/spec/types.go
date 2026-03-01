package spec

// Spec represents a parsed markdown specification.
type Spec struct {
	Title       string
	Description string
	Sections    []Section
	RawContent  string // full markdown, used for LLM prompt
}

// Section represents a single heading and its content within a spec.
type Section struct {
	Heading string
	Level   int    // 1, 2, 3...
	Content string // text content under this heading
}
