# Claude Code Operating Guide for OpenMarmut-Go

How to build this project with Claude Code across multiple sessions without losing context or progress.

---

## The Problem

Claude Code starts every session with a blank context window (~200K tokens). It has no memory of what you worked on yesterday. Your strategy has three layers that work together:

1. **CLAUDE.md** — loaded every session, survives compaction, under 200 lines
2. **Files on disk** (specs/, progress/) — ground truth, read on demand
3. **Session discipline** — how you start, work, and end each session

---

## Starting a Session

**Always use this pattern as your first message:**

```
Read progress/progress.md and specs/system-spec.md.
Tell me what's done, what's in progress, and what the next item is.
Then implement it.
```

Or if you already know what you want:

```
Read progress/progress.md. Continue with the next unchecked item.
```

This forces Claude to ground itself in actual project state before writing code.

---

## During a Session

### One module at a time
Don't ask Claude to implement localrt, dockerrt, and CLI in one shot. For each module:
1. Implement the code
2. Write tests
3. Run tests (`go test ./internal/pathutil/...`)
4. Fix failures
5. Update progress.md
6. Commit

### Monitor context
Run `/context` periodically. At 50%, decide: compact or clear.

### Compact with instructions
```
/compact "Completed pathutil module with full tests. Moving to config module next."
```

This tells the compactor what to preserve. CLAUDE.md reloads automatically after compaction.

---

## Ending a Session

Before closing the terminal:

```
Update progress/progress.md with what we accomplished. Add a session log entry.
Then commit all changes with a conventional commit message.
```

This ensures the ground truth is on disk and in git.

---

## Resuming After a Break

**Option A: Continue last session**
```bash
claude --continue
```
Then: `Read progress/progress.md. What's the current state?`

**Option B: Fresh start (recommended for multi-day gaps)**
```bash
claude
```
Then: `Read progress/progress.md and the session log. What's next?`

Fresh starts are often better because old sessions may have compacted and lost detail.

---

## The "Document & Clear" Pattern

For complex multi-step work that fills context:

1. Work on a feature until ~50% context
2. Ask Claude:
   ```
   Write a session handoff to progress/handoff-<module>.md covering:
   - What's implemented and tested
   - What's in progress (file paths, function names)
   - What's left to do
   - Any decisions or gotchas
   ```
3. Commit everything including the handoff
4. `/clear` or start new session
5. Resume: `Read progress/handoff-<module>.md and continue where we left off.`

More reliable than compaction because you control what transfers.

---

## Session Plan for OpenMarmut-Go

### Session 1: Foundation
```
Implement internal/pathutil, internal/runtime (interface only),
internal/config, and internal/logger. Each with tests.
Update progress.md and commit after each module.
```

### Session 2: Local Runtime
```
Implement all internal/localrt methods with full test suite.
May need document-and-clear if context fills up.
```

### Session 3: CLI
```
Implement internal/cli (root, runner, all commands) and cmd/openmarmut/main.go.
Manual smoke test: go run ./cmd/openmarmut read README.md
```

### Session 4: Docker Runtime
```
Implement internal/dockerrt. Unit tests with mocked Docker client.
Integration tests if Docker available.
```

### Session 5: Polish
```
Runtime factory. E2E tests. Makefile. Dockerfile. README.
```

---

## Quick Reference

| Situation | Action |
|-----------|--------|
| Start of session | `Read progress/progress.md` |
| Switching modules | Commit, then `/compact "Done with X. Next: Y"` |
| Context heavy (>50%) | `/context` to check, commit, then `/compact` |
| End of session | `Update progress.md, add session log, commit` |
| Multi-day gap | Fresh `claude` + read progress.md |
| Complex handoff | Write handoff .md → commit → `/clear` |
| Parallel work | `claude --session-id <name>` per independent module |

---

## Anti-Patterns

- **Don't paste the full spec into chat.** CLAUDE.md points to spec files. Claude reads them on demand.
- **Don't rely on auto-memory for progress.** progress.md is the ground truth.
- **Don't skip the progress update.** Every session must end with one.
- **Don't implement multiple modules without committing.** Compaction loses uncommitted code.
- **Don't grow CLAUDE.md past 200 lines.** Split into .claude/rules/ files.
- **Don't ask Claude to "remember" things.** Write them to files instead.
