# OpenCode-Go

CLI tool for AI-assisted development with local or Docker runtime modes. Operates on a target project directory through a unified `Runtime` interface.

## Build

```bash
make build
```

Or directly:

```bash
go build -o opencode ./cmd/opencode
```

## Usage

### Global flags

```
--mode, -m      Runtime mode: "local" (default) or "docker"
--target, -t    Target directory (default: current directory)
--config, -c    Config file path
--log-level     debug / info / warn / error (default: info)
--log-format    text / json (default: text)
```

### Commands

```bash
# Read a file
opencode read path/to/file.txt

# Write stdin to a file
echo "content" | opencode write path/to/file.txt

# Delete a file
opencode delete path/to/file.txt

# List directory
opencode ls src/

# Create directory
opencode mkdir path/to/dir

# Execute a command
opencode exec "go test ./..."

# Show runtime info
opencode info
```

### Local mode (default)

Operates directly on the host filesystem:

```bash
opencode -t /path/to/project read main.go
opencode -t /path/to/project exec "go build ./..."
```

### Docker mode

Mounts the target directory into an isolated container:

```bash
# Build the default image
docker build -t opencode-sandbox .

# Use Docker mode
opencode -m docker --docker-image opencode-sandbox read main.go
opencode -m docker --docker-image opencode-sandbox exec "ls -la"
```

Or via config file (`.opencode.yaml`):

```yaml
mode: docker
docker:
  image: opencode-sandbox
  mount_path: /workspace
  network_mode: none
```

### Configuration

Config is merged from (highest to lowest priority):

1. CLI flags
2. Environment variables (`OPENCODE_MODE`, `OPENCODE_TARGET_DIR`, etc.)
3. Config file (`.opencode.yaml` in target dir, or `~/.config/opencode/config.yaml`)
4. Defaults

## Testing

```bash
# Unit tests
make test

# Docker integration tests (requires Docker)
make integration-test

# Lint (format + vet)
make lint
```

## Architecture

```
cmd/opencode/         Entrypoint
internal/
  cli/                Cobra commands + Runner lifecycle
  runtime/            Runtime interface, types, factory
  localrt/            Local filesystem + os/exec implementation
  dockerrt/           Docker SDK implementation
  config/             Config loading (flags > env > file > defaults)
  pathutil/           Path sandboxing
  logger/             slog wrapper
```

Both runtimes implement the same `Runtime` interface — the CLI doesn't know which one it's using.

## License

See LICENSE file.
