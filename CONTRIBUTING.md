# Contributing to Kiseki

Thanks for your interest in contributing.

## Reporting Issues

Open a GitHub issue with a clear description, steps to reproduce, and your environment (OS, Go version, Ollama version).

## Building

```bash
go build -tags fts5 ./...
```

## Running Tests

```bash
go test -tags fts5 ./...
```

The `fts5` build tag is required for SQLite full-text search support.

## Branch Policy

Work on a feature branch. Submit pull requests against `main`. Keep PRs focused on a single concern.

## Code Style

Standard Go conventions. Run `gofmt` before committing. Keep diffs minimal and purposeful.
