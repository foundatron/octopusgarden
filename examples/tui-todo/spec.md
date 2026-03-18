# Todo Manager

A terminal todo list manager with CLI subcommands for batch operations and an interactive TUI for
browsing and editing tasks.

## Data Format

Tasks are stored in a JSON file as an array of objects. Each task has:

- `text` (string): the task description
- `done` (boolean): completion status, defaults to false when added

Example file contents:

```json
[
  {"text": "Buy milk", "done": false},
  {"text": "Write tests", "done": true}
]
```

If the file does not exist, CLI subcommands that read it treat it as an empty list. The `add`
subcommand creates the file if it does not exist.

## CLI Subcommands

The binary is named `myapp`. All subcommands take `--file <path>` to specify the JSON file path. The
`--file` flag is required for all subcommands.

### `myapp add --file <path> <text>`

Add a new task with the given text. The task starts as not done. Prints nothing on success and exits
with code 0.

Validation: if `<text>` is empty or missing, print an error to stderr and exit with code 1.

### `myapp list --file <path>`

Print all tasks to stdout, one per line, in the format:

```text
[ ] Buy milk
[x] Write tests
```

`[x]` indicates done, `[ ]` indicates not done. If there are no tasks, print nothing and exit 0.

### `myapp validate --file <path>`

Check that the file contains valid JSON in the expected format. On success, print `ok: N tasks` to
stdout (where N is the count) and exit 0. On failure, print an error message to stderr and exit 1.

### `myapp tui --file <path>`

Launch the interactive TUI (described below).

## TUI Mode

### Display

The TUI shows a scrollable task list. Each task is displayed as:

```text
[ ] Buy milk
[x] Write tests
```

The currently selected task is visually highlighted (e.g., reverse video, `>` prefix, or color). A
status bar at the bottom shows `N tasks (M done)`.

### Navigation

- `j` or Down arrow: move selection down
- `k` or Up arrow: move selection up
- Selection does not wrap around

### Actions

- `space`: toggle the selected task's done status and save to the file immediately
- `d`: show a delete confirmation overlay
- `?`: show a help overlay listing all keybindings
- `q`: quit with exit code 0

### Help Overlay

Pressing `?` shows an overlay on top of the task list displaying the available keybindings. The
overlay must contain the text `Help` as a title or heading. Pressing `?` again or `escape` dismisses
the overlay and returns to the normal task list view.

### Delete Confirmation Overlay

Pressing `d` shows an overlay asking for confirmation. The overlay must contain the text `Delete`.
Pressing `y` deletes the selected task, saves the file, and closes the overlay. Pressing `n` or
`escape` cancels and closes the overlay without deleting.

After deletion, if there are remaining tasks, the selection moves to the nearest valid position.

## Error Handling

- Missing `--file` flag: print usage to stderr, exit 1
- Unknown subcommand: print error to stderr, exit 1
- `validate` with malformed JSON: print error describing the problem to stderr, exit 1

## Requirements

- Pure stdin/stdout terminal application; no network required
- The built binary must be available in PATH inside the container
- Changes from `space` (toggle) and `d` (delete) are persisted to the JSON file immediately
- Any programming language
