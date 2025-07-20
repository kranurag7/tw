# daemon-check-output

A Go implementation of the daemon-check-output functionality, migrated from the shell-based pipeline.

## Overview

This tool starts a daemon process and monitors its output for expected patterns. It can run setup scripts before starting the daemon and post scripts after monitoring completes.

## Features

- Start and monitor daemon processes
- Pattern matching in daemon output using regular expressions
- Error pattern detection
- Setup and post script execution
- Configurable timeout
- Graceful process termination (SIGTERM then SIGKILL)

## Usage

```bash
daemon-check-output \
  --start "your-daemon-command" \
  --expected-output "pattern1\npattern2" \
  --timeout 30 \
  --setup "echo 'Setting up...'" \
  --post "echo 'Cleaning up...'" \
  --error-strings "ERROR\nFAIL"
```

## Flags

- `--start`: Command to start the daemon (required)
- `--expected-output`: Newline separated patterns to find in output (required)
- `--timeout`: Timeout in seconds to wait for expected output (default: 30)
- `--setup`: Setup script to run before starting daemon (optional)
- `--post`: Post script to run after monitoring (optional)
- `--error-strings`: Newline separated error patterns (default includes common error patterns)

## Building

```bash
make build
```

## Integration

This tool is integrated into the main `tw` multicall binary and can be called as:

```bash
tw daemon-check-output [flags]
```

Or as a standalone binary:

```bash
daemon-check-output [flags]
```
