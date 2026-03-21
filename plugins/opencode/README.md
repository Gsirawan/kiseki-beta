# Kiseki OpenCode Plugin

Extends [OpenCode](https://github.com/opencode-ai/opencode) with Kiseki's memory and context features. Injects identity, time awareness, cross-agent messaging, and session continuity into your AI assistant — all via OpenCode's plugin system.

## Features

### Foundation Injection

Auto-injects identity and context files into every system prompt. Define a list of markdown files (personality, project context, rules) and they're loaded once, cached, and prepended to every conversation. Your AI starts every session knowing who it is.

### Time Context

Injects the current date, time, period of day (morning/afternoon/evening/night/late night), Hijri (Islamic) calendar date, and seasonal flags into the system prompt. The AI always knows *when* it is — no more "I don't have access to the current time."

Includes:
- **Hijri date** — the Islamic calendar date (e.g., "Islamic date: 24 Ramadan 1447 AH") calculated using the Kuwaiti/tabular algorithm. Accuracy is ±1 day, which is appropriate since Muslims determine actual dates by moon sighting.
- **Ramadan detection** — approximate Gregorian date ranges for 2025–2029 that add a contextual reminder during the holy month.

### Dynamic Reminders

Injects configurable reminder lines from a markdown file into the time context block. The file is watched for changes in real-time using a layered refresh strategy:

1. **`fs.watch`** — instant invalidation on file change (fast path)
2. **10-second polling** — periodic stat check as a fallback if the watcher silently drops events
3. **mtime/size check** — belt-and-suspenders validation on every read

Configure the path via `reminders_path` in `kiseki.json`. The file format is simple markdown:

```markdown
# My reminders (comment lines starting with # are ignored)
- Did you stretch today?
- Remember to drink water
* Check on the deployment status
Plain text lines work too
```

**Format rules:**
- Lines starting with `#` are treated as comments and skipped
- Empty lines are skipped
- Markdown bullet prefixes (`- ` or `* `) are automatically stripped
- Plain text lines are included as-is
- If the file is missing, empty, or unreadable, a default message is shown

### Sister Mail

Cross-agent message notification system. On session start, a toast notification shows unread message count and a preview. A system prompt nudge reminds the AI to check mail if messages are waiting. Designed for multi-agent setups where agents need to communicate across sessions.

### Session Briefing

On session compaction, fires a scribe agent in an ephemeral session to update a briefing file. The plugin extracts a structured summary from the compacted session (user requests, assistant decisions, tools used) and prompts the scribe to merge it into an existing briefing document. The ephemeral session is cleaned up automatically.

### Compaction Preservation

Injects context into the compaction process telling it to preserve identity, relationships, and emotional context — preventing the compactor from summarizing away your foundation.

## Installation

### 1. Copy the plugin

```bash
cp kiseki-oc-plugin.ts ~/.config/opencode/plugins/
```

### 2. Create the config file

Create `.opencode/kiseki.json` in your project root (the directory where you run OpenCode):

```bash
mkdir -p .opencode
cp kiseki.json.example .opencode/kiseki.json
# Edit with your actual paths
```

### 3. Register the plugin

Add the plugin to your OpenCode configuration (`opencode.json`):

```json
{
  "plugins": [
    "~/.config/opencode/plugins/kiseki-oc-plugin.ts"
  ]
}
```

## Configuration

The plugin reads `.opencode/kiseki.json` from your project root. All fields:

```jsonc
{
  // Files to inject into every system prompt (absolute paths)
  "foundation_paths": [
    "/path/to/your/foundation.md",
    "/path/to/your/identity.md"
  ],

  // Directory containing cross-agent JSON message files
  "sister_mail_dir": "/path/to/your/mail/inbox",

  // Feature toggles — enable/disable individually
  "enabled": {
    "foundation": true,       // Inject foundation files into system prompt
    "sister_mail": true,      // Show mail notifications + system prompt nudge
    "session_briefing": true, // Fire scribe agent on session compaction
    "time_context": true      // Inject current time into system prompt
  },

  // Session briefing configuration (required if session_briefing is enabled)
  "briefing": {
    "agent": "your-scribe-agent",           // OpenCode agent name for briefing updates
    "output_path": "/path/to/briefing.md"   // File the scribe agent writes to
  },

  // Time context configuration (required if time_context is enabled)
  "time": {
    "timezone": "America/New_York",  // IANA timezone (e.g., "Asia/Dubai", "Europe/London")
    "locale": "en-US"                // Locale for date formatting (e.g., "en-US", "ja-JP")
  },

  // Path to a markdown file containing reminder lines injected into time context
  // Supports ~ for home directory. File is watched for live changes.
  "reminders_path": "~/reminders.md"
}
```

### Configuration Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `foundation_paths` | `string[]` | No | Array of absolute file paths to inject as system context. Files are read once and cached for the session. Missing files are silently skipped. |
| `sister_mail_dir` | `string` | No | Directory containing `.json` message files. Each file should have `{ "message": "...", "read": false }` structure. |
| `enabled.foundation` | `boolean` | Yes | Toggle foundation file injection. |
| `enabled.sister_mail` | `boolean` | Yes | Toggle sister mail notifications and system prompt nudge. |
| `enabled.session_briefing` | `boolean` | Yes | Toggle session briefing on compaction. |
| `enabled.time_context` | `boolean` | Yes | Toggle time context injection. |
| `briefing.agent` | `string` | If briefing enabled | Name of the OpenCode agent to use for briefing updates. |
| `briefing.output_path` | `string` | If briefing enabled | Absolute path to the briefing file the agent will update. |
| `time.timezone` | `string` | If time enabled | IANA timezone string (e.g., `"Asia/Dubai"`, `"America/New_York"`, `"Europe/London"`). |
| `time.locale` | `string` | No | Locale for `Intl.DateTimeFormat`. Defaults to `"en-US"`. |
| `reminders_path` | `string` | No | Path to a markdown file with reminder lines. Supports `~` for home directory. File is watched for live changes via `fs.watch` with polling fallback. |

## Time Context Details

When `time_context` is enabled, the plugin injects a block like this into every system prompt:

```
<!-- KISEKI TIME CONTEXT -->
Current time: Friday, March 13, 2026, 9:00 PM (Asia/Dubai)
Period: night
Islamic date: 14 Ramadan 1447 AH
Did you stretch today?
Remember to drink water
🌙 Ramadan Mubarak — be mindful of fasting hours and spiritual context.
<!-- END KISEKI TIME CONTEXT -->
```

### Period of Day Logic

| Hour Range | Period |
|------------|--------|
| 00:00–04:59 | late night |
| 05:00–11:59 | morning |
| 12:00–16:59 | afternoon |
| 17:00–20:59 | evening |
| 21:00–23:59 | night |

### Hijri (Islamic) Date

The plugin calculates the Hijri date using the Kuwaiti/tabular algorithm — a well-known arithmetic approximation of the Islamic calendar. No external dependencies are required. The calculation is accurate to ±1 day, which is appropriate since actual Islamic dates are determined by moon sighting and vary by region.

The Hijri date appears in the time context block as:
```
Islamic date: 14 Ramadan 1447 AH
```

Month names used: Muharram, Safar, Rabi al-Awwal, Rabi al-Thani, Jumada al-Ula, Jumada al-Thani, Rajab, Shaban, Ramadan, Shawwal, Dhul Qadah, Dhul Hijjah.

### Seasonal Flags

During Ramadan (approximate Gregorian date ranges for 2025–2029), an additional line is injected:

```
🌙 Ramadan Mubarak — be mindful of fasting hours and spiritual context.
```

The Ramadan ranges are hardcoded approximations. Add future years by extending the `ramadanRanges` object in the plugin source.

## How It Works

The plugin hooks into three OpenCode extension points:

1. **`event`** — Listens for `session.created` (mail toast) and `session.compacted` (briefing trigger)
2. **`experimental.chat.system.transform`** — Appends foundation, time context, and mail nudge to the system prompt
3. **`experimental.session.compacting`** — Injects preservation instructions into the compaction context

### Session Briefing Flow

```
Session compacted
  → Plugin reads messages from compacted session
  → Extracts structured summary (topics, decisions, tools used)
  → Creates ephemeral session
  → Prompts scribe agent to merge summary into briefing file
  → Cleans up ephemeral session
```

The briefing update is fire-and-forget — it runs asynchronously and does not block the main session.

## Requirements

- [OpenCode](https://github.com/opencode-ai/opencode) with plugin support
- Node.js runtime (for `Intl.DateTimeFormat` timezone support)
- For sister mail: a shared directory accessible by all participating agents
- For session briefing: a configured OpenCode agent capable of file editing

## License

MIT — Same as Kiseki.
