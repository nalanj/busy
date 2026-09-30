# AADC - Autonomous Agent Daemon with Cron

## Overview

AADC runs AI agents as standalone processes, each with their own scheduled jobs defined in a config file. No inter-agent messaging - each agent is independent.

## Usage

```bash
aadc --config /path/to/agent.yaml
```

## Config Format (YAML)

```yaml
agent:
  name: "my-agent"
  # Providers loaded from the [models.dev] catalog (anthropic, openai, azure, bedrock, deepseek, gemini, groq, fireworks, openrouter, etc.) at startup
  provider: "anthropic"
  model: "claude-sonnet-4-20250514"
  system: "You are a helpful assistant."
  thinking: "low"  # or disabled/low/medium/high/very_high/max, or a number
  state_dir: "~/.local/share/aadc"  # optional, defaults to ~/.local/share/aadc
  skills_dir: "~/.config/aadc/skills"  # optional
  listen_addr: "127.0.0.1:8080"  # optional, enables web UI
  path_prefix: "/agent1"  # optional, for path-based reverse-proxy routing (Caddy)

  compaction:
    retain_tokens: 20000

jobs:
  - name: "daily-report"
    schedule: "0 9 * * *"  # cron format (6 fields with seconds), or @every Xs/Xm/Xh, or @once
    preconditions:
      - "command -v generate_report >/dev/null"

    prompt: |
      Run the generate_report script and summarize the output.
```

## Schedule Formats

- **Cron**: `0 0 9 * * *` (6 fields with seconds: sec min hour day month dow)
- **Interval**: `@every 30s`, `@every 5m`, `@every 1h`
- **One-shot**: `@once`
- **Session-start**: `@session-start` - runs once on first start with setup prompt

## Thinking Budget

| Level | Tokens |
|-------|--------|
| `disabled` | 0 |
| `low` | 10,000 |
| `medium` | 16,000 |
| `high` | 32,000 |
| `very_high` | 64,000 |
| `max` | 100,000 |

Or specify directly: `thinking: 20000`

## Preconditions

Jobs can have shell commands that must pass before execution:

```yaml
jobs:
  - name: "review-changes"
    schedule: "@every 30s"
    preconditions:
      - "command -v changes_to_review >/dev/null"
      - "[ -d /home/user/repo ]"
    prompt: "..."
```

## Tools

Agents have access to the following tools:

| Tool | Description |
|------|-------------|
| `read_file` | Read the contents of a file |
| `edit_file` | Write or append content to a file |
| `bash` | Run a shell command |
| `glob` | List files matching a glob pattern |
| `list_dir` | List contents of a directory |

## Done Marker

Agents signal completion by including `<<<<<DONE>>>>>` in their response.

## Skills

Skills are loaded from a directory specified by `skills_dir` in the agent config. Each skill is a subdirectory containing a `SKILL.md` file.

### Skill Format

```markdown
---
name: golang-testing
description: Best practices for Go testing
---

# Go Testing Guide

This skill covers...
```

### Loading Skills

Skills are loaded from subdirectories in the skills directory:
```
~/.config/aadc/skills/
├── golang-testing/
│   └── SKILL.md
├── bash-scripting/
│   └── SKILL.md
└── git-workflow/
    └── SKILL.md
```

When a skill is loaded, its content is included in the agent's system prompt with a reference to the skill file location. The agent is instructed to read skill files when a task matches their purpose.

## Web UI

When `listen_addr` is configured, AADC starts a simple web server displaying the current session:

- Session metadata (number, message count, last updated)
- Job queue status and pending jobs
- All messages with roles color-coded
- Compaction markers showing when sessions were compacted
- Long content is truncated (2000 chars) with total length shown

When `path_prefix` is set, AADC serves its UI under that prefix (e.g. `/agent1/style.css`).
This lets multiple agents share one hostname via a Caddy-style reverse proxy that
routes by path. The server strips the prefix from incoming requests, and emits
prefix-aware URLs in its HTML/JS so browser-side navigation keeps working.

## Docker

AADC runs in a Docker container for isolation:

```bash
# Build everything
mise run docker-build

# Run an agent (agent name comes from config file)
mise run docker-run --config config/my-agent.yaml
```

### Docker Setup

The container is **fully isolated** from the host:
- No access to host filesystem (except config file mounted read-only)
- No SSH keys, git credentials, or other secrets
- Agent starts as root and can install packages freely

The only mounted items are:
- Config file (read-only)
- State directory for session persistence

Environment variables like `ANTHROPIC_API_KEY` are passed through.

## Project Structure

```
cmd/aadc/main.go      # Entry point
internal/
  config/             # YAML config loading
  agent/              # Agent runner and manual loop on top of github.com/nalanj/sorus
  scheduler/          # Cron scheduling
```

## Session Persistence

Sessions are persisted to JSONL files for each agent:

```
~/.local/share/aadc/{agent_name}/
├── meta.json           # Session metadata
├── session_001.jsonl   # First session
├── session_002.jsonl   # After first compaction
├── job_queue.jsonl     # Pending jobs (FIFO queue)
└── {agent_name}/       # Agent state subdirectory
    └── ...
```

### Job Queue

Jobs are queued and processed one at a time:
- When a scheduled job triggers, it's added to the queue
- If a job is already running, new triggers are re-queued
- The worker processes jobs FIFO from the queue
- Queue state is visible in the web UI

### Compaction

Compaction is triggered when the provider reports `context_too_large`. It:
1. Generates a summary of the conversation using the LLM
2. Starts a new session file, retaining complete user/assistant pairs
3. Keeps pairs up to `retain_tokens` (default: 20,000), estimated as ~4 chars per token
4. Never splits tool calls from their results
- Uses YAML instead of markdown
