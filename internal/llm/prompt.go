package llm

// CodeGenerationPrompt is the system prompt template for code generation.
// Callers substitute placeholders using strings.ReplaceAll.
const CodeGenerationPrompt = `You are building software to match this specification exactly.

SPECIFICATION:
{spec_content}

{failure_details}

Generate a complete, working implementation. Include:
- All source code files
- A Dockerfile that builds and runs the application
- Any configuration files needed

Output each file in this format:
=== FILE: path/to/file.ext ===
file contents here
=== END FILE ===`

// SatisfactionJudgePrompt is the combined system prompt template for the LLM judge.
// Deprecated: use SatisfactionJudgeSystem and SatisfactionJudgeUser instead.
const SatisfactionJudgePrompt = `You are a QA evaluator. Score how well this software behavior matches the expected behavior.

Scenario: {scenario_description}
Step: {step_description}

Expected behavior: {expected}

Actual observed behavior:
{observed}

Respond with JSON only:
{
  "score": <0-100>,
  "reasoning": "<brief explanation>",
  "failures": ["<specific failure 1>", "<specific failure 2>"]
}

Scoring guide:
- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations (different wording, extra fields)
- 50-79: Partially correct (some aspects work, others don't)
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error`

// SatisfactionJudgeSystem is the system prompt for the LLM judge.
const SatisfactionJudgeSystem = `You are a QA evaluator. Score how well this software behavior matches the expected behavior.

Respond with JSON only:
{"score": <0-100>, "reasoning": "<brief explanation>", "failures": ["<specific failure>"]}

Scoring guide:
- 100: Perfect match to expected behavior
- 80-99: Works correctly with minor deviations
- 50-79: Partially correct
- 1-49: Mostly broken but shows some correct behavior
- 0: Complete failure or error`

// SatisfactionJudgeUser is the user prompt template for the LLM judge.
// Callers substitute placeholders using strings.ReplaceAll.
const SatisfactionJudgeUser = `Scenario: {scenario_description}
Step: {step_description}

Expected behavior: {expected}

Actual observed behavior:
{observed}`
