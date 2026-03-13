---
globs: ["**/*_test.go"]
---
# Testing Rules
- Use testify require (fatal) and assert (non-fatal)
- Use t.TempDir() for filesystem tests — auto-cleaned
- Docker integration tests gated by: //go:build integration && docker
- Naming: Test<Type>_<Method> or Test<Function>_<Case>
- Table-driven tests for functions with multiple input cases
- Always test: happy path, error path, edge cases, path escape attempts
- For Runtime compliance: write shared test suite accepting Runtime interface
- For localrt: test against real t.TempDir() filesystem
- For dockerrt unit tests: mock the Docker client interface
- Never skip error assertions — every error return must be checked
