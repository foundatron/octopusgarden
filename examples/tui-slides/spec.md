# Slide Viewer

A terminal slide viewer that reads a plain-text slide file and displays one slide at a time.

## Input Format

The application takes a single CLI argument: the path to a slide file. Slides in the file are
separated by a line containing only `---`. Leading and trailing blank lines within each slide are
preserved. An empty file produces one empty slide.

## Display

The application renders a full-screen TUI with:

- The current slide content occupying the main area
- A status bar at the bottom showing `Slide N of M` where N is the 1-based current slide number and
  M is the total number of slides

## Navigation

- Right arrow or `l`: advance to the next slide
- Left arrow or `h`: go back to the previous slide
- `q`: quit the application with exit code 0

Navigation does not wrap around. Pressing right on the last slide or left on the first slide has no
effect.

## Error Handling

- If no file argument is provided, print a usage message to stderr and exit with code 1
- If the file does not exist or cannot be read, print an error to stderr and exit with code 1

## Requirements

- Pure stdin/stdout terminal application; no network required
- The built binary must be available in PATH inside the container
- Any programming language
