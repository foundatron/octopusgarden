# Octopus Garden CLI

A command-line tool that prints ASCII art of an octopus and its garden.

## Commands

### draw

Print ASCII art of an octopus in a garden.

- `octopus-garden draw` — default medium-sized octopus garden
- `octopus-garden draw --size small` — compact version (at most 10 lines)
- `octopus-garden draw --size large` — large detailed version (at least 20 lines)
- Output must contain at least one octopus character (e.g. using characters like `O`, `~`, or
  tentacle shapes)
- Output must contain at least one garden element (e.g. seaweed `}`, coral, shells, fish)
- Exit code: 0

### greet

Print a personalized greeting mentioning the octopus garden.

- `octopus-garden greet Alice` — prints a greeting that includes the name "Alice" and mentions
  "octopus garden" or "garden"
- Exit code: 0
- If no name argument is provided: print usage message to stderr and exit with code 1

## Error Handling

- Unknown command (e.g. `octopus-garden unknown`): print error to stderr and exit with code 1
- Unknown flags (e.g. `octopus-garden draw --invalid`): print error to stderr and exit with code 1

## Requirements

- Any programming language
- Single binary or script invocable as `octopus-garden`
- Must be installed to a PATH location in the Dockerfile
