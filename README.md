# OpenMarmut

AI-powered coding assistant that runs in your terminal. Multi-provider, multi-runtime, with project-aware intelligence. Ask questions, generate code, run commands, and manage files — all through natural language in a sandboxed environment.

## Key Features

- **Multi-provider LLM support** — OpenAI, Anthropic, Azure OpenAI, Azure Codex/Responses API, Google Gemini, Ollama, or any OpenAI-compatible endpoint
- **Dual runtime modes** — Local (host filesystem) or Docker (isolated container)
- **Interactive chat** — REPL with slash commands, plan mode, sub-agents, and background tasks
- **Project memory** — Learns your codebase via OPENMARMUT.md, rules, skills, and auto-memory
- **Agent teams** — Parallel execution with lead/worker coordination
- **MCP support** — Connect external tool servers via Model Context Protocol
- **Hooks system** — Shell and HTTP hooks triggered by tool events
- **Session management** — Persistent conversation history with resume

## Quick Start

```bash
# Install
go install github.com/marmutapp/openmarmut/cmd/openmarmut@latest

# Configure (minimal — OpenAI example)
cat > .openmarmut.yaml <<EOF
llm:
  active_provider: openai
  providers:
    - name: openai
      type: openai
      api_key: ${OPENAI_API_KEY}
      model: gpt-4o
EOF

# Start chatting
openmarmut chat
```

## Installation

### Binary download

Download the latest release from [GitHub Releases](https://github.com/marmutapp/openmarmut/releases).

Or use the install script:

```bash
curl -sSL https://raw.githubusercontent.com/marmutapp/openmarmut/main/install.sh | sh
```

### Go install

```bash
go install github.com/marmutapp/openmarmut/cmd/openmarmut@latest
```

### Build from source

```bash
git clone https://github.com/marmutapp/openmarmut.git
cd openmarmut
make build
```

## Configuration

Configuration is loaded from (highest to lowest priority):
1. CLI flags
2. Environment variables (`OPENMARMUT_*`)
3. Config file (`.openmarmut.yaml` in target dir, or `~/.config/openmarmut/config.yaml`)
4. Defaults

### Provider examples

**OpenAI:**
```yaml
llm:
  active_provider: openai
  providers:
    - name: openai
      type: openai
      api_key: ${OPENAI_API_KEY}
      model: gpt-4o
```

**Azure OpenAI:**
```yaml
llm:
  active_provider: azure
  providers:
    - name: azure
      type: openai
      base_url: https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT
      api_key: ${AZURE_OPENAI_API_KEY}
      model: gpt-4o
```

**Azure Codex / Responses API:**
```yaml
llm:
  active_provider: azure-codex
  providers:
    - name: azure-codex
      type: responses
      base_url: https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT
      api_key: ${AZURE_OPENAI_API_KEY}
      model: codex-mini
```

**Anthropic:**
```yaml
llm:
  active_provider: anthropic
  providers:
    - name: anthropic
      type: anthropic
      api_key: ${ANTHROPIC_API_KEY}
      model: claude-sonnet-4-20250514
```

**Ollama (local):**
```yaml
llm:
  active_provider: ollama
  providers:
    - name: ollama
      type: ollama
      base_url: http://localhost:11434
      model: llama3
```

**Custom OpenAI-compatible endpoint:**
```yaml
llm:
  active_provider: custom
  providers:
    - name: custom
      type: custom
      base_url: https://your-endpoint.example.com/v1
      api_key: ${CUSTOM_API_KEY}
      model: your-model
```

## Usage

### One-shot ask

```bash
openmarmut ask "explain this error" < error.log
openmarmut ask "what does main.go do?"
```

### Interactive chat

```bash
openmarmut chat
```

### File operations

```bash
openmarmut read path/to/file.txt
echo "content" | openmarmut write path/to/file.txt
openmarmut delete path/to/file.txt
openmarmut ls src/
openmarmut mkdir path/to/dir
```

### Command execution

```bash
openmarmut exec "go test ./..."
```

### Provider management

```bash
openmarmut providers         # List configured providers
openmarmut providers test    # Test provider connectivity
```

## Project Memory

OpenMarmut learns about your project through several mechanisms:

- **OPENMARMUT.md** — Project-level instructions and context (checked into your repo)
- **Rules** — `.openmarmut/rules/*.md` files with specific coding guidelines
- **Skills** — `.openmarmut/commands/*.md` custom slash commands with YAML frontmatter
- **Auto-memory** — Automatically extracts and remembers important context from conversations (stored in `~/.openmarmut/memory/`)

## Docker Mode

Run commands in an isolated Docker container:

```bash
openmarmut -m docker --docker-image openmarmut-sandbox exec "ls -la"
```

Or configure in `.openmarmut.yaml`:

```yaml
mode: docker
docker:
  image: openmarmut-sandbox
  mount_path: /workspace
  network_mode: none
```

## Advanced Features

- **Plan mode** — `/plan` toggles structured planning before execution
- **Sub-agents** — `/agent <task>` spawns focused sub-agents for parallel work
- **Agent teams** — `/team <task>` coordinates multiple agents with lead/worker strategy
- **Background jobs** — `/bg <task>` runs tasks without blocking the chat
- **MCP servers** — Connect external tools via Model Context Protocol
- **Hooks** — Shell and HTTP hooks triggered by tool execution events
- **File references** — `@file.go` syntax to include file contents in messages
- **Image input** — Attach images for vision-capable models

## Slash Commands

| Command | Description |
|---------|-------------|
| `/clear` | Reset conversation history |
| `/compact [instr]` | Compact conversation history |
| `/rename <name>` | Rename current session |
| `/sessions` | List recent sessions |
| `/rules` | Show loaded rules |
| `/skill [name]` | List skills or invoke one |
| `/memory` | Show stored memories |
| `/memory add <text>` | Add a memory entry |
| `/memory clear` | Clear all memories |
| `/ignore` | Show ignore patterns |
| `/commands` | List custom commands |
| `/diff [file]` | Show uncommitted changes |
| `/commit [msg]` | Commit changes |
| `/rewind [n]` | Undo last N turns of file changes |
| `/pr` | Show PR status |
| `/plan [msg]` | Toggle plan mode |
| `/agent <task>` | Spawn a sub-agent |
| `/agents` | List sub-agents |
| `/bg <task>` | Run task in background |
| `/team <task>` | Parallel agent team |
| `/btw <question>` | Quick side question |
| `/tools` | List available tools |
| `/hooks` | List configured hooks |
| `/mcp` | Show MCP servers |
| `/loop <int> <cmd>` | Run command on interval |
| `/cost` | Show session cost |
| `/context` | Show context usage |
| `/model [name]` | Show or switch model |
| `/thinking` | Toggle extended thinking |
| `/effort <level>` | Set thinking effort level |
| `/tasks` | Show tracked tasks |
| `/provider` | Manage LLM provider |
| `/version` | Show version info |
| `/help` | Show help |
| `/quit` | Exit chat |

## Configuration Reference

All `.openmarmut.yaml` fields:

```yaml
mode: local                    # "local" or "docker"
target_dir: .                  # Target project directory
default_timeout: 30s           # Default command timeout

log:
  level: info                  # debug, info, warn, error
  format: text                 # text, json

docker:
  image: openmarmut-sandbox    # Docker image name
  mount_path: /workspace       # Container mount point
  shell: /bin/sh               # Shell for exec commands
  network_mode: none           # Docker network mode
  memory: 512m                 # Memory limit
  cpus: "2"                    # CPU limit
  extra_volumes: []            # Additional bind mounts
  env_vars: []                 # Environment variables for container

llm:
  active_provider: openai      # Which provider to use
  default_temperature: 0.7     # Sampling temperature (0.0-2.0)
  default_max_tokens: 4096     # Max response tokens
  default_timeout: 60s         # LLM request timeout
  providers:
    - name: openai
      type: openai             # openai, anthropic, gemini, ollama, responses, custom
      api_key: ${OPENAI_API_KEY}
      base_url: ""             # Override API base URL
      model: gpt-4o

agent:
  auto_allow: []               # Tools that skip confirmation
  confirm: []                  # Tools that always require confirmation
  context_window: 0            # Override context window size (0 = provider default)
  truncation_threshold: 0.80   # Fraction triggering history truncation
  keep_recent_turns: 4         # Minimum recent turns to preserve
  session_retention_days: 30   # Days to keep sessions
  auto_memory: true            # Enable auto-memory extraction
  memory_file: ""              # Custom MEMORY.md path
  plan_provider: ""            # Provider for plan mode (empty = active)
  team:
    max_members: 3             # Max parallel agents
    lead_provider: ""          # Provider for lead agent
    worker_provider: ""        # Provider for workers
    strategy: parallel         # "parallel" or "sequential"

mcp:
  servers:
    - name: example
      command: /path/to/server
      args: ["--flag"]
      env:
        KEY: value

hooks:
  - name: on-write
    event: tool:after          # tool:before, tool:after
    tools: [write_file]        # Which tools trigger this hook
    type: shell                # "shell" or "http"
    command: "make lint"       # Shell command to run
    timeout: 10s
    on_error: warn             # "warn", "block", or "ignore"
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OPENMARMUT_MODE` | Runtime mode (`local` or `docker`) |
| `OPENMARMUT_TARGET_DIR` | Target project directory |
| `OPENMARMUT_LOG_LEVEL` | Log level (`debug`, `info`, `warn`, `error`) |
| `OPENMARMUT_LOG_FORMAT` | Log format (`text` or `json`) |
| `OPENMARMUT_LLM_PROVIDER` | Active LLM provider name |
| `OPENMARMUT_LLM_MODEL` | Override model for active provider |
| `OPENMARMUT_LLM_API_KEY` | Override API key for active provider |
| `OPENMARMUT_DOCKER_IMAGE` | Docker image name |

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/my-feature`)
3. Make your changes with tests
4. Run `make test && make lint`
5. Commit with [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, etc.)
6. Open a pull request

## License

MIT License — Copyright (c) 2026 [Gaja AI Private Limited](https://gaja.ai)

See [LICENSE](LICENSE) for details.
