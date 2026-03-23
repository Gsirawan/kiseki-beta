# .githooks — kiseki-beta git hooks

## What's here

| File         | Purpose                                      |
|--------------|----------------------------------------------|
| `pre-commit` | Scans staged changes for personal data leaks |

---

## pre-commit: personal data leak scanner

Runs before every commit. Scans all staged *added lines* (not the whole file —
only what you're actually committing) against a set of patterns.

### What it blocks

| Category              | Examples caught                                              |
|-----------------------|--------------------------------------------------------------|
| Personal paths        | `/root/`, `/home/maximus/`, `/mnt/office/`                   |
| VPS IP addresses      | Known deployment server IPs                                  |
| Personal domains      | `deepnode.me` and subdomains                                 |
| Private repo URLs     | Gitea instance URLs                                          |
| Telegram bot tokens   | `digits:AAH...` format                                       |
| Hardcoded secrets     | `password=`, `api_key=`, `token=`, `secret=` with real values |
| Live env values       | `KISEKI_DB_KEY=<actual-value>`                               |
| Personal names in code| `Ghaith`, `Sirawan`, `Nadia` outside of comments            |

### What it does NOT block (allowlist)

- `*.example` files — placeholder values by design
- `README.md`, `LICENSE`, `docs/`, `*.md` — documentation may reference generic paths
- `*_test.go`, `*_test.py`, `testdata/`, `test/`, `tests/` — fake values by design
- This hook script itself
- Lines where secret-looking values are shell variable references (`$VAR`)
- Comment lines for personal name checks (`//`, `#`, `--`, `/*`)

---

## Installation

Run once per clone:

```bash
git config core.hooksPath .githooks
```

Verify it's active:

```bash
git config core.hooksPath
# should print: .githooks
```

---

## Emergency bypass

If you have a genuine reason to skip the check (e.g. the file is intentionally
archiving sanitized data and you've verified it manually):

```bash
SKIP_LEAK_CHECK=1 git commit -m "your message"
```

A warning is printed. This is logged in your terminal history. Use sparingly.

---

## Adding new patterns

Open `.githooks/pre-commit` and find the `PATTERNS=()` array.

Each entry follows the format:

```
"label:PCRE-regex"
```

Rules:
- Label must not contain `:` (it's the delimiter)
- Regex is PCRE (passed to `grep -P`)
- Do not put actual personal data in the regex — use character classes and length anchors
- If you're adding an allowlist exception for a whole file, add it to `is_allowed_file()`
- Document why you added the pattern or exception in a comment above the entry

Example — block a new environment variable key:

```bash
"env-new-key:NEW_SECRET_KEY\s*=\s*(?!\s*$)(?!your-)(?!<)[A-Za-z0-9_/+=-]{6,}"
```

Example — exempt a new directory:

```bash
fixtures/* ) return 0 ;;
```

---

## How it works internally

1. Collects staged files via `git diff --cached --name-only --diff-filter=ACM`
2. Skips files matching the allowlist
3. For each remaining file, extracts only the *added lines* from the staged diff
4. Runs each pattern against those lines using `grep -P` (PCRE)
5. For personal-name patterns, strips comment lines first to reduce false positives
6. Prints the matching lines and exits 1 if any pattern fires
