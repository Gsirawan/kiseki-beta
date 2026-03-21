# Weekly Review Process

Run this at the end of each week to extract solutions and decisions from conversations, then mark chunks by importance.

**The core idea:** pattern detection, not LLM judgment. LLMs can't reliably tell you which chunk was "most useful." But they can spot code blocks after long discussions, messages containing "fixed" or "worked", and chunks near the end of a session. That's mechanical. That works.

---

## When to Run

End of week: Friday evening or Sunday before the new week starts.

Trigger manually via a subagent, or schedule it.

---

## The Process

### Step 1: Pull the week's chunks

```bash
kiseki search-msg --json "2026-W08"
```

Or use a date range:

```bash
kiseki search-msg --json --from 2026-02-17 --to 2026-02-23
```

### Step 2: Group by session

Chunks sharing a session ID belong to the same conversation. Process each session independently.

### Step 3: Score each candidate chunk

This is mechanical pattern detection. Apply the scoring table:

| Signal | Score | Why It Works |
|--------|-------|--------------|
| Code block appears after 10+ messages in session | +2 | Long discussion resolved into answer |
| Contains "fixed", "worked", "solution", "done", "✅" | +1 | Explicit solution language |
| Chunk is in the last 3-5 messages of a session | +2 | Final answer lives here |
| Short dense message after a long back-and-forth | +1 | Compression = answer |
| File path + code block in the same chunk | +2 | Concrete change was made |

**Thresholds:**

- Score ≥ 4 → mark as `solution`
- Score ≥ 2 → mark as `key`
- Score < 2 → leave as `normal`

### Step 4: Mark the chunks

```bash
kiseki mark message <id> solution
kiseki mark message <id> key
```

### Step 5: Create stones for major solutions (optional)

For significant fixes or decisions, create an explicit stone record (Phase 4 feature). This promotes the solution from a marked chunk into a permanent, searchable record.

---

## What the Review Agent Does

When you ask a subagent to run the weekly review, it follows this sequence:

1. Fetch all chunks from the specified week using `search-msg --json`
2. Group chunks by session ID
3. For each session, identify candidates:
   - Last 3-5 chunks (resolution usually lands here)
   - Any chunk containing a code block
   - Any chunk containing solution words: "fixed", "worked", "solution", "done", "resolved", "✅"
4. Apply the scoring formula to each candidate
5. Mark chunks that cross the thresholds
6. Report the summary

She does not read chunks for meaning. She scans for signals.

---

## Important Notes

**Pattern detection, not judgment.** The scoring formula is the whole system. Don't ask the agent to "understand what was most helpful" — that produces noise. Ask her to detect patterns.

**Recency bias is a feature.** The last message in a conversation about a problem is almost always the fix. Weighting end-of-session chunks heavily is correct.

**False positives are fine.** Marking a `key` chunk that turns out to be ordinary costs nothing. Missing a `solution` chunk costs retrieval quality. Err toward marking.

---

## Example Output

```
Weekly Review: 2026-W08

Sessions analyzed: 12
Chunks scanned: 234

Solutions found (marked):
- msg_abc123: "Azure auth fix - AllowedUsers config"
- msg_def456: "GraphRAG tool missing from agent prompt"
- msg_ghi789: "Kiseki rename to avoid Greek goddess"

Key chunks found (marked):
- msg_jkl012: "Decided to use Core42 GPT-4.1"
- msg_mno345: "Parser rewrite approach"

Total: 3 solutions, 2 key chunks marked
```

---

## Quick Reference

```bash
# Pull week's chunks
kiseki search-msg --json "2026-W08"

# Mark a solution
kiseki mark message msg_abc123 solution

# Mark a key chunk
kiseki mark message msg_abc123 key

# Verify marks
kiseki search-msg --json --importance solution
```
