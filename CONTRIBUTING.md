# Contributing to Aegora

Thank you for your interest in contributing to Aegora. Aegora is an open-source Go Layer 7 reliability gateway designed for high-concurrency microservice traffic management. We welcome contributions ranging from bug reports and documentation fixes to performance optimizations and new resilience features.

This guide outlines our development environment, coding standards, pull request workflow, and verification steps.

---

## Getting Started

### Prerequisites

Before working on the codebase, ensure your local environment has the following installed:

- **Go**: Version `1.26.4` (or compatible Go 1.25+ as declared in `go.mod`).
- **Make**: For running build automation and verification targets.
- **golangci-lint**: For running static analysis (`make lint`).
- **Redis** (optional for unit tests, required for full integration testing): Unit tests use an embedded miniredis mock; a live Redis instance on port `6379` is required for multi-tier caching and distributed rate-limiting manual tests.
- **Docker and Docker Compose**: For containerized packaging and the demo environment.

### Clone and Build

1. Clone the repository and navigate into the root directory:
   ```bash
   git clone https://github.com/syncronix-labs/aegora.git
   cd aegora
   ```

2. Download module dependencies:
   ```bash
   go mod download
   ```

3. Build the binary:
   ```bash
   make build
   ```
   The compiled binary will be placed at `bin/aegora`.

4. Run all verification checks:
   ```bash
   make verify
   ```

---

## Development Workflow

1. **Fork and Branch**:
   - Fork the repository on GitHub.
   - Clone your fork locally.
   - Create a feature branch originating from `main`:
     ```bash
     git checkout -b feature/my-improvement
     ```

2. **One Logical Change per PR**:
   - Keep each pull request focused on a single feature, bugfix, or refactoring task.
   - Avoid combining unrelated modifications into a single branch.

3. **Code Style and Formatting**:
   - Follow standard Go idioms and naming conventions.
   - Document all exported packages, types, functions, and methods with godoc comments.
   - Run `make fmt` to automatically format all files with `go fmt` and `go vet`.

---

## Commit Message Conventions

We enforce the Conventional Commits specification and require scoped conventional commits for code changes. Format: type(scope): summary.

```text
type(scope): summary
```

feat and fix commits must carry a scope naming the affected package or area.

Common types include:
- `feat`: A new user-facing capability or architectural component.
- `fix`: A bug fix.
- `docs`: Documentation additions or revisions.
- `test`: Adding or refactoring tests.
- `refactor`: Code reorganization with no behavior changes.
- `chore`: Build tooling, dependency, or configuration updates.

### Examples from Repository History

- `feat(middleware): add request body size limit`
- `fix(reliability): do not trip breaker on oversized chunked bodies`
- `fix(cache): normalize query parameter order in cache keys`
- `docs: document VERSION Docker build arg`

Write commit descriptions in the imperative, present tense (for example, "add feature" rather than "added feature").

### Co-Authored-By Trailers

Do not add Co-Authored-By trailers to commit messages. GitHub credits anyone named in a Co-Authored-By trailer as a repository contributor, which is reserved for the human author of the change. AI assistance is fine to use and should be disclosed in the pull request description instead of in commit metadata.

---

## Verification Before Pushing

Before committing or pushing changes, run the primary validation target:

```bash
make verify
```

`make verify` executes the complete validation pipeline:
1. `make fmt`: Checks Go code formatting and runs `go vet`.
2. `make tidy`: Ensures `go.mod` and `go.sum` are synchronized.
3. `make lint`: Runs `golangci-lint` with the project configuration.
4. `make test`: Runs all unit and integration tests with the `-race` detector and `-cover` analysis.
5. `make build`: Builds the static binary with compiler flags.

All checks must pass with zero errors, zero lint warnings, and zero race conditions before submitting a pull request.

---

## Pull Request Rules

1. **Never Squash-Merge**:
   - Aegora maintains an explicit, linear commit history where each commit represents a reviewable step.
   - Maintainers will merge pull requests using a standard merge commit or rebase-and-merge.
   - Please keep your commits clean, atomic, and well-described.

2. **Keep PRs Focused**:
   - PRs addressing multiple unrelated subsystems will be asked to split into smaller, independent submissions.

3. **Documentation Updates**:
   - If your change adds, removes, or modifies any YAML configuration field in `internal/config`, you must update [docs/configuration.md](docs/configuration.md) in the same PR.
   - If your change introduces significant architectural changes, update [docs/architecture.md](docs/architecture.md).

4. **Style Constraint**:
   - Do not use the em dash character in user-facing text, error messages, code comments, or documentation. Use a plain hyphen or rephrase.

---

## Where to Ask Questions

- **Setup and Usage Questions**: GitHub Discussions is currently not enabled for this repository. If you have setup questions or usage inquiries, please check [docs/configuration.md](docs/configuration.md) and [docs/architecture.md](docs/architecture.md) first. If you still need help, open an issue using the `question` label.
- **Bug Reports**: Open an issue using the [Bug Report](.github/ISSUE_TEMPLATE/bug_report.md) template.
- **Feature Requests**: Open an issue using the [Feature Request](.github/ISSUE_TEMPLATE/feature_request.md) template.
- **Security Vulnerabilities**: Report security issues privately by following [SECURITY.md](SECURITY.md). Never post security exploits publicly on GitHub Issues.
