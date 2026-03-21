# Kiseki (軌跡) — Personal Memory for AI Assistants

Your AI forgets everything between sessions. Kiseki fixes that.

**One Go binary. One SQLite file. 100% local. Zero cloud cost.**

[![CI](https://github.com/Gsirawan/kiseki-beta/actions/workflows/ci.yml/badge.svg)](https://github.com/Gsirawan/kiseki-beta/actions/workflows/ci.yml)

## How It Works

```
                        ┌─────────────────┐
  Your notes, docs,     │                 │     Claude Code
  chat sessions    ───► │ Kiseki (SQLite) │ ◄── OpenCode
                        │                 │     Any MCP client
                        └────────┬────────┘
                                 │
                          Search Pipeline
                                 │
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                   ▼
         FTS5 (exact)    Vec Chunks (semantic)  Vec Messages
              │                  │                   │
              └──────────────────┼───────────────────┘
                                 ▼
                     Merge + Tier Rank
               stones > solutions > key > normal
```

Queries go through **entity expansion** first — searching "Customer Guiding System" also finds "CGS" and related aliases — then hit three search layers in parallel. Results are merged, deduplicated, and ranked by importance tier.

## Quick Start

```bash
# 1. Get Kiseki (fts5 tag required for full-text search)
go install -tags "fts5" github.com/Gsirawan/kiseki-beta@latest
# Or download from Releases: https://github.com/Gsirawan/kiseki-beta/releases

# 2. Get an embedding model (Ollama required)
ollama pull qwen3-embedding:0.6b

# 3. Use it
kiseki ingest --file notes.md
kiseki search "authentication setup"
kiseki status
```

## Features

| Feature                | What it does                                                                 |
| ---------------------- | ---------------------------------------------------------------------------- |
| **Multi-layer search** | Entity expansion → FTS5 + vector chunks + vector messages → merged results   |
| **Live capture**       | Auto-ingest from Claude Code / OpenCode sessions in real-time                |
| **MCP server**         | Drop-in integration — your AI searches memory via tool calls                 |
| **Entity graph**       | YAML-defined entities with aliases — search expands aliases to canonical names |
| **Stones**             | Named records for solutions and decisions                                    |
| **Importance marks**   | Tag chunks as `solution` / `key` / `normal` — search prioritizes accordingly |
| **Weekly review**      | Mechanical scoring to surface solutions from conversation history (no LLM)   |
| **Weekly report**      | Generate business summaries of work accomplished                             |
| **Agent mail**         | Message passing between multiple Kiseki instances                            |
| **Migration**          | Import from legacy Nectar databases                                          |
| **Multi-instance**     | Run multiple instances (e.g. work + personal) with `KISEKI_PREFIX`           |
| **Note extraction**    | Auto-save subagent outputs as markdown notes — zero intervention             |

## Commands

```
kiseki ingest       Ingest files (markdown, text, stdin)
kiseki search       Multi-layer search (entity + FTS5 + vectors)
kiseki search-msg   Search messages with context window
kiseki history      Chronological entity mentions
kiseki status       Health check + stats
kiseki serve        Start MCP server
kiseki watch-oc     Realtime messages ingesting - OpenCode session
kiseki watch-cc     Realtime messages ingesting - Claude Code session
kiseki batch-oc     Batch export OpenCode sessions - format (USER, time, date, message)
kiseki batch-cc     Batch export Claude Code sessions - format (USER, time, date, message)
kiseki stone        Manage solution/decision records
kiseki mark         Set chunk importance
kiseki review       Weekly review scoring
kiseki report       Generate weekly report
kiseki re-embed     Re-embed all vectors (model changes)
kiseki migrate      Import from sessions from DB
kiseki list         Show ingested files
kiseki forget       Remove chunks
```

## MCP Integration

Add to `.mcp.json` (Claude Code) or `opencode.json`:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/path/to/kiseki",
      "args": ["serve"],
      "env": {
        "KISEKI_DB": "/path/to/memory.db",
        "KISEKI_PREFIX": "memory",
        "EMBED_MODEL": "qwen3-embedding:0.6b"
      }
    }
  }
}
```

`KISEKI_PREFIX` controls tool names — set it to `nectar` and tools become `nectar_search`, `nectar_ingest`, etc. Run multiple instances with different prefixes.

## Entity Graph

Define entities in YAML, point to it with `KISEKI_ENTITIES`:

```yaml
entities:
  - name: React
    aliases: [ReactJS, react.js]

  - name: Next.js
    aliases: [nextjs, next]
```

Searching "ReactJS" now expands to "React" across all search layers. Aliases are matched with word boundaries to prevent false positives.

## Note Extraction

Auto-save subagent synthesis outputs as markdown files. The watcher detects completed subagent sessions, extracts the final response, and saves it as a structured note — zero manual intervention.

```bash
# Live: extract notes while watching a session
kiseki watch-oc --notes ~/notes --project-dir /path/to/project --notes-agent researcher

# Backfill: extract all existing sessions and exit
kiseki watch-oc --notes ~/notes --project-dir /path/to/project --notes-agent researcher --backfill
```

Notes are saved as `{topic-slug}.md` with metadata headers (Date, Agent, Session, Parent, Prompt). Duplicate sessions are tracked in `.extracted` and skipped on re-runs. Responses under 500 characters are filtered as non-substantial.

## Configuration

| Variable             | Default                | Description                      |
| -------------------- | ---------------------- | -------------------------------- |
| `KISEKI_DB`          | `kiseki.db`            | Database path                    |
| `OLLAMA_HOST`        | `localhost:11434`      | Ollama address                   |
| `EMBED_MODEL`        | `qwen3-embedding:0.6b` | Embedding model                  |
| `EMBED_DIM`          | `1024`                 | Embedding dimensions             |
| `KISEKI_PREFIX`      | `kiseki`               | MCP tool name prefix             |
| `KISEKI_ENTITIES`    | —                      | Path to entities YAML            |
| `KISEKI_NOTES_AGENT` | —                      | Agent name to extract notes from |
| `USER_ALIAS`         | `User`                 | Human name in watcher            |
| `ASSISTANT_ALIAS`    | `Assistant`            | AI name in watcher               |

Also supports: `KISEKI_MAIL_IDENTITY`, `KISEKI_MAIL_PEERS`, `KISEKI_MAIL_DIR`, `KISEKI_TYPOS`, `KISEKI_BACKUP_DIR`, `KISEKI_NOTES_DIR`. See `.env.example`.

## Build

```bash
git clone https://github.com/Gsirawan/kiseki-beta.git && cd kiseki
go build -tags "fts5" -o kiseki .
```

## Stats

- **~15K lines of Go** across 18 internal packages
- **6 direct dependencies** (sqlite-vec, lipgloss, misspell, godotenv, go-sqlite3, go-sdk)
- Go 1.23+, Ollama required

## License

MIT
