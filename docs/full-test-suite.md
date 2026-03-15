# OpenMarmut Full Test Suite

**Covers every feature across all phases (1–12)**
**Platform:** Ubuntu 22.04+, Go 1.22+, Azure OpenAI endpoint
**Last verified:** 2026-03-15

---

## Prerequisites

```bash
# Go 1.22+
go version  # expected: go1.22.x or higher

# Build the binary
cd /path/to/opencode-go
go build -o openmarmut ./cmd/openmarmut

# Run all unit tests (must all pass before manual testing)
go test ./...
# Expected: all 19 packages ok

# Docker (for Docker mode tests only)
docker version  # expected: Docker Engine running

# Azure OpenAI config — create .openmarmut.yaml in test directory
cat > /tmp/test-project/.openmarmut.yaml << 'YAMLEOF'
mode: local
target: /tmp/test-project

llm:
  active_provider: azure
  providers:
    - name: azure
      type: openai
      endpoint: https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT/chat/completions?api-version=2024-12-01-preview
      auth:
        api_key: env:AZURE_OPENAI_API_KEY
      model: gpt-4o
      context_window: 128000

agent:
  auto_memory: true
  session_retention_days: 30
YAMLEOF

# Set the API key
export AZURE_OPENAI_API_KEY="your-key-here"
```

---

## 1. CORE RUNTIME

### 1.1 Local Mode — File Operations

```bash
# Setup
mkdir -p /tmp/test-project && cd /tmp/test-project

# Write a file
echo "hello world" | ./openmarmut write test.txt
# Expected: no error, file created

# Read a file
./openmarmut read test.txt
# Expected: "hello world"

# List directory
./openmarmut ls .
# Expected: table with test.txt, permissions, size (human-readable), mod time

# Make directory
./openmarmut mkdir subdir
# Expected: no error
./openmarmut ls .
# Expected: subdir/ and test.txt listed

# Write into subdirectory
echo "nested content" | ./openmarmut write subdir/nested.txt
./openmarmut read subdir/nested.txt
# Expected: "nested content"

# Delete a file
./openmarmut delete test.txt
# Expected: no error
./openmarmut read test.txt
# Expected: error — file not found
```

### 1.2 Local Mode — Exec

```bash
cd /tmp/test-project

# Simple command
./openmarmut exec -- echo "hello from exec"
# Expected: stdout: "hello from exec", exit code: 0

# Exit code forwarding
./openmarmut exec -- bash -c "exit 42"
# Expected: exit code: 42 (NOT an error)

# Command with stdin/stdout
./openmarmut exec -- ls -la
# Expected: directory listing of /tmp/test-project
```

### 1.3 Path Escape Prevention

```bash
cd /tmp/test-project

# Attempt path traversal — read
./openmarmut read ../../../etc/passwd
# Expected: error containing "path escape" or "outside target"

# Attempt path traversal — write
echo "bad" | ./openmarmut write ../../etc/evil.txt
# Expected: error containing "path escape" or "outside target"

# Attempt path traversal — delete
./openmarmut delete ../../../tmp/something
# Expected: error containing "path escape" or "outside target"

# Symlink escape (if symlinks exist)
ln -s /etc/passwd /tmp/test-project/link-to-passwd 2>/dev/null
./openmarmut read link-to-passwd
# Expected: error — symlink resolves outside target
rm -f /tmp/test-project/link-to-passwd
```

### 1.4 Atomic Writes

```bash
cd /tmp/test-project

# Write a file and verify atomicity (no partial writes visible)
echo "atomic content" | ./openmarmut write atomic-test.txt
./openmarmut read atomic-test.txt
# Expected: "atomic content" — complete content, never partial

# Overwrite existing file
echo "updated content" | ./openmarmut write atomic-test.txt
./openmarmut read atomic-test.txt
# Expected: "updated content"
```

### 1.5 Docker Mode — File Operations

> Requires Docker running. Tests same operations through container.

```bash
cd /tmp/test-project

# Write a file in Docker mode
echo "docker content" | ./openmarmut -m docker write docker-test.txt
# Expected: no error, file written inside container

# Read file from Docker
./openmarmut -m docker read docker-test.txt
# Expected: "docker content"

# List directory in Docker
./openmarmut -m docker ls .
# Expected: directory listing from container's /workspace

# Exec in Docker
./openmarmut -m docker exec -- uname -a
# Expected: Linux kernel info from container

# Delete in Docker
./openmarmut -m docker delete docker-test.txt
./openmarmut -m docker read docker-test.txt
# Expected: error — file not found

# Info command shows Docker details
./openmarmut -m docker info
# Expected: styled box with Runtime: docker, container ID, image, mount point
```

### 1.6 Docker Mode — Exit Code Forwarding

```bash
cd /tmp/test-project

./openmarmut -m docker exec -- bash -c "exit 7"
# Expected: exit code: 7 (not treated as error)
```

---

## 2. LLM PROVIDERS

### 2.1 Providers List

```bash
cd /tmp/test-project

# List providers (with config from prerequisites)
./openmarmut providers
# Expected: styled table with:
#   ★ azure | openai | gpt-4o | https://YOUR-RESOURCE...
# Active provider marked with ★
```

### 2.2 Multi-Provider Config

```bash
# Create a config with multiple providers
cat > /tmp/test-project/.openmarmut.yaml << 'YAMLEOF'
mode: local
llm:
  active_provider: azure
  providers:
    - name: azure
      type: openai
      endpoint: https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT/chat/completions?api-version=2024-12-01-preview
      auth:
        api_key: env:AZURE_OPENAI_API_KEY
      model: gpt-4o
    - name: local-ollama
      type: ollama
      endpoint: http://localhost:11434
      model: llama3
YAMLEOF

./openmarmut providers
# Expected: two rows — azure (★ active) and local-ollama
```

### 2.3 Ask Command with Active Provider

```bash
cd /tmp/test-project

./openmarmut ask "What is 2+2? Reply with just the number."
# Expected: "4" (or similar), followed by summary line:
# [tokens │ ~$cost │ duration]
```

### 2.4 Provider Switching via --provider Flag

```bash
cd /tmp/test-project

# Use a specific provider
./openmarmut ask --provider azure "Say hello"
# Expected: response from azure provider

# Non-existent provider
./openmarmut ask --provider nonexistent "hello"
# Expected: error — provider "nonexistent" not found
```

### 2.5 Environment Variable Overrides

```bash
cd /tmp/test-project

# Override model via env
OPENMARMUT_LLM_MODEL=gpt-4o-mini ./openmarmut ask "Say hi"
# Expected: uses gpt-4o-mini model

# Override provider via env
OPENMARMUT_LLM_PROVIDER=azure ./openmarmut ask "Say hi"
# Expected: uses azure provider
```

### 2.6 Error Cases

```bash
cd /tmp/test-project

# Missing API key
AZURE_OPENAI_API_KEY="" ./openmarmut ask "hello"
# Expected: error about missing credentials or authentication failure

# Bad provider name
./openmarmut ask --provider does-not-exist "hello"
# Expected: error — provider not found

# Unreachable endpoint
cat > /tmp/test-bad/.openmarmut.yaml << 'YAMLEOF'
mode: local
llm:
  providers:
    - name: bad
      type: openai
      endpoint: https://localhost:1/v1/chat/completions
      auth:
        api_key: fake
      model: test
YAMLEOF
./openmarmut -t /tmp/test-bad ask --provider bad "hello"
# Expected: connection error (after retries)
```

---

## 3. AGENT LOOP

### 3.1 Tool Calling

```bash
cd /tmp/test-project

# Create test files for the agent to work with
echo "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}" > main.go
mkdir -p src
echo "test data" > src/data.txt

# Ask agent to read a file (uses read_file tool)
./openmarmut ask "Read the file main.go and tell me what language it is"
# Expected: mentions Go, shows tool call [read_file]

# Ask agent to find files (uses find_files tool)
./openmarmut ask "Find all .txt files in this project"
# Expected: shows find_files tool call, lists src/data.txt

# Ask agent to grep (uses grep_files tool)
./openmarmut ask "Search for the word 'hello' in all files"
# Expected: shows grep_files tool call, finds match in main.go

# Ask agent to write a file (uses write_file tool)
./openmarmut ask --auto-approve "Create a file called greeting.txt containing 'Hello World'"
# Expected: shows write_file tool call, file created
cat greeting.txt
# Expected: Hello World

# Ask agent to patch a file (uses patch_file tool)
./openmarmut ask --auto-approve "In main.go, change 'hello' to 'goodbye'"
# Expected: shows patch_file tool call, file modified
cat main.go
# Expected: println("goodbye")
```

### 3.2 Permission System

```bash
cd /tmp/test-project

# Without --auto-approve, write tools require confirmation
./openmarmut chat << 'EOF'
Create a file called perm-test.txt with "test"
n
/quit
EOF
# Expected: shows permission prompt for write_file, denied with 'n', file NOT created

# With --auto-approve, no prompts
./openmarmut ask --auto-approve "Create a file called auto-test.txt with 'auto'"
# Expected: no prompt, file created directly
```

### 3.3 Git Tools

```bash
cd /tmp/test-project
git init
git add -A && git commit -m "initial"

echo "new line" >> main.go

# Ask about git status
./openmarmut ask "What files have been modified?"
# Expected: uses git_status tool, reports main.go modified

# Ask about diff
./openmarmut ask "Show me the diff"
# Expected: uses git_diff tool, shows the added line
```

### 3.4 Max Iterations

```bash
cd /tmp/test-project

# The agent loop has a max iteration limit (default 20)
# This prevents infinite tool-calling loops
# Verified by unit tests — not easily testable via CLI
# See: internal/agent/agent_test.go TestAgent_MaxIterations
go test ./internal/agent/ -run TestAgent_MaxIterations -v
# Expected: PASS
```

---

## 4. CHAT REPL

### 4.1 Slash Commands

```bash
cd /tmp/test-project

# Start chat and test each slash command
# Note: interactive tests — run manually, verify output

# /help — shows all commands in a styled box
./openmarmut chat << 'EOF'
/help
/quit
EOF
# Expected: styled box listing all slash commands with descriptions

# /tools — list available tools
./openmarmut chat << 'EOF'
/tools
/quit
EOF
# Expected: styled table with tool names, permission levels (auto/confirm), descriptions

# /rules — show loaded rules
./openmarmut chat << 'EOF'
/rules
/quit
EOF
# Expected: "No rules loaded" or list of rules with glob patterns

# /cost — show session cost
./openmarmut chat << 'EOF'
/cost
/quit
EOF
# Expected: styled box with 0 tokens, $0.000 cost

# /context — show context window usage
./openmarmut chat << 'EOF'
/context
/quit
EOF
# Expected: styled box with model window, usage %, turns, progress bar

# /clear — reset conversation
./openmarmut chat << 'EOF'
/clear
/quit
EOF
# Expected: "✓ Conversation cleared"

# /compact — summarize history
./openmarmut chat << 'EOF'
Tell me a joke
/compact
/quit
EOF
# Expected: shows before/after token counts

# /plan — toggle plan mode
./openmarmut chat << 'EOF'
/plan
/plan off
/quit
EOF
# Expected: "Plan mode: ON" then "Plan mode: OFF"

# /diff — show git diff
./openmarmut chat << 'EOF'
/diff
/quit
EOF
# Expected: git diff output or "no changes"

# /commit — commit with message
./openmarmut chat << 'EOF'
/commit test commit
n
/quit
EOF
# Expected: shows commit confirmation prompt

# /rewind — show/restore checkpoints
./openmarmut chat << 'EOF'
/rewind --list
/quit
EOF
# Expected: "No checkpoints" or list of checkpoints

# /tasks — task management
./openmarmut chat << 'EOF'
/tasks
/tasks add Build feature X
/tasks
/tasks done 1
/tasks
/tasks clear
/quit
EOF
# Expected: tasks created, completed, cleared with styled output

# /memory — memory management
./openmarmut chat << 'EOF'
/memory
/memory add Test memory entry
/memory
/memory off
/quit
EOF
# Expected: shows memories, adds entry, disables auto-memory

# /ignore — ignore patterns
./openmarmut chat << 'EOF'
/ignore
/quit
EOF
# Expected: list of ignore patterns with sources (defaults, .gitignore, .openmarmutignore)

# /agents — sub-agent listing
./openmarmut chat << 'EOF'
/agents
/quit
EOF
# Expected: "No sub-agents" or list

# /model — show/switch model
./openmarmut chat << 'EOF'
/model
/quit
EOF
# Expected: shows current provider and model

# /effort — reasoning effort
./openmarmut chat << 'EOF'
/effort
/effort high
/quit
EOF
# Expected: shows current effort, then sets to high

# /btw — side question
./openmarmut chat << 'EOF'
/btw What is the capital of France?
/quit
EOF
# Expected: styled btw box with answer, no history pollution

# /bg — background execution
./openmarmut chat << 'EOF'
/bg status
/quit
EOF
# Expected: "No background jobs" or status list

# /commands — custom commands
./openmarmut chat << 'EOF'
/commands
/quit
EOF
# Expected: "No custom commands" or list

# /mcp — MCP servers
./openmarmut chat << 'EOF'
/mcp
/quit
EOF
# Expected: "No MCP servers" or list

# /loop — recurring tasks
./openmarmut chat << 'EOF'
/loop status
/quit
EOF
# Expected: "No active loops"

# /skill — skills
./openmarmut chat << 'EOF'
/skill
/quit
EOF
# Expected: "No skills loaded" or list
```

### 4.2 Welcome Banner

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/quit
EOF
# Expected: branded welcome box showing:
#   - Provider/model info
#   - Target directory
#   - Runtime mode (local/docker)
#   - Instructions status (OPENMARMUT.md loaded or not)
#   - Custom commands count
#   - MCP server status
```

### 4.3 Spinner and Streaming

```bash
cd /tmp/test-project

# Interactive test — observe visually
./openmarmut chat
# Type: "Tell me a short joke"
# Expected: "Thinking..." spinner appears, then streamed response token-by-token
# Then: summary line with tokens/cost/duration/context%
# Type: /quit
```

### 4.4 Markdown Rendering

```bash
cd /tmp/test-project

./openmarmut chat
# Type: "Show me a markdown example with a code block, a list, and a header"
# Expected: rendered with colors/formatting via glamour
# Type: /quit
```

---

## 5. SESSION PERSISTENCE

### 5.1 Create and Resume Session

```bash
cd /tmp/test-project

# Start a named session
./openmarmut chat --name "test-session" << 'EOF'
Remember that my favorite color is blue
/quit
EOF
# Expected: session auto-saved

# Resume with --continue (most recent session for this directory)
./openmarmut chat --continue << 'EOF'
What is my favorite color?
/quit
EOF
# Expected: resumes session, agent remembers "blue"
# Shows resume banner with session info
```

### 5.2 Resume with --resume (Picker)

```bash
cd /tmp/test-project

# List sessions
./openmarmut sessions
# Expected: table with session ID, name, target, provider, last used, turns

# Resume specific session
./openmarmut chat --resume <session-id-from-above>
# Type: /quit
# Expected: loads that specific session
```

### 5.3 Session List and Delete

```bash
cd /tmp/test-project

# List all sessions
./openmarmut sessions
# Expected: styled table of sessions

# List sessions for specific target
./openmarmut sessions --target /tmp/test-project
# Expected: filtered list

# Delete a session
./openmarmut sessions delete <session-id>
# Expected: session removed

./openmarmut sessions
# Expected: deleted session no longer listed
```

### 5.4 Session Shows Correct Metadata

```bash
cd /tmp/test-project

./openmarmut sessions
# Expected: each session shows:
#   - Provider name
#   - Target directory
#   - Runtime mode
#   - Number of turns
#   - Last used timestamp
```

---

## 6. PROJECT MEMORY

### 6.1 OPENMARMUT.md Loading

```bash
cd /tmp/test-project

# Create project instructions
cat > /tmp/test-project/OPENMARMUT.md << 'EOF'
# Project Rules
- Always use snake_case for variables
- Never delete production data

@coding-standards.md
EOF

cat > /tmp/test-project/coding-standards.md << 'EOF'
# Coding Standards
- Max line length: 120
- Always add error handling
EOF

# Start chat — should show instructions loaded
./openmarmut chat << 'EOF'
/quit
EOF
# Expected: "Instructions: OPENMARMUT.md (N lines)" in startup

# Global instructions
mkdir -p ~/.openmarmut
echo "# Global: always be concise" > ~/.openmarmut/OPENMARMUT.md

./openmarmut chat << 'EOF'
/quit
EOF
# Expected: loads both global and project instructions
# Merge order: global → project

# Clean up global
rm ~/.openmarmut/OPENMARMUT.md
```

### 6.2 Rules System

```bash
cd /tmp/test-project

# Create a rule
mkdir -p .openmarmut/rules
cat > .openmarmut/rules/go-style.md << 'EOF'
---
globs: ["*.go"]
---
When editing Go files:
- Use gofumpt formatting
- Add error wrapping with fmt.Errorf
EOF

./openmarmut chat << 'EOF'
/rules
/quit
EOF
# Expected: shows go-style rule with globs: *.go
```

### 6.3 Skills System

```bash
cd /tmp/test-project

# Create a manual skill
mkdir -p .openmarmut/skills
cat > .openmarmut/skills/review.md << 'EOF'
---
description: Code review checklist
trigger: manual
---
Review the code for:
1. Security vulnerabilities
2. Performance issues
3. Error handling
EOF

./openmarmut chat << 'EOF'
/skill
/skill review
/quit
EOF
# Expected: /skill lists "review", /skill review shows skill content
```

### 6.4 Auto-Memory

```bash
cd /tmp/test-project

# Memory commands
./openmarmut chat << 'EOF'
/memory
/memory add This project uses PostgreSQL 15
/memory
/quit
EOF
# Expected: /memory shows entries, /memory add saves entry
# Entry stored in ~/.openmarmut/memory/MEMORY.md

cat ~/.openmarmut/memory/MEMORY.md
# Expected: contains "This project uses PostgreSQL 15" with date and project path

# Disable auto-memory
./openmarmut chat << 'EOF'
/memory off
/quit
EOF
# Expected: "Auto-memory disabled for this session"
```

### 6.5 Ignore System

```bash
cd /tmp/test-project

# Create .openmarmutignore
cat > .openmarmutignore << 'EOF'
*.log
build/
secret.txt
EOF

# Test that ignored files are hidden from tools
echo "secret" > secret.txt
echo "log data" > app.log
mkdir -p build && echo "binary" > build/output

./openmarmut chat << 'EOF'
/ignore
/quit
EOF
# Expected: shows patterns from:
#   - defaults (.git/, node_modules/, etc.)
#   - .gitignore (if present)
#   - .openmarmutignore (*.log, build/, secret.txt)

# Verify ls filtering
./openmarmut ask "List all files in the current directory"
# Expected: secret.txt, app.log, build/ NOT shown in list_dir output
# Should see "[+N hidden by .openmarmutignore]"

# Add/remove patterns
./openmarmut chat << 'EOF'
/ignore add *.tmp
/ignore
/ignore remove *.tmp
/quit
EOF
# Expected: pattern added then removed from .openmarmutignore
```

---

## 7. PLAN MODE

### 7.1 Plan Analysis and Approval

```bash
cd /tmp/test-project

echo "package main\n\nfunc add(a, b int) int { return a + b }" > calc.go

./openmarmut chat << 'EOF'
/plan Add a multiply function to calc.go
y
/quit
EOF
# Expected:
# 1. Agent analyzes using read-only tools only
# 2. Displays plan in blue-bordered box
# 3. Shows approval prompt: [y]es / [n]o / [e]dit
# 4. On 'y': executes the plan with write tools
```

### 7.2 Plan Toggle Mode

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/plan on
/plan off
/quit
EOF
# Expected: "Plan mode: ON" then "Plan mode: OFF"
```

### 7.3 Plan in Non-Interactive Mode

```bash
cd /tmp/test-project

./openmarmut ask --plan --auto-approve "Add a subtract function to calc.go"
# Expected: analyzes first, then executes
```

---

## 8. GIT INTEGRATION

### 8.1 Checkpoint Creation

```bash
cd /tmp/test-project
git init 2>/dev/null
git add -A && git commit -m "baseline" 2>/dev/null

./openmarmut chat --auto-approve << 'EOF'
Create a file called checkpoint-test.txt with "version 1"
Now update checkpoint-test.txt to say "version 2"
/rewind --list
/quit
EOF
# Expected: /rewind --list shows checkpoints for file changes
```

### 8.2 Rewind to Restore Files

```bash
cd /tmp/test-project

./openmarmut chat --auto-approve << 'EOF'
Create a file called rewind-test.txt with "original"
Now change rewind-test.txt to "modified"
/rewind 1
/quit
EOF
# Expected: file restored to "original" after rewind
cat rewind-test.txt
# Expected: "original"
```

### 8.3 Diff and Commit

```bash
cd /tmp/test-project

echo "new file" > diff-test.txt
git add diff-test.txt

./openmarmut chat << 'EOF'
/diff
/commit test: add diff test file
y
/quit
EOF
# Expected: /diff shows changes, /commit prompts for confirmation, creates commit

git log --oneline -1
# Expected: "test: add diff test file"
```

---

## 9. CONTEXT MANAGEMENT

### 9.1 Context Usage Display

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/context
/quit
EOF
# Expected: styled box with:
#   - Model context window (e.g., 128000 tokens)
#   - Current usage (tokens and %)
#   - Number of turns
#   - System prompt tokens
#   - Truncation threshold (80%)
#   - Progress bar: ██████░░░░░░ N%
```

### 9.2 Context in Summary Line

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
Hello, how are you?
/quit
EOF
# Expected: summary line includes "ctx: N%" at the end
# Format: [N tool calls │ P+C=T tokens │ ~$cost │ Ns │ ctx: N%]
```

### 9.3 Compact with Custom Instructions

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
Tell me about Go programming
Now tell me about Python
/compact Focus on the programming languages discussed
/context
/quit
EOF
# Expected: /compact shows before/after token counts
# /context shows reduced usage after compaction
```

---

## 10. SUB-AGENTS

### 10.1 Spawn Sub-Agent

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/agent Read all Go files and summarize the project structure
/agents
/quit
EOF
# Expected: sub-agent spawned, runs independently
# /agents shows the sub-agent with status/tokens/duration
```

### 10.2 Sub-Agent with Different Provider

```bash
cd /tmp/test-project

# Only testable with multi-provider config
./openmarmut chat << 'EOF'
/agent --provider azure Summarize main.go
/agents
/quit
EOF
# Expected: sub-agent uses specified provider
```

### 10.3 Sub-Agent Management

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/agents
/quit
EOF
# Expected: "No sub-agents" or list with columns:
#   Name | Status | Tokens | Duration
```

---

## 11. MCP (Model Context Protocol)

### 11.1 MCP Server Configuration

```bash
# Add MCP config to .openmarmut.yaml
cat >> /tmp/test-project/.openmarmut.yaml << 'YAMLEOF'

mcp:
  servers:
    - name: test-server
      transport: sse
      url: http://localhost:3000/sse
YAMLEOF

# List MCP servers
./openmarmut mcp list
# Expected: shows test-server with SSE transport

# Test MCP connection (will fail if no server running)
./openmarmut mcp test test-server
# Expected: connection attempt, error if server not running
```

### 11.2 MCP in Chat

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/mcp
/quit
EOF
# Expected: shows MCP server status (connected/disconnected)
```

### 11.3 MCP Add Command

```bash
cd /tmp/test-project

./openmarmut mcp add new-server http://localhost:4000/sse
# Expected: adds server to config

./openmarmut mcp list
# Expected: shows new-server in list
```

---

## 12. ADVANCED FEATURES

### 12.1 /btw Side Questions

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
Tell me about this project
/btw What year was Go created?
What were we talking about?
/quit
EOF
# Expected:
# - First message: agent analyzes project
# - /btw: isolated response in styled box, separate token count
# - Third message: agent continues project discussion (btw didn't pollute history)
```

### 12.2 /loop Recurring Tasks

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/loop 5s echo "heartbeat"
/loop status
/loop off
/quit
EOF
# Expected:
# - /loop 5s: starts loop running "echo heartbeat" every 5 seconds
# - /loop status: shows active loop with interval and command
# - /loop off: stops all loops
```

### 12.3 /bg Background Execution

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/bg Analyze the project structure and create a summary
/bg status
/quit
EOF
# Expected:
# - /bg: spawns background sub-agent
# - /bg status: shows running job with ID, status, task
```

### 12.4 /tasks Management

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/tasks add Implement login feature
/tasks add Write tests
/tasks
/tasks done 1
/tasks
/tasks clear
/quit
EOF
# Expected: tasks created, listed, completed (✓), cleared
```

### 12.5 Custom Slash Commands

```bash
cd /tmp/test-project

# Create custom command
mkdir -p .openmarmut/commands
cat > .openmarmut/commands/test.md << 'EOF'
---
description: Run project tests
---
Run all tests in this project and report any failures.
EOF

./openmarmut chat << 'EOF'
/commands
/test
/quit
EOF
# Expected:
# - /commands: lists "test" with description
# - /test: executes the custom command content via agent
```

### 12.6 /model and /effort Switching

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/model
/model gpt-4o-mini
/model
/effort
/effort high
/effort
/quit
EOF
# Expected:
# - /model: shows current provider/model
# - /model gpt-4o-mini: switches model for session
# - /effort: shows current setting
# - /effort high: sets reasoning effort to high
```

### 12.7 Extended Thinking Toggle

```bash
cd /tmp/test-project

./openmarmut chat << 'EOF'
/thinking
/thinking
/quit
EOF
# Expected: toggles extended thinking on/off
# Shows "Extended thinking: ON" / "Extended thinking: OFF"
```

---

## 13. UI/UX

### 13.1 Styled Output

```bash
cd /tmp/test-project

# Providers table — color-coded types
./openmarmut providers
# Expected: styled table with colored provider types, ★ marker, truncated endpoints

# ls — colored permissions and human-readable sizes
echo "test" > small.txt
dd if=/dev/zero of=large.bin bs=1024 count=100 2>/dev/null
./openmarmut ls .
# Expected: permissions colored (r=green, w=yellow, x=red), sizes like "100.0 KB"

# Info — styled box
./openmarmut info
# Expected: bordered box with Runtime/Target/Provider/Model fields
```

### 13.2 Error Messages with Hints

```bash
cd /tmp/test-project

# File not found — should suggest checking path
./openmarmut read nonexistent-file.txt
# Expected: red ✗ error with hint about file not found

# Missing provider config
./openmarmut ask --provider missing "hello"
# Expected: error with hint about available providers
```

### 13.3 Human-Readable File Sizes

```bash
cd /tmp/test-project

# Verify HumanizeBytes in ls output
dd if=/dev/zero of=test-1k.bin bs=1024 count=1 2>/dev/null
dd if=/dev/zero of=test-1m.bin bs=1024 count=1024 2>/dev/null
./openmarmut ls .
# Expected: "1.0 KB" and "1.0 MB" (not raw byte counts)
rm -f test-1k.bin test-1m.bin large.bin
```

### 13.4 Syntax Highlighting

```bash
cd /tmp/test-project

echo 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hello")\n}' > highlight.go
./openmarmut read highlight.go
# Expected: syntax-highlighted Go code via glamour
rm highlight.go
```

---

## 14. CONFIGURATION

### 14.1 Config File Loading

```bash
cd /tmp/test-project

cat > .openmarmut.yaml << 'YAMLEOF'
mode: local
target: /tmp/test-project
log:
  level: info
  format: text
llm:
  active_provider: azure
  providers:
    - name: azure
      type: openai
      endpoint: https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT/chat/completions?api-version=2024-12-01-preview
      auth:
        api_key: env:AZURE_OPENAI_API_KEY
      model: gpt-4o
      context_window: 128000
agent:
  auto_memory: true
  session_retention_days: 30
  context_window: 128000
  truncation_threshold: 0.8
  keep_recent_turns: 4
  auto_allow:
    - read_file
    - list_dir
  confirm:
    - write_file
    - execute_command
YAMLEOF

./openmarmut info
# Expected: shows config loaded correctly — mode: local, target dir
```

### 14.2 Environment Variable Overrides

```bash
cd /tmp/test-project

# OPENMARMUT_* env vars override config file
OPENMARMUT_MODE=local ./openmarmut info
# Expected: mode: local

OPENMARMUT_LOG_LEVEL=debug ./openmarmut info
# Expected: debug-level logging visible
```

### 14.3 Flag Overrides Beat Everything

```bash
cd /tmp/test-project

# --mode flag overrides env and config
OPENMARMUT_MODE=docker ./openmarmut -m local info
# Expected: mode: local (flag wins over env)

# --target flag
./openmarmut -t /tmp info
# Expected: target: /tmp
```

### 14.4 Agent Config

```bash
cd /tmp/test-project

# Verify agent config fields are respected
./openmarmut chat << 'EOF'
/context
/quit
EOF
# Expected: context window matches config (128000)
# Truncation threshold at 80%
```

---

## Automated Unit Test Results

Run the full unit test suite to verify all features at the code level:

```bash
cd /path/to/opencode-go

# All unit tests (19 packages)
go test ./... -v -count=1 2>&1 | tail -30
# Expected: all PASS
# Approximate test count: 400+ tests across 19 packages

# Individual package tests for targeted verification
go test ./internal/pathutil/ -v          # path sandboxing
go test ./internal/config/ -v            # config loading
go test ./internal/localrt/ -v           # local runtime (44 tests)
go test ./internal/dockerrt/ -v          # docker runtime (40 tests, mocked)
go test ./internal/llm/... -v            # all LLM providers (100+ tests)
go test ./internal/agent/ -v             # agent loop, tools, permissions, memory, rules, skills, etc.
go test ./internal/cli/ -v               # CLI commands and chat tests
go test ./internal/session/ -v           # session persistence
go test ./internal/mcp/ -v              # MCP client
go test ./internal/ui/ -v               # UI styles and spinner

# Docker integration tests (requires Docker running)
go test ./internal/dockerrt/ -tags "integration,docker" -v
# Expected: 16 integration tests pass with real Docker
```

---

## Build Verification

```bash
cd /path/to/opencode-go

# Build
go build -o openmarmut ./cmd/openmarmut
echo $?
# Expected: 0

# Verify binary
./openmarmut --help
# Expected: usage output with all commands listed

# Verify version/info
./openmarmut info -t /tmp
# Expected: styled box with runtime info
```

---

## Test Matrix Summary

| Feature Area          | Unit Tests | Manual Tests | Docker Tests |
|-----------------------|------------|--------------|--------------|
| Path Sandboxing       | ✓          | ✓            | ✓            |
| Local Runtime         | 44 tests   | 6 tests      | —            |
| Docker Runtime        | 40 tests   | 6 tests      | 16 tests     |
| Config                | 42 tests   | 4 tests      | —            |
| LLM Providers (6)     | 100+ tests | 3 tests      | —            |
| Agent Loop            | 21+ tests  | 4 tests      | —            |
| Agent Tools (17)      | 50+ tests  | 5 tests      | —            |
| Permissions           | 23 tests   | 2 tests      | —            |
| Context Management    | 13+ tests  | 3 tests      | —            |
| Sessions              | 15 tests   | 4 tests      | —            |
| Git Integration       | 34 tests   | 3 tests      | —            |
| Plan Mode             | 17 tests   | 3 tests      | —            |
| Project Memory        | 16 tests   | 2 tests      | —            |
| Rules                 | 22 tests   | 1 test       | —            |
| Skills                | 12 tests   | 1 test       | —            |
| Auto-Memory           | ~20 tests  | 3 tests      | —            |
| Ignore System         | ~25 tests  | 3 tests      | —            |
| Sub-Agents            | 29 tests   | 3 tests      | —            |
| MCP                   | 33 tests   | 3 tests      | —            |
| Tasks                 | 14 tests   | 1 test       | —            |
| Custom Commands       | 12 tests   | 2 tests      | —            |
| /btw, /loop, /bg      | 20 tests   | 3 tests      | —            |
| UI Styles             | 23+ tests  | 4 tests      | —            |
| Chat REPL             | 80+ tests  | 20+ tests    | —            |
| **Total**             | **~550+**  | **~90+**     | **16**       |
