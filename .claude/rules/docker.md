---
globs: ["internal/dockerrt/**"]
---
# Docker Runtime Rules
- Use Docker SDK (github.com/docker/docker/client), never shell out to docker CLI
- All file reads: base64 encode via docker exec, decode on host
- All file writes: base64 encode on host, pipe to docker exec via stdin
- For files >10MB, switch to docker cp + tar extraction
- Use stdcopy.StdCopy to demux multiplexed docker exec streams
- Container lifecycle: create + start on Init(), stop + rm on Close()
- Container command: ["sleep", "infinity"]
- Default mount point: /workspace
- Network mode: "none" for isolation by default
- Run commands with --user matching host UID:GID to avoid permission issues
- Handle container death: return ErrContainerNotRunning
- Use path.Join (not filepath.Join) for container-side Linux paths
- Shell-escape all paths used in docker exec commands
