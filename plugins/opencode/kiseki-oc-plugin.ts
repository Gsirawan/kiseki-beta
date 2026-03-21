import type { Plugin } from "@opencode-ai/plugin";
import { readFileSync, readdirSync, existsSync, statSync, watch } from "fs";
import type { FSWatcher } from "fs";
import { join } from "path";

interface KisekiConfig {
  foundation_paths: string[];
  sister_mail_dir: string;
  enabled: {
    foundation: boolean;
    sister_mail: boolean;
    session_briefing: boolean;
    time_context: boolean;
  };
  briefing?: {
    agent: string;
    output_path: string;
  };
  time?: {
    timezone: string;
    locale?: string;
  };
  reminders_path?: string;
}

const loadConfig = (directory: string): KisekiConfig | null => {
  const configPath = join(directory, ".opencode", "kiseki.json");
  try {
    return JSON.parse(readFileSync(configPath, "utf-8"));
  } catch {
    return null;
  }
};

const DEFAULT_REMINDER =
  "No reminders configured. Set reminders_path in kiseki.json to load from a markdown file.";

let reminderCache: { lines: string[]; mtimeMs: number; size: number } | null =
  null;
let reminderWatcher: FSWatcher | null = null;
/** Epoch ms of the last time we actually stat'd + validated the reminder file */
let lastReminderRefreshMs = 0;
/** Poll interval — guarantees cache is re-validated at least every 10 s */
const REMINDER_POLL_INTERVAL_MS = 10_000;
/** Resolved path to the reminders file — set once from config */
let resolvedRemindersPath: string | null = null;

/**
 * Initialize a file watcher on the reminders file so edits are picked up immediately.
 * Called once lazily from loadReminders(). Safe to call multiple times — no-ops
 * after the first successful watch.
 */
const initReminderWatcher = (): void => {
  if (reminderWatcher || !resolvedRemindersPath) return; // already watching or no path
  try {
    reminderWatcher = watch(resolvedRemindersPath, (eventType) => {
      // Any change or rename → invalidate cache; next loadReminders() re-reads
      if (eventType === "change" || eventType === "rename") {
        reminderCache = null;
      }
    });
    // Don't let the watcher keep the process alive
    reminderWatcher.unref();
    // If the watcher errors out (file deleted, etc.), clean up gracefully
    reminderWatcher.on("error", () => {
      try {
        reminderWatcher?.close();
      } catch {
        /* ignore */
      }
      reminderWatcher = null;
    });
  } catch {
    // watch() unavailable or path invalid — fall back to mtime-only caching
    reminderWatcher = null;
  }
};

/**
 * Load reminder lines from the configured markdown file with file-watch +
 * mtime/size caching AND a 10-second polling fallback.
 *
 * Refresh strategy (layered):
 *   1. fs.watch  — instant invalidation on file change (fast path).
 *   2. 10 s poll — if ≥ REMINDER_POLL_INTERVAL_MS since last stat, force a
 *                  stat+reload cycle regardless of watcher state. This catches
 *                  cases where the watcher silently drops events.
 *   3. mtime/size — belt-and-suspenders: even within the 10 s window, a stat
 *                   that shows changed mtime/size triggers a reload.
 *
 * Falls back to the default reminder if the file is missing or unreadable.
 */
const loadReminders = (): string[] => {
  if (!resolvedRemindersPath) return [DEFAULT_REMINDER];

  // Lazily start the file watcher (once)
  initReminderWatcher();

  const now = Date.now();

  // If 10 s have elapsed since last refresh, invalidate cache so we
  // unconditionally re-stat the file below.
  if (now - lastReminderRefreshMs >= REMINDER_POLL_INTERVAL_MS) {
    reminderCache = null;
  }

  try {
    const stat = statSync(resolvedRemindersPath);

    // Record that we just performed a stat check
    lastReminderRefreshMs = now;

    if (
      reminderCache &&
      reminderCache.mtimeMs === stat.mtimeMs &&
      reminderCache.size === stat.size
    ) {
      return reminderCache.lines;
    }

    const raw = readFileSync(resolvedRemindersPath, "utf-8");
    const lines: string[] = [];

    for (const rawLine of raw.split("\n")) {
      const trimmed = rawLine.trim();
      // Skip empty lines and comments (lines starting with #)
      if (!trimmed || trimmed.startsWith("#")) continue;
      // Strip markdown bullet prefixes
      let line = trimmed;
      if (line.startsWith("- ")) {
        line = line.slice(2);
      } else if (line.startsWith("* ")) {
        line = line.slice(2);
      }
      if (line) lines.push(line);
    }

    // If file exists but has no valid lines, fall back to default
    if (lines.length === 0) {
      lines.push(DEFAULT_REMINDER);
    }

    reminderCache = { lines, mtimeMs: stat.mtimeMs, size: stat.size };
    return lines;
  } catch {
    // File missing or unreadable — use default
    lastReminderRefreshMs = now;
    return [DEFAULT_REMINDER];
  }
};

// ---------------------------------------------------------------------------
// Hijri (Islamic) Calendar Conversion
// ---------------------------------------------------------------------------
// Uses the Kuwaiti/tabular algorithm — a well-known arithmetic approximation
// of the Islamic calendar. Accuracy: ±1 day vs. observed dates (Muslims
// determine actual dates by moon sighting, so algorithmic precision beyond
// ±1 day is not meaningful). Zero external dependencies.
//
// Reference: https://en.wikipedia.org/wiki/Tabular_Islamic_calendar
// ---------------------------------------------------------------------------

const HIJRI_MONTH_NAMES = [
  "Muharram",
  "Safar",
  "Rabi al-Awwal",
  "Rabi al-Thani",
  "Jumada al-Ula",
  "Jumada al-Thani",
  "Rajab",
  "Shaban",
  "Ramadan",
  "Shawwal",
  "Dhul Qadah",
  "Dhul Hijjah",
];

/**
 * Convert a Gregorian date to an approximate Hijri date using the
 * Kuwaiti/tabular algorithm.
 *
 * @returns { day, month, year, monthName } — e.g. { day: 24, month: 9, year: 1447, monthName: "Ramadan" }
 */
const gregorianToHijri = (
  gYear: number,
  gMonth: number,
  gDay: number,
): { day: number; month: number; year: number; monthName: string } => {
  // Step 1: Convert Gregorian to Julian Day Number (JDN)
  const a = Math.floor((14 - gMonth) / 12);
  const y = gYear + 4800 - a;
  const m = gMonth + 12 * a - 3;
  const jdn =
    gDay +
    Math.floor((153 * m + 2) / 5) +
    365 * y +
    Math.floor(y / 4) -
    Math.floor(y / 100) +
    Math.floor(y / 400) -
    32045;

  // Step 2: Convert JDN to Hijri using the tabular Islamic calendar
  // Epoch: Julian Day of 1 Muharram 1 AH = 1948439.5 (we use integer 1948440)
  const l = jdn - 1948440 + 10632;
  const n = Math.floor((l - 1) / 10631);
  const remainder = l - 10631 * n + 354;
  const j =
    Math.floor((10985 - remainder) / 5316) *
      Math.floor((50 * remainder) / 17719) +
    Math.floor(remainder / 5670) *
      Math.floor((43 * remainder) / 15238);
  const adjustedRemainder =
    remainder -
    Math.floor((30 - j) / 15) *
      Math.floor((17719 * j) / 50) -
    Math.floor(j / 16) *
      Math.floor((15238 * j) / 43) +
    29;
  const hMonth = Math.floor((24 * adjustedRemainder) / 709);
  const hDay = adjustedRemainder - Math.floor((709 * hMonth) / 24);
  const hYear = 30 * n + j - 30;

  return {
    day: hDay,
    month: hMonth,
    year: hYear,
    monthName: HIJRI_MONTH_NAMES[hMonth - 1] ?? `Month ${hMonth}`,
  };
};

/**
 * Format a Hijri date string from a Date object in the given timezone.
 * Example output: "Islamic date: 24 Ramadan 1447 AH"
 */
const getHijriDateString = (date: Date, timezone: string): string => {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(date);

    const gYear = parseInt(parts.find((p) => p.type === "year")!.value);
    const gMonth = parseInt(parts.find((p) => p.type === "month")!.value);
    const gDay = parseInt(parts.find((p) => p.type === "day")!.value);

    const hijri = gregorianToHijri(gYear, gMonth, gDay);
    return `Islamic date: ${hijri.day} ${hijri.monthName} ${hijri.year} AH`;
  } catch {
    return "";
  }
};

const readFoundationFiles = (paths: string[]): string => {
  const contents: string[] = [];
  for (const p of paths) {
    try {
      const content = readFileSync(p, "utf-8");
      contents.push(`<!-- FILE: ${p} -->\n${content}`);
    } catch {
      // Skip missing files
    }
  }
  return contents.join("\n\n---\n\n");
};

const checkSisterMail = (
  mailDir: string,
): { count: number; preview: string } => {
  try {
    if (!existsSync(mailDir)) return { count: 0, preview: "" };

    const files = readdirSync(mailDir).filter((f) => f.endsWith(".json"));
    let unreadCount = 0;
    let preview = "";

    for (const file of files) {
      try {
        const msg = JSON.parse(readFileSync(join(mailDir, file), "utf-8"));
        if (!msg.read) {
          unreadCount++;
          if (!preview && msg.message) {
            preview =
              msg.message.slice(0, 100) +
              (msg.message.length > 100 ? "..." : "");
          }
        }
      } catch {
        // Skip invalid files
      }
    }

    return { count: unreadCount, preview };
  } catch {
    return { count: 0, preview: "" };
  }
};

/**
 * Extract a concise summary from session messages for the scribe agent.
 * Pulls out user requests, assistant decisions, tool usage, and key outcomes.
 */
const extractSessionSummary = (messages: any[]): string => {
  const sections: string[] = [];
  const topics: string[] = [];
  const decisions: string[] = [];
  const toolsUsed = new Set<string>();

  for (const msg of messages) {
    // SDK returns { info: { role, id, ... }, parts: [...] }
    const role = msg.info?.role ?? msg.role ?? msg.metadata?.role;
    const parts = msg.parts ?? [];

    for (const part of parts) {
      // SDK uses "text" for text parts
      const text = (part.text ?? part.content ?? "").trim();

      if (part.type === "text" && text) {
        if (role === "user") {
          // User messages = what was requested/discussed
          const truncated =
            text.length > 300 ? text.slice(0, 300) + "..." : text;
          topics.push(`- User: ${truncated}`);
        } else if (role === "assistant") {
          // Look for decision-like patterns in assistant responses
          const lines = text.split("\n");
          for (const line of lines) {
            const trimmed = line.trim();
            if (
              trimmed.match(
                /^(decided|decision|conclusion|summary|result|plan|approach):/i,
              ) ||
              trimmed.match(/^[-*]\s*(we|i) (decided|chose|went with|will)/i)
            ) {
              decisions.push(`- ${trimmed}`);
            }
          }
          // Keep a brief snippet of each assistant response
          if (text.length > 0) {
            const truncated =
              text.length > 200 ? text.slice(0, 200) + "..." : text;
            topics.push(`- Assistant: ${truncated}`);
          }
        }
      }

      // Track tool usage — check multiple possible field names
      if (
        part.type === "tool-invocation" ||
        part.type === "tool-call" ||
        part.type === "tool-use"
      ) {
        const toolName =
          part.toolName ?? part.name ?? part.tool ?? "unknown";
        toolsUsed.add(toolName);
      }
    }
  }

  if (topics.length > 0) {
    // Cap at 30 most recent exchanges for better coverage
    const recentTopics = topics.slice(-30);
    sections.push("## Discussion\n" + recentTopics.join("\n"));
  }

  if (decisions.length > 0) {
    sections.push("## Decisions\n" + decisions.join("\n"));
  }

  if (toolsUsed.size > 0) {
    sections.push("## Tools Used\n" + [...toolsUsed].join(", "));
  }

  sections.push(`\n_Total messages: ${messages.length}_`);

  return sections.join("\n\n") || "No meaningful content extracted.";
};

/**
 * Check if a given date falls within Ramadan for known years.
 * Uses approximate Gregorian date ranges (Ramadan shifts ~11 days earlier each year).
 */
const isRamadan = (date: Date, timezone: string): boolean => {
  try {
    // Get the date components in the configured timezone
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(date);

    const year = parseInt(parts.find((p) => p.type === "year")!.value);
    const month = parseInt(parts.find((p) => p.type === "month")!.value);
    const day = parseInt(parts.find((p) => p.type === "day")!.value);

    // Approximate Ramadan ranges (Gregorian) — add future years as needed
    const ramadanRanges: Record<number, { startMonth: number; startDay: number; endMonth: number; endDay: number }> = {
      2025: { startMonth: 3, startDay: 1, endMonth: 3, endDay: 30 },
      2026: { startMonth: 2, startDay: 18, endMonth: 3, endDay: 19 },
      2027: { startMonth: 2, startDay: 8, endMonth: 3, endDay: 9 },
      2028: { startMonth: 1, startDay: 28, endMonth: 2, endDay: 26 },
      2029: { startMonth: 1, startDay: 16, endMonth: 2, endDay: 14 },
    };

    const range = ramadanRanges[year];
    if (!range) return false;

    // Convert to comparable day-of-year-like number (month * 100 + day)
    const current = month * 100 + day;
    const start = range.startMonth * 100 + range.startDay;
    const end = range.endMonth * 100 + range.endDay;

    return current >= start && current <= end;
  } catch {
    return false;
  }
};

/**
 * Generate time context string for system prompt injection.
 * Uses Intl.DateTimeFormat with IANA timezone — zero external dependencies.
 */
const getTimeContext = (config: KisekiConfig): string | null => {
  try {
    const timezone = config.time?.timezone;
    if (!timezone) return null;

    const locale = config.time?.locale ?? "en-US";
    const now = new Date();

    // Format the full date/time string
    const formatter = new Intl.DateTimeFormat(locale, {
      timeZone: timezone,
      weekday: "long",
      year: "numeric",
      month: "long",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
      hour12: true,
    });
    const formattedTime = formatter.format(now);

    // Extract hour in the configured timezone to determine period of day
    const hourStr = new Intl.DateTimeFormat(locale, {
      timeZone: timezone,
      hour: "numeric",
      hour12: false,
    }).format(now);
    const hour = parseInt(hourStr);

    let period: string;
    if (hour >= 0 && hour <= 4) {
      period = "late night";
    } else if (hour >= 5 && hour <= 11) {
      period = "morning";
    } else if (hour >= 12 && hour <= 16) {
      period = "afternoon";
    } else if (hour >= 17 && hour <= 20) {
      period = "evening";
    } else {
      period = "night";
    }

    // Hijri (Islamic) calendar date
    const hijriStr = getHijriDateString(now, timezone);

    const reminders = loadReminders();

    const lines: string[] = [
      "<!-- KISEKI TIME CONTEXT -->",
      `Current time: ${formattedTime} (${timezone})`,
      `Period: ${period}`,
    ];

    if (hijriStr) {
      lines.push(hijriStr);
    }

    // Dynamic reminders from configured markdown file
    lines.push(...reminders);

    // Ramadan seasonal flag
    if (isRamadan(now, timezone)) {
      lines.push(
        "\u{1F319} Ramadan Mubarak \u{2014} be mindful of fasting hours and spiritual context.",
      );
    }

    lines.push("<!-- END KISEKI TIME CONTEXT -->");

    return lines.join("\n");
  } catch {
    return null;
  }
};

export const KisekiPlugin: Plugin = async ({ client, $, directory }) => {
  const config = loadConfig(directory);
  if (!config) return {};

  // Resolve the reminders file path from config (supports ~ expansion)
  if (config.reminders_path) {
    const rp = config.reminders_path;
    resolvedRemindersPath = rp.startsWith("~/")
      ? join(process.env.HOME ?? "/tmp", rp.slice(2))
      : rp;
  }

  let foundationCache: string | null = null;
  let sessionNotified = false;

  const log = async (message: string, extra?: Record<string, any>) => {
    await client.app.log({
      body: {
        service: "kiseki-plugin",
        level: "info",
        message,
        extra,
      },
    });
  };

  return {
    event: async ({ event }) => {
      // Sister mail toast on session start
      if (event.type === "session.created" && config.enabled.sister_mail) {
        const mail = checkSisterMail(config.sister_mail_dir);
        if (mail.count > 0) {
          await client.tui.toast.show({
            body: {
              title: `📬 Sister Mail: ${mail.count} message(s)`,
              message: mail.preview || "Check your mail!",
              type: "info",
            },
          });
          await log("Sister mail notification shown", { count: mail.count });
        }
        sessionNotified = true;
      }

      // Session briefing on compaction — fire-and-forget scribe agent flow
      if (
        event.type === "session.compacted" &&
        config.enabled.session_briefing &&
        config.briefing
      ) {
        const sessionID = (event as any).properties?.sessionID;
        if (!sessionID) {
          await log("Compaction event missing sessionID");
          return;
        }

        await log("Session compacted — triggering briefing update", {
          sessionID,
        });

        // Fire-and-forget: don't block the main session
        const briefingConfig = config.briefing;
        (async () => {
          let ephemeralSessionId: string | undefined;
          try {
            // 1. Read messages from the compacted session
            const messagesRes = await client.session.messages({
              path: { id: sessionID },
            });
            const messages = messagesRes.data ?? [];

            if (messages.length === 0) {
              await log("No messages in compacted session, skipping briefing", {
                sessionID,
              });
              return;
            }

            // 2. Extract key information from messages
            // Debug: AGGRESSIVE logging to find why extraction fails
            if (messages.length > 0) {
              const sample = messages[0];

              // Count roles across ALL messages
              const roleCounts: Record<string, number> = {};
              const partTypeCounts: Record<string, number> = {};
              let textPartsWithContent = 0;
              let textPartsEmpty = 0;

              for (const m of messages) {
                const r = String(m.info?.role ?? m.role ?? "MISSING");
                roleCounts[r] = (roleCounts[r] ?? 0) + 1;
                for (const p of (m.parts ?? [])) {
                  const pt = String(p.type ?? "NO_TYPE");
                  partTypeCounts[pt] = (partTypeCounts[pt] ?? 0) + 1;
                  if (pt === "text") {
                    const txt = (p.text ?? p.content ?? "");
                    if (typeof txt === "string" && txt.trim().length > 0) {
                      textPartsWithContent++;
                    } else {
                      textPartsEmpty++;
                    }
                  }
                }
              }

              // Sample first 5 messages with actual values
              const roleSamples = messages.slice(0, 5).map((m: any, i: number) => {
                const txt = m.parts?.[0]?.text;
                return {
                  idx: i,
                  role: m.info?.role,
                  roleType: typeof m.info?.role,
                  partCount: m.parts?.length ?? 0,
                  firstPartType: m.parts?.[0]?.type,
                  textType: typeof txt,
                  textLen: typeof txt === "string" ? txt.length : -1,
                  textPreview: typeof txt === "string" ? txt.slice(0, 100) : String(txt).slice(0, 100),
                };
              });

              await log("Message format debug — FULL CENSUS", {
                totalMessages: messages.length,
                roleCounts,
                partTypeCounts,
                textPartsWithContent,
                textPartsEmpty,
                roleSamples,
                infoKeys: sample.info ? Object.keys(sample.info) : [],
                firstPartKeys: sample.parts?.[0] ? Object.keys(sample.parts[0]) : [],
              });
            }

            const summary = extractSessionSummary(messages);

            await log("Session summary extracted", {
              sessionID,
              messageCount: messages.length,
              summaryLength: summary.length,
              summaryPreview: summary.slice(0, 300),
            });

            // 3. Create ephemeral session for the scribe agent
            const newSession = await client.session.create({
              body: { title: "briefing-update" },
            });
            ephemeralSessionId = newSession.data?.id;

            if (!ephemeralSessionId) {
              await log("Failed to create ephemeral session for briefing");
              return;
            }

            await log("Ephemeral session created for briefing", {
              ephemeralSessionId,
            });

            // 4. Prompt the scribe agent to update the briefing file
            await client.session.prompt({
              path: { id: ephemeralSessionId },
              body: {
                agent: briefingConfig.agent,
                parts: [
                  {
                    type: "text",
                    text: [
                      `A session was just compacted. Update the briefing file at: ${briefingConfig.output_path}`,
                      "",
                      "Here is a summary of what happened in the session:",
                      "",
                      summary,
                      "",
                      "Instructions:",
                      "- Read the current briefing file first",
                      "- Merge this new information into it — don't replace, update",
                      "- Preserve existing context that is still relevant",
                      "- Add new decisions, state changes, and open questions",
                      "- Remove anything that is now stale or resolved",
                      "- Keep it concise and scannable",
                    ].join("\n"),
                  },
                ],
              },
            });

            await log("Briefing update completed", {
              sessionID,
              ephemeralSessionId,
            });
          } catch (err) {
            await log("Briefing update failed", {
              sessionID,
              error: err instanceof Error ? err.message : String(err),
            }).catch(() => {});
          } finally {
            // 5. Clean up: delete the ephemeral session
            if (ephemeralSessionId) {
              try {
                await client.session.delete({
                  path: { id: ephemeralSessionId },
                });
                await log("Ephemeral briefing session cleaned up", {
                  ephemeralSessionId,
                });
              } catch (cleanupErr) {
                await log("Failed to clean up ephemeral session", {
                  ephemeralSessionId,
                  error:
                    cleanupErr instanceof Error
                      ? cleanupErr.message
                      : String(cleanupErr),
                }).catch(() => {});
              }
            }
          }
        })();
      }
    },

    "experimental.chat.system.transform": async (input, output) => {
      // Foundation injection
      if (config.enabled.foundation) {
        if (!foundationCache) {
          foundationCache = readFoundationFiles(config.foundation_paths);
          await log("Foundation loaded", { bytes: foundationCache.length });
        }

        if (foundationCache) {
          output.system.push(`
<!-- KISEKI FOUNDATION - Auto-injected by kiseki-oc-plugin -->
${foundationCache}
<!-- END KISEKI FOUNDATION -->
`);
        }
      }

      // Time context injection
      if (config.enabled.time_context && config.time?.timezone) {
        try {
          const timeContext = getTimeContext(config);
          if (timeContext) {
            output.system.push(timeContext);
          }
        } catch (err) {
          await log("Time context injection failed", {
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }

      if (config.enabled.sister_mail && !sessionNotified) {
        const mail = checkSisterMail(config.sister_mail_dir);
        if (mail.count > 0) {
          output.system.push(`
<!-- SISTER MAIL NOTIFICATION -->
📬 You have ${mail.count} unread message(s)! Call the receive tool to read them.
<!-- END SISTER MAIL -->
`);
        }
        sessionNotified = true;
      }
    },

    "experimental.session.compacting": async (input, output) => {
      if (!config.enabled.foundation || !foundationCache) return;

      output.context.push(`
IMPORTANT: Preserve identity context. The foundation files define who the agent is.
Do not summarize away identity, relationships, or emotional context.
`);
      await log("Compaction context added");
    },
  };
};

export default KisekiPlugin;
