# OpenCode-Go Hands-On Walkthrough

This walkthrough assumes Ubuntu with Go 1.22+ installed.
Every command is copy-pasteable and designed to run in sequence.

---

## 1. Build the Binary

```bash
cd /path/to/opencode-go
go build -o opencode ./cmd/opencode
```

Verify:

```bash
./opencode --help
```

Expected output:

```
CLI tool for AI-assisted development with local or Docker runtimes

Usage:
  opencode [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  delete      Delete a file
  exec        Execute a shell command
  help        Help about any command
  info        Show runtime information
  ls          List directory contents
  mkdir       Create a directory
  read        Read a file and print to stdout
  write       Write stdin to a file

Flags:
  -c, --config string       config file path
  -h, --help                help for opencode
      --log-format string   log format: text/json
      --log-level string    log level: debug/info/warn/error
  -m, --mode string         runtime mode: "local" or "docker"
  -t, --target string       target directory (default: cwd)

Use "opencode [command] --help" for more information about a command.
```

---

## 2. Local Mode Walkthrough

### 2.1 Set up a temp project

```bash
export DEMO_DIR=$(mktemp -d)
echo "Created demo dir: $DEMO_DIR"
```

### 2.2 Show runtime info

```bash
./opencode -t "$DEMO_DIR" info
```

Expected:

```
Target directory: /tmp/tmp.XXXXXXXX
```

### 2.3 Create directories

```bash
./opencode -t "$DEMO_DIR" mkdir src
./opencode -t "$DEMO_DIR" mkdir src/utils
```

No output on success.

### 2.4 Write files

```bash
echo 'package main

import "fmt"

func main() {
    fmt.Println("hello from opencode")
}' | ./opencode -t "$DEMO_DIR" write src/main.go

echo '# My Project' | ./opencode -t "$DEMO_DIR" write README.md
echo 'module demo' | ./opencode -t "$DEMO_DIR" write go.mod
```

No output on success.

### 2.5 List directory contents

Root of the project:

```bash
./opencode -t "$DEMO_DIR" ls
```

Expected (your sizes/perms may vary):

```
-  -rw-r--r--  12          README.md
-  -rw-r--r--  12          go.mod
d  -rwxr-xr-x  4096        src
```

Nested directory:

```bash
./opencode -t "$DEMO_DIR" ls src
```

Expected:

```
-  -rw-r--r--  83          main.go
d  -rwxr-xr-x  4096        utils
```

### 2.6 Read a file

```bash
./opencode -t "$DEMO_DIR" read src/main.go
```

Expected:

```
package main

import "fmt"

func main() {
    fmt.Println("hello from opencode")
}
```

### 2.7 Execute a command

Simple command:

```bash
./opencode -t "$DEMO_DIR" exec "ls -1"
```

Expected:

```
README.md
go.mod
src
```

Command with environment variables:

```bash
./opencode -t "$DEMO_DIR" exec -e GREETING=hello "echo \$GREETING world"
```

Expected:

```
hello world
```

Command in a subdirectory:

```bash
./opencode -t "$DEMO_DIR" exec -w src "ls -1"
```

Expected:

```
main.go
utils
```

### 2.8 Overwrite a file

```bash
echo '# My Updated Project' | ./opencode -t "$DEMO_DIR" write README.md
./opencode -t "$DEMO_DIR" read README.md
```

Expected:

```
# My Updated Project
```

### 2.9 Delete a file

```bash
./opencode -t "$DEMO_DIR" delete go.mod
./opencode -t "$DEMO_DIR" ls
```

Expected — `go.mod` is gone:

```
-  -rw-r--r--  21          README.md
d  -rwxr-xr-x  4096        src
```

### 2.10 Clean up

```bash
rm -rf "$DEMO_DIR"
```

---

## 3. Docker Mode Walkthrough

Requires Docker to be installed and running.

### 3.1 Set up

```bash
export DEMO_DIR=$(mktemp -d)
echo "Created demo dir: $DEMO_DIR"
```

### 3.2 Write a config file for Docker mode

Rather than passing flags on every command, create a config file:

```bash
cat > "$DEMO_DIR/.opencode.yaml" << 'EOF'
mode: docker
docker:
  image: ubuntu:20.04
  mount_path: /workspace
  network_mode: none
EOF
```

### 3.3 Show runtime info

```bash
./opencode -t "$DEMO_DIR" info
```

Expected:

```
Target directory: /tmp/tmp.XXXXXXXX
```

You can verify a container was created and destroyed by watching in another terminal:

```bash
# In another terminal, run:
watch docker ps
```

Each opencode command creates a container, runs the operation, and destroys it.

### 3.4 Create directories and write files

```bash
./opencode -t "$DEMO_DIR" mkdir src
echo 'print("hello from docker")' | ./opencode -t "$DEMO_DIR" write src/app.py
```

### 3.5 List and read

```bash
./opencode -t "$DEMO_DIR" ls
```

Expected:

```
-  -rw-r--r--  89          .opencode.yaml
d  -rwxr-xr-x  4096        src
```

```bash
./opencode -t "$DEMO_DIR" read src/app.py
```

Expected:

```
print("hello from docker")
```

### 3.6 Execute a command inside the container

```bash
./opencode -t "$DEMO_DIR" exec "uname -a"
```

Expected (shows the container's kernel, not your host):

```
Linux <container-id> ...
```

```bash
./opencode -t "$DEMO_DIR" exec "whoami"
```

Expected (runs as your UID mapped in the container):

```
I have no name!
```

This is expected — the container runs with your host UID:GID for file
permission safety, but there is no `/etc/passwd` entry for that UID.

### 3.7 Verify container lifecycle

Immediately after a command, check that no container is left running:

```bash
docker ps --filter "ancestor=ubuntu:20.04" --format "{{.ID}} {{.Status}}"
```

Expected: empty (container was stopped and removed).

### 3.8 Execute with env vars and working directory

```bash
./opencode -t "$DEMO_DIR" exec -w src -e LANG=C "pwd && ls -1"
```

Expected:

```
/workspace/src
app.py
```

### 3.9 Delete and clean up

```bash
./opencode -t "$DEMO_DIR" delete src/app.py
rm -rf "$DEMO_DIR"
```

---

## 4. Config File Walkthrough

### 4.1 Config file

Create a project with a `.opencode.yaml`:

```bash
export DEMO_DIR=$(mktemp -d)

cat > "$DEMO_DIR/.opencode.yaml" << 'EOF'
mode: local
log:
  level: debug
  format: text
default_timeout: 10s
EOF
```

Now commands pick up the config automatically:

```bash
./opencode -t "$DEMO_DIR" exec "echo hi"
```

Expected — you will see debug-level log lines on stderr before the output:

```
time=... level=INFO msg="local runtime initialized" target=...
hi
```

(With `level: debug`, you may see additional log output depending on the operation.)

### 4.2 Environment variable overrides

Environment variables override the config file:

```bash
# Override mode (will fail without Docker, but demonstrates the override)
OPENCODE_MODE=local OPENCODE_TARGET_DIR="$DEMO_DIR" ./opencode info
```

Expected:

```
Target directory: /tmp/tmp.XXXXXXXX
```

Override log level to suppress logs:

```bash
OPENCODE_LOG_LEVEL=error ./opencode -t "$DEMO_DIR" exec "echo quiet"
```

Expected — no log lines, just:

```
quiet
```

### 4.3 Docker image via environment variable

```bash
OPENCODE_MODE=docker OPENCODE_DOCKER_IMAGE=ubuntu:20.04 ./opencode -t "$DEMO_DIR" exec "echo from-docker"
```

Expected (if Docker is running):

```
from-docker
```

### 4.4 Flag overrides beat everything

Flags always win, even over env vars and config files:

```bash
OPENCODE_MODE=docker ./opencode -t "$DEMO_DIR" -m local exec "echo flags-win"
```

Expected:

```
flags-win
```

### 4.5 Clean up

```bash
rm -rf "$DEMO_DIR"
```

---

## 5. Error Cases

### 5.1 Path escape attempt

```bash
export DEMO_DIR=$(mktemp -d)

./opencode -t "$DEMO_DIR" read ../../../etc/passwd
```

Expected (exit code 1):

```
dockerrt.ReadFile(../../../etc/passwd): path escapes target directory: "../../../etc/passwd" resolves outside mount path
```

Or in local mode:

```
localrt.ReadFile(../../../etc/passwd): pathutil.Resolve: path escapes target directory
```

The exact message depends on the runtime, but the operation is always blocked.

### 5.2 Reading a file that does not exist

```bash
./opencode -t "$DEMO_DIR" read nonexistent.txt
```

Expected (exit code 1):

```
localrt.ReadFile(nonexistent.txt): open /tmp/tmp.XXXXXXXX/nonexistent.txt: no such file or directory
```

### 5.3 Deleting a file that does not exist

```bash
./opencode -t "$DEMO_DIR" delete ghost.txt
```

Expected (exit code 1):

```
localrt.DeleteFile(ghost.txt): remove /tmp/tmp.XXXXXXXX/ghost.txt: no such file or directory
```

### 5.4 Command that fails with non-zero exit code

```bash
./opencode -t "$DEMO_DIR" exec "exit 42"
echo "opencode exit code: $?"
```

Expected — the tool forwards the command's exit code:

```
opencode exit code: 42
```

### 5.5 Command that writes to stderr

```bash
./opencode -t "$DEMO_DIR" exec "echo oops >&2 && exit 1"
echo "exit code: $?"
```

Expected — stderr output appears on stderr, exit code is forwarded:

```
oops
exit code: 1
```

### 5.6 Docker mode without Docker running

If the Docker daemon is not running:

```bash
OPENCODE_MODE=docker OPENCODE_DOCKER_IMAGE=ubuntu:20.04 ./opencode -t "$DEMO_DIR" info
```

Expected (exit code 1):

```
cli.Runner.Run: init runtime: dockerrt.Init: pull image ubuntu:20.04: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?
```

### 5.7 Clean up

```bash
rm -rf "$DEMO_DIR"
```

---

## Summary

| Feature | Command |
|---------|---------|
| Read file | `opencode read <path>` |
| Write file | `echo data \| opencode write <path>` |
| Delete file | `opencode delete <path>` |
| List dir | `opencode ls [path]` |
| Create dir | `opencode mkdir <path>` |
| Run command | `opencode exec "<cmd>"` |
| Runtime info | `opencode info` |
| Target dir | `-t /path` or `OPENCODE_TARGET_DIR` |
| Docker mode | `-m docker` + config/env for image |
| Config file | `.opencode.yaml` in target dir |
