package llm

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
