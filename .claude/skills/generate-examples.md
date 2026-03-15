# Generate Examples

Run app generation (`octog run`) for every example in the repo, mixing providers to exercise both
backends. Use this before merging PRs to verify end-to-end generation still works.

TRIGGER when: user asks to "run examples", "generate examples", "run app generation", "pre-merge
check", "test all examples", or "smoke test".

## Instructions

1. First, build the binary:

```sh
make build
```

1. Discover all examples by listing directories under `examples/` that contain both a `spec.md` and
   a `scenarios/` subdirectory.

1. Assign providers to examples in alternating fashion to get roughly equal coverage of both
   `anthropic` and `openai`. For example, with 6 examples, 3 should use anthropic and 3 should use
   openai. Randomize or rotate which examples get which provider across runs.

1. Run each example **sequentially** (one at a time, not in parallel) using:

```sh
./octog run \
  -spec examples/<name>/spec.md \
  -scenarios examples/<name>/scenarios/ \
  -provider <provider> \
  -budget 5
```

Use the Bash tool with a generous timeout (600000ms / 10 minutes) for each run. Do NOT run these in
the background — run them sequentially and check each result before proceeding.

1. After each run, record the result (pass/fail, final satisfaction score, provider used).

1. After all examples have been run, produce a summary table:

```text
| Example           | Provider  | Result | Score | Notes |
|-------------------|-----------|--------|-------|-------|
| hello-api         | anthropic | PASS   | 98    |       |
| todo-app          | openai    | FAIL   | 72    | ...   |
| ...               | ...       | ...    | ...   | ...   |
```

1. If any example fails (does not converge), flag it clearly and include the relevant error output.

## Notes

- API keys are in the platform-native config file (macOS: `~/Library/Application Support/octopusgarden/config`; Linux: `~/.config/octopusgarden/config`; legacy: `~/.octopusgarden/config`) — no env vars needed.
- Default threshold is 95%. Do not override unless the user specifies.
- If the user asks to run with a specific provider only, use that provider for all examples.
- If the user asks to run a specific example, run only that one.
- If a run fails with a transient error (rate limit, network), note it and move on to the next
  example. Do not retry automatically.
