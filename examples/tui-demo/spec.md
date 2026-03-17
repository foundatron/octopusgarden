# Counter TUI

A menu-driven terminal counter application.

## Display

The application renders in the terminal with:

- A header showing the current counter value: `Counter: N` (starts at 0)
- A numbered menu with four options:
  1. Increment
  1. Decrement
  1. Reset
  1. Quit

All output must fit within 80 columns and 24 rows.

## Behavior

- Pressing `1` increments the counter by 1 and re-renders the display
- Pressing `2` decrements the counter by 1 and re-renders the display
- Pressing `3` resets the counter to 0 and re-renders the display
- Pressing `4` exits the application with exit code 0
- Any other key is ignored; the display re-renders unchanged

## Requirements

- Pure stdin/stdout terminal application; no network required
- Runs as a single binary named `app` in the container working directory
- The command to launch is `./app`
- After each key press the updated counter value is visible immediately
- Counter may go negative (decrement below 0 is allowed)
- Any programming language
