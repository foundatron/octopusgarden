# Runbook Viewer

A terminal viewer for operational runbooks written in markdown. Displays formatted content with
executable command blocks, configurable themes, and a command output panel.

## Input Format

A runbook is a single markdown file. Steps are separated by lines containing only `---`. Each step
begins with a heading (`##` or `#`) that serves as the step title.

Markdown formatting within each step:

- Headings (`#`, `##`, `###`)
- Paragraphs of text
- Unordered lists (`-` or `*` items)
- Bold text (`**text**`)
- Inline code (`` `text` ``)
- Fenced code blocks with language tags

Fenced code blocks tagged with `sh` or `bash` are executable. Code blocks with any other language
tag or no tag are display-only.

Example runbook:

````text
## Check Environment

Verify your tools are ready:

- Docker must be installed
- kubectl must be configured

```sh
echo "tools ready"
```

---

## Deploy

Run the deployment command:

```sh
echo "deploying..."
```
````

If the file has no `---` separators, the entire content is a single step.

## CLI Subcommands

The binary is named `myapp`.

### `myapp view <file> [--theme <name>]`

Launch the interactive TUI viewer for the given runbook file. The optional `--theme` flag sets the
initial theme (default: `dark`).

### `myapp list <file>`

Print step titles to stdout, one per line, numbered:

```text
1. Check Environment
2. Deploy
```

The title is the first heading in each step. If a step has no heading, use `Step N` as the title. If
there are no steps, print nothing and exit 0.

### `myapp validate <file>`

Check that the file exists and can be parsed. On success, print `ok: N steps` to stdout (where N is
the count) and exit 0. On failure (file not found or unreadable), print an error to stderr and exit
1\.

### `myapp themes`

Print available theme names to stdout, one per line. Exit 0. At minimum the output includes `dark`,
`light`, and `ocean`.

### `myapp run <file> --step <n>`

Run all executable code blocks in step N (1-based) non-interactively. Print each block's combined
stdout and stderr output to stdout. Exit with the exit code of the last executed block, or 0 if the
step has no executable blocks.

If N is out of range, print an error to stderr and exit 1.

## TUI Mode

### Layout

- **Content area** (upper region): Renders the current step's markdown content with formatting
- **Output panel** (lower region, hidden by default): Shows command execution output
- **Status bar** (bottom line): Shows `Step N of M | Theme: <name>`

### Markdown Rendering

The content area renders markdown with visual formatting. At minimum:

- Headings are displayed prominently (bold, larger, or distinctly prefixed)
- Raw markdown syntax characters (`#` prefixes, `**` wrappers, `` ` `` wrappers) must not appear
  verbatim; the content is rendered, not shown as source
- List items are displayed as a formatted list
- Code blocks are visually distinct from surrounding text (indented, bordered, or labeled)

### Navigation

- Right arrow or `l`: advance to the next step
- Left arrow or `h`: go to the previous step
- Navigation does not wrap around
- `q`: quit with exit code 0

### Command Execution

- `x`: execute the next executable code block in the current step
  - First press runs the first executable block, second press runs the second, and so on
  - If no more executable blocks remain in the current step, `x` has no effect
  - Navigating to a different step resets the execution position for all steps
- While a command is running, the status bar appends `| Running...`
- After execution completes, the output panel becomes visible (if it was hidden) and shows the
  command's combined stdout and stderr output
- `o`: toggle the output panel visibility

### Themes

Three built-in themes are available:

- `dark` (default)
- `light`
- `ocean`

The current theme name appears in the status bar after `Theme:`.

- `t`: cycle to the next theme (dark then light then ocean then dark)
- `T` (shift-T): cycle to the previous theme

### Index Overlay

Pressing `g` shows an overlay listing all steps with their titles, numbered. The overlay must
contain the text `Index` as a title or heading. Pressing `escape` dismisses the overlay.

### Help Overlay

Pressing `?` shows an overlay listing all keybindings. The overlay must contain the text `Help` as a
title or heading. Pressing `escape` dismisses the overlay.

## Error Handling

- Missing file argument for `view`, `list`, `validate`, `run`: print usage to stderr, exit 1
- File not found or unreadable: print error to stderr, exit 1
- `run --step` with out-of-range N: print error to stderr, exit 1
- Unknown subcommand: print error to stderr, exit 1

## Requirements

- Pure stdin/stdout terminal application; no network required
- The built binary must be available in PATH inside the container
- Executable code blocks run as child processes with their combined stdout and stderr captured
- The output panel shows real command output, not the code block source text
- Any programming language
