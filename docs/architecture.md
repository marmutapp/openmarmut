# OpenMarmut-Go: Architecture Overview

## High-Level Flow

```
User invokes CLI
       │
       ▼
┌──────────────┐
│  cobra CLI   │  Parses flags, selects command
│  (cmd/)      │
└──────┬───────┘
       │
       ▼
┌──────────────┐
│   Runner     │  Loads config → logger → runtime → Init() → fn() → Close()
│  (cli/)      │
└──────┬───────┘
       │
       ▼
┌──────────────┐
│   Runtime    │  Factory selects implementation based on config.Mode
│  Factory     │
└──────┬───────┘
       │
       ├──── mode=local ────▶ LocalRuntime
       │                       • os.ReadFile, os.WriteFile
       │                       • exec.CommandContext("sh", "-c", cmd)
       │                       • pathutil.Resolve for sandboxing
       │
       └──── mode=docker ───▶ DockerRuntime
                               • Docker SDK client
                               • Container with bind mount
                               • docker exec for all operations
                               • stdcopy.StdCopy for stream demux
```

## Dependency Graph

```
cmd/openmarmut/main.go
    └── internal/cli

internal/cli
    ├── internal/config
    ├── internal/logger
    └── internal/runtime (interface)

internal/runtime (interface + factory)
    ├── internal/localrt
    ├── internal/dockerrt
    └── internal/config

internal/localrt
    ├── internal/runtime (types)
    └── internal/pathutil

internal/dockerrt
    ├── internal/runtime (types)
    ├── internal/pathutil
    └── github.com/docker/docker/client

internal/pathutil
    └── (stdlib only)

internal/config
    └── gopkg.in/yaml.v3

internal/logger
    └── (stdlib only: log/slog)
```

No circular dependencies. pathutil and config are leaf nodes.

## Security Boundaries

```
┌──────────────────────────────────────────┐
│              Target Directory             │
│                                          │
│  pathutil.Resolve ensures ALL paths      │
│  resolve within this boundary.           │
│                                          │
│  ../../etc/passwd → ErrPathEscape        │
│  /absolute/path  → rejected             │
│  a/../b          → resolved to b ✓      │
└──────────────────────────────────────────┘

Docker Mode adds a second boundary:

┌─────────────────────────────────────────┐
│           Docker Container              │
│  ┌───────────────────────────────────┐  │
│  │  /workspace (bind mount)          │  │
│  │  = Host target directory          │  │
│  │  All operations scoped here       │  │
│  └───────────────────────────────────┘  │
│  NetworkMode: "none" (default)          │
│  No host access beyond the mount        │
└─────────────────────────────────────────┘
```
