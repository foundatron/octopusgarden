# Timer Board

A terminal countdown timer manager with CLI subcommands for managing timer presets and an
interactive TUI for running multiple timers simultaneously with a two-panel layout.

## Data Format

Timer presets are stored in a JSON file as an array of objects. Each timer has:

- `name` (string): the timer name
- `seconds` (integer): the countdown duration in seconds

Example file contents:

```json
[
  {"name": "Eggs", "seconds": 180},
  {"name": "Tea", "seconds": 120}
]
```

If the file does not exist, CLI subcommands that read it treat it as an empty list. The `add`
subcommand creates the file if it does not exist.

## CLI Subcommands

The binary is named `myapp`. All subcommands take `--file <path>` to specify the JSON file path. The
`--file` flag is required for all subcommands.

### `myapp add --file <path> <name> <seconds>`

Add a new timer preset with the given name and duration in seconds. Prints nothing on success and
exits with code 0.

Validation: if `<name>` is empty or missing, or `<seconds>` is not a positive integer, print an
error to stderr and exit with code 1. If a timer with the same name already exists, print an error
to stderr and exit with code 1.

### `myapp list --file <path>`

Print all timer presets to stdout, one per line, in the format:

```text
Eggs       03:00
Tea        02:00
```

Name is left-aligned, duration is formatted as MM:SS. If there are no timers, print nothing and exit
0\.

### `myapp remove --file <path> <name>`

Remove the timer preset with the given name. Prints nothing on success and exits with code 0. If no
timer with that name exists, print an error to stderr and exit with code 1.

### `myapp validate --file <path>`

Check that the file contains valid JSON in the expected format. On success, print `ok: N timers` to
stdout (where N is the count) and exit 0. On failure, print an error message to stderr and exit 1.

### `myapp tui --file <path>`

Launch the interactive TUI (described below).

## TUI Mode

### Layout

The TUI has two panels and a status bar:

- **Timer panel** (upper area): Lists all timers with their remaining time and state
- **Log panel** (lower area): Shows recent timer event messages
- **Status bar** (bottom line): Shows `N timers | Focus: Timers` or `N timers | Focus: Log`

The Timer panel is focused by default when the TUI starts.

### Timer Display

Each timer in the timer panel shows its name, remaining time as MM:SS, and state. The currently
selected timer is visually highlighted (e.g., reverse video, `>` prefix, or color). Example:

```text
  Eggs       03:00  READY
> Tea        01:45  RUNNING
  Pasta      00:00  DONE
```

Timer states are:

- `READY`: not yet started, showing the full preset duration
- `RUNNING`: actively counting down, remaining time decreases by one second automatically
- `PAUSED`: countdown stopped mid-way, remaining time preserved
- `DONE`: remaining time reached 00:00

When a RUNNING timer's remaining time reaches 00:00, its state changes to DONE.

### Focus Switching

Pressing `Tab` switches focus between the Timer panel and the Log panel. The status bar reflects
which panel is currently focused by showing `Focus: Timers` or `Focus: Log`.

### Timer Panel Keys (when Timer panel is focused)

- `j` or Down arrow: move selection down
- `k` or Up arrow: move selection up
- `space`: toggle the selected timer (READY to RUNNING, RUNNING to PAUSED, PAUSED to RUNNING; no
  effect on DONE)
- `r`: reset the selected timer to its original duration and set state to READY
- `d`: show delete confirmation overlay
- Selection does not wrap around

These keys have no effect when the Log panel is focused.

### Log Panel Keys (when Log panel is focused)

- `j` or Down arrow: scroll log down
- `k` or Up arrow: scroll log up
- `c`: clear all log entries

These keys have their normal Timer-panel meaning when the Timer panel is focused.

### Log Events

The log panel shows event messages. Events are added when:

- A timer is started: `Started: <name>`
- A timer is paused: `Paused: <name>`
- A timer is reset: `Reset: <name>`
- A timer completes (reaches 00:00): `Done: <name>`

The most recent event appears at the bottom.

### Index Overlay

Pressing `i` shows an overlay listing all timers with their current state. The overlay must contain
the text `Index` as a title or heading. Each entry shows the timer name and its current state.
Pressing `escape` dismisses the overlay.

### Help Overlay

Pressing `?` shows an overlay listing all keybindings. The overlay must contain the text `Help` as a
title or heading. Pressing `escape` dismisses the overlay.

### Delete Confirmation Overlay

Pressing `d` (when Timer panel is focused) shows an overlay asking for confirmation. The overlay
must contain the text `Delete`. Pressing `y` removes the timer preset, saves the file, and closes
the overlay. Pressing `n` or `escape` cancels without deleting.

After deletion, if there are remaining timers, the selection moves to the nearest valid position.

## Error Handling

- Missing `--file` flag: print usage to stderr, exit 1
- Unknown subcommand: print error to stderr, exit 1
- `validate` with malformed JSON: print error describing the problem to stderr, exit 1

## Requirements

- Pure stdin/stdout terminal application; no network required
- The built binary must be available in PATH inside the container
- Timers count down automatically every second while in RUNNING state; the display updates without
  requiring any keypress
- Changes from `d` (delete) are persisted to the JSON file immediately
- Any programming language
