---
name: Bug Report
about: Create a report to help us improve Aegora
title: "fix: "
labels: ["bug"]
assignees: []
---

## Description
A clear and concise description of what the bug is.

## Environment
- **Aegora Version**: (e.g. git commit SHA, tag, or version from `GET /admin/health`)
- **Go Version**: (e.g. `go version go1.26.4 darwin/arm64`)
- **OS / Architecture**: (e.g. macOS arm64, Ubuntu 24.04 x86_64, Docker)

## Configuration
Provide the relevant snippet from your `aegora.yaml` or environment variable overrides:

```yaml
# Paste relevant configuration snippet here
```

## Steps to Reproduce
1. Start the gateway with config ...
2. Send request: `curl ...`
3. Observe behavior ...

## Expected Behavior
A clear and concise description of what you expected to happen.

## Actual Behavior
A clear and concise description of what actually happened (include HTTP status codes and response headers).

## Gateway Logs
Include relevant stdout or structured `log/slog` output from the gateway process:

```text
# Paste relevant gateway logs here
```

## Additional Context
Add any other context about the problem here (for example, upstream backend configuration or network topology).
