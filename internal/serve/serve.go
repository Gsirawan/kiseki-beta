package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/entity"
	"github.com/Gsirawan/kiseki-beta/internal/history"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/mailbox"
	"github.com/Gsirawan/kiseki-beta/internal/mark"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/report"
	"github.com/Gsirawan/kiseki-beta/internal/search"
	"github.com/Gsirawan/kiseki-beta/internal/status"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunMCPServer(db *sql.DB, ollama *ollama.OllamaClient, embedModel, prefix string, mb *mailbox.Mailbox) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    prefix,
		Version: "1.0.0",
	}, nil)

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_search",
		Description: "Search memories using multi-layer pipeline (entity expansion + FTS5 + vec_chunks + vec_messages). Returns chunks and messages ranked by layer overlap and similarity. IMPORTANT: When you find a relevant chunk, do NOT skim it. Use the Read tool to open the SourceFile and read the full section (matching SectionTitle) and its sub-sections before responding. The chunk is a pointer \u2014 the full context lives in the file.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "Search query"},
				"as_of": {"type": "string", "description": "Optional ISO date filter"},
			"limit": {"type": "integer", "description": "Maximum results (default 250)"}
			},
			"required": ["query"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		query, err := requiredStringArg(args, "query")
		if err != nil {
			return nil, err
		}
		asOf, err := optionalStringArg(args, "as_of")
		if err != nil {
			return nil, err
		}
		limit, ok, err := optionalIntArg(args, "limit")
		if err != nil {
			return nil, err
		}
		if !ok || limit <= 0 {
			limit = 250 // ceiling, not floor — specific queries return less, broad queries don't truncate
		}

		cfg := search.DefaultMultiLayerConfig()
		cfg.MinPerLayer = limit
		cfg.AsOf = asOf

		results, entityMatches, err := search.MultiLayerSearch(db, ollama, query, cfg)
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(results)
		if err != nil {
			return nil, err
		}

		var sb strings.Builder
		if len(entityMatches) > 0 {
			sb.WriteString("Entity matches: ")
			for i, m := range entityMatches {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(m.Alias)
				sb.WriteString(" → ")
				sb.WriteString(m.EntityName)
			}
			sb.WriteString("\n\n")
		}
		sb.Write(payload)

		whisper := "\n\n---\n⚡ Before responding: if any chunk above is relevant, READ the full section in its SourceFile (use Read tool with the file path). The chunk is a summary — the real context, nuance, and sub-sections live in the original file. Don't skim. Don't guess. Read it."

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: sb.String() + whisper},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_ingest",
		Description: "Ingest a markdown file into the memory store.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Path to markdown file"},
				"valid_at": {"type": "string", "description": "Optional ISO date for valid_at"}
			},
			"required": ["file_path"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		filePath, err := requiredStringArg(args, "file_path")
		if err != nil {
			return nil, err
		}
		if err := validateIngestPath(filePath); err != nil {
			return nil, err
		}
		validAt, err := optionalStringArg(args, "valid_at")
		if err != nil {
			return nil, err
		}

		result, err := ingest.IngestFile(db, ollama, filePath, validAt)
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_history",
		Description: "Fetch chronological history for an entity.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"entity": {"type": "string", "description": "Entity name"},
				"limit": {"type": "integer", "description": "Maximum results (default 250)"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		entity, err := requiredStringArg(args, "entity")
		if err != nil {
			return nil, err
		}
		limit, ok, err := optionalIntArg(args, "limit")
		if err != nil {
			return nil, err
		}
		if !ok || limit <= 0 {
			limit = 250
		}

		results, err := history.History(db, entity, limit)
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(results)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_search_msg",
		Description: "Search messages directly with context window. Returns conversation snippets around matching messages. Use for finding specific discussions or phrases.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "Search query"},
				"fts": {"type": "boolean", "description": "Use exact phrase matching (FTS5/LIKE) instead of semantic search"},
				"context": {"type": "integer", "description": "Context window in minutes (default 3)"},
				"limit": {"type": "integer", "description": "Maximum results (default 250)"}
			},
			"required": ["query"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		query, err := requiredStringArg(args, "query")
		if err != nil {
			return nil, err
		}
		useFTS, _, _ := optionalBoolArg(args, "fts")
		contextMins, ok, _ := optionalIntArg(args, "context")
		if !ok || contextMins <= 0 {
			contextMins = 3
		}
		limit, ok, _ := optionalIntArg(args, "limit")
		if !ok || limit <= 0 {
			limit = 250
		}

		// Entity expansion: replace aliases with canonical names before searching.
		searchQuery := query
		var entityMatches []entity.AliasMatch
		if eq, matches, err := entity.ExpandQuery(db, query); err == nil {
			searchQuery = eq
			entityMatches = matches
		}

		// Build entity match prefix for response (if any aliases were expanded).
		var entityPrefix string
		if len(entityMatches) > 0 {
			var sb strings.Builder
			sb.WriteString("Entity matches: ")
			for i, m := range entityMatches {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(m.Alias)
				sb.WriteString(" → ")
				sb.WriteString(m.EntityName)
			}
			sb.WriteString("\n\n")
			entityPrefix = sb.String()
		}

		if useFTS {
			results, err := dbpkg.SearchMessagesFTS(db, searchQuery, limit)
			if err != nil {
				return nil, err
			}
			payload, err := json.Marshal(results)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: entityPrefix + string(payload)},
				},
			}, nil
		}

		// Semantic search with context
		contexts, err := dbpkg.SearchMessagesWithContext(db, ollama, searchQuery, limit, contextMins)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(contexts)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: entityPrefix + string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_mark",
		Description: "Mark chunk or message importance for retrieval priority.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"type": {"type": "string", "description": "Target type: chunk or message"},
				"id": {"type": "string", "description": "Chunk ID (integer string) or message ID"},
				"importance": {"type": "string", "description": "Importance value: solution, key, or normal"}
			},
			"required": ["type", "id", "importance"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		targetType, err := requiredStringArg(args, "type")
		if err != nil {
			return nil, err
		}
		id, err := requiredStringArg(args, "id")
		if err != nil {
			return nil, err
		}
		importance, err := requiredStringArg(args, "importance")
		if err != nil {
			return nil, err
		}

		updated, err := mark.MarkImportance(db, targetType, id, importance)
		if err != nil {
			return nil, err
		}
		if !updated {
			return nil, fmt.Errorf("%s with id %s not found", targetType, id)
		}

		normalizedImportance, _ := mark.NormalizeImportance(importance)
		payload, err := json.Marshal(map[string]any{
			"type":       strings.ToLower(targetType),
			"id":         id,
			"importance": normalizedImportance,
			"updated":    true,
		})
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_status",
		Description: "Get system status and health details.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := status.Status(db, ollama, embedModel)

		payload, err := json.Marshal(status)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_list",
		Description: "List all ingested source files with chunk counts and date ranges.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}}
`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		files, err := dbpkg.ListFiles(db)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(files)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_stone_add",
		Description: "Create an explicit stone record for an important solution or decision.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {"type": "string", "description": "Stone title"},
				"category": {"type": "string", "description": "Category such as fix, decision, pattern"},
				"problem": {"type": "string", "description": "Problem statement"},
				"solution": {"type": "string", "description": "Solution statement"},
				"tags": {"type": "string", "description": "Comma-separated tags"},
				"chunk_ids": {"type": "string", "description": "Comma-separated related chunk/message IDs"},
				"key_chunk_ids": {"type": "string", "description": "Comma-separated key chunk/message IDs"},
				"source_session": {"type": "string", "description": "Source session id"}
			},
			"required": ["title"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}

		title, err := requiredStringArg(args, "title")
		if err != nil {
			return nil, err
		}
		category, _ := optionalStringArg(args, "category")
		problem, _ := optionalStringArg(args, "problem")
		solution, _ := optionalStringArg(args, "solution")
		tags, _ := optionalStringArg(args, "tags")
		chunkIDs, _ := optionalStringArg(args, "chunk_ids")
		keyChunkIDs, _ := optionalStringArg(args, "key_chunk_ids")
		sourceSession, _ := optionalStringArg(args, "source_session")

		stone, err := stone.CreateStone(db, stone.StoneInput{
			Title:         title,
			Category:      category,
			Problem:       problem,
			Solution:      solution,
			Tags:          tags,
			ChunkIDs:      chunkIDs,
			KeyChunkIDs:   keyChunkIDs,
			SourceSession: sourceSession,
		})
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(stone)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_stone_search",
		Description: "Search stones by title, tags, category, and/or week.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "Optional text query for title/tags/problem/solution"},
				"category": {"type": "string", "description": "Optional category filter"},
				"week": {"type": "string", "description": "Optional week filter (YYYY-Www)"},
				"limit": {"type": "integer", "description": "Maximum results (default 250)"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}

		query, _ := optionalStringArg(args, "query")
		category, _ := optionalStringArg(args, "category")
		week, _ := optionalStringArg(args, "week")
		limit, ok, _ := optionalIntArg(args, "limit")
		if !ok || limit <= 0 {
			limit = 250
		}

		stones, err := stone.SearchStones(db, stone.StoneSearchOptions{Query: query, Category: category, Week: week, Limit: limit})
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(stones)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_stone_read",
		Description: "Get full details for a stone by id.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Stone ID"}
			},
			"required": ["id"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		id, err := requiredStringArg(args, "id")
		if err != nil {
			return nil, err
		}

		stone, err := stone.GetStone(db, id)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(stone)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_stone_list",
		Description: "List all stones, optionally filtered by week.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"week": {"type": "string", "description": "Optional week filter (YYYY-Www)"},
				"limit": {"type": "integer", "description": "Maximum results (default 250)"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		week, _ := optionalStringArg(args, "week")
		limit, ok, _ := optionalIntArg(args, "limit")
		if !ok || limit <= 0 {
			limit = 250
		}

		stones, err := stone.ListStones(db, week, limit)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(stones)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_stone_delete",
		Description: "Delete a stone by id.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Stone ID"}
			},
			"required": ["id"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		id, err := requiredStringArg(args, "id")
		if err != nil {
			return nil, err
		}

		deleted, err := stone.DeleteStone(db, id)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(map[string]any{"id": id, "deleted": deleted})
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_forget",
		Description: "Remove chunks from memory. Specify exactly one of: file (delete by source file path), before (delete chunks with valid_at before date YYYY-MM-DD), or all (wipe entire database). Always shows what will be deleted before deleting.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file": {"type": "string", "description": "Delete all chunks from this source file path"},
				"before": {"type": "string", "description": "Delete all chunks with valid_at before this date (YYYY-MM-DD)"},
				"all": {"type": "boolean", "description": "Wipe entire database (use with caution)"},
				"confirm": {"type": "boolean", "description": "Must be true to execute deletion (safety gate)"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}

		fileArg, _ := optionalStringArg(args, "file")
		beforeArg, _ := optionalStringArg(args, "before")
		allArg, _, _ := optionalBoolArg(args, "all")
		confirm, _, _ := optionalBoolArg(args, "confirm")

		// Count how many flags are set
		set := 0
		if fileArg != "" {
			set++
		}
		if beforeArg != "" {
			set++
		}
		if allArg {
			set++
		}
		if set == 0 {
			return nil, fmt.Errorf("one of 'file', 'before', or 'all' is required")
		}
		if set > 1 {
			return nil, fmt.Errorf("only one of 'file', 'before', or 'all' may be specified")
		}

		// Preview mode: show what would be deleted without confirm=true
		if !confirm {
			var count int
			var description string
			switch {
			case fileArg != "":
				count, err = dbpkg.CountForgetByFile(db, fileArg)
				description = fmt.Sprintf("chunks from file: %s", fileArg)
			case beforeArg != "":
				count, err = dbpkg.CountForgetBefore(db, beforeArg)
				description = fmt.Sprintf("chunks with valid_at before %s", beforeArg)
			case allArg:
				err = db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count)
				description = "ALL chunks in the database"
			}
			if err != nil {
				return nil, fmt.Errorf("count chunks: %w", err)
			}
			msg := fmt.Sprintf("Preview: this would delete %d %s. Call again with confirm=true to execute.", count, description)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: msg}},
			}, nil
		}

		// Execute deletion
		var result dbpkg.ForgetResult
		switch {
		case fileArg != "":
			result, err = dbpkg.ForgetByFile(db, fileArg)
		case beforeArg != "":
			result, err = dbpkg.ForgetBefore(db, beforeArg)
		case allArg:
			result, err = dbpkg.ForgetAll(db)
		}
		if err != nil {
			return nil, fmt.Errorf("forget: %w", err)
		}

		payload, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_get_context",
		Description: "Retrieve conversation context around a specific message ID. Returns ordered messages from the same session within a time range. Use when you find a message ID in a chunk and need the full conversation thread.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"message_id": {"type": "string", "description": "Message ID (from <!-- msg:... --> in chunks)"},
				"range_minutes": {"type": "integer", "description": "Minutes before and after the message to include (default 5)"}
			},
			"required": ["message_id"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}
		messageID, err := requiredStringArg(args, "message_id")
		if err != nil {
			return nil, err
		}
		rangeMinutes, ok, err := optionalIntArg(args, "range_minutes")
		if err != nil {
			return nil, err
		}
		if !ok || rangeMinutes <= 0 {
			rangeMinutes = 5
		}

		msgs, err := dbpkg.GetMessageContext(db, messageID, rangeMinutes)
		if err != nil {
			return nil, err
		}

		payload, err := json.Marshal(msgs)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(payload)},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        prefix + "_report",
		Description: "Generate a weekly activity report from conversation history. Analyzes messages in the given time range to extract accomplishments, decisions, and files changed. Returns a formatted report (markdown, text, or JSON). Provide either a week (ISO format like 2026-W10) OR a from/to date range — not both.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"week": {"type": "string", "description": "ISO week to report on (YYYY-Wnn, e.g. 2026-W10)"},
				"from": {"type": "string", "description": "Start date (YYYY-MM-DD). Must be used with 'to'."},
				"to": {"type": "string", "description": "End date (YYYY-MM-DD). Must be used with 'from'."},
				"format": {"type": "string", "description": "Output format: markdown (default), text, or json"},
				"verbose": {"type": "boolean", "description": "Include detailed file paths and excerpts"},
				"summary_only": {"type": "boolean", "description": "Only include summary section, omit details"}
			}
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := argsOrEmpty(req)
		if err != nil {
			return nil, err
		}

		week, _ := optionalStringArg(args, "week")
		from, _ := optionalStringArg(args, "from")
		to, _ := optionalStringArg(args, "to")
		format, _ := optionalStringArg(args, "format")
		verbose, _, _ := optionalBoolArg(args, "verbose")
		summaryOnly, _, _ := optionalBoolArg(args, "summary_only")

		rendered, err := report.GenerateReport(db, week, from, to, format, verbose, summaryOnly)
		if err != nil {
			return nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: rendered},
			},
		}, nil
	})

	// Only add mail tools if mailbox is configured
	if mb != nil && len(mb.Peers) > 0 {
		peerNames := strings.Join(mb.Peers, ", ")

		server.AddTool(&mcp.Tool{
			Name:        prefix + "_send",
			Description: fmt.Sprintf("Send a message to %s. Use this to communicate across sessions.", peerNames),
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"message": {"type": "string", "description": "Message to send to ` + mb.Peers[0] + `"}
				},
				"required": ["message"]
			}`),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, err := argsOrEmpty(req)
			if err != nil {
				return nil, err
			}
			message, err := requiredStringArg(args, "message")
			if err != nil {
				return nil, err
			}

			// Send to first peer
			msg, err := mb.Send(mb.Peers[0], message)
			if err != nil {
				return nil, err
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Message sent to %s at %s (id: %s)", mb.Peers[0], msg.Timestamp.Format("15:04:05"), msg.ID[:8])},
				},
			}, nil
		})

		server.AddTool(&mcp.Tool{
			Name:        prefix + "_receive",
			Description: fmt.Sprintf("Check for new messages from %s. Returns unread messages and marks them as read.", peerNames),
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			messages, err := mb.Receive()
			if err != nil {
				return nil, err
			}

			if len(messages) == 0 {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: "No new messages."},
					},
				}, nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("You have %d new message(s):\n\n", len(messages)))
			for _, msg := range messages {
				sb.WriteString(fmt.Sprintf("From: %s\nTime: %s\nMessage: %s\n\n---\n\n",
					msg.From, msg.Timestamp.Format("2006-01-02 15:04:05"), msg.Message))
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: sb.String()},
				},
			}, nil
		})
	}
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func validateIngestPath(filePath string) error {
	cleaned := filepath.Clean(filePath)
	if filepath.IsAbs(cleaned) {
		root := os.Getenv("KISEKI_INGEST_ROOT")
		if root == "" {
			return fmt.Errorf("absolute paths require KISEKI_INGEST_ROOT to be set")
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("invalid KISEKI_INGEST_ROOT: %w", err)
		}
		if !strings.HasPrefix(cleaned, absRoot+string(filepath.Separator)) && cleaned != absRoot {
			return fmt.Errorf("path %q is outside allowed root %q", cleaned, absRoot)
		}
	} else if strings.Contains(cleaned, "..") {
		return fmt.Errorf("path %q contains directory traversal", filePath)
	}
	return nil
}

func argsOrEmpty(req *mcp.CallToolRequest) (map[string]any, error) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, err
	}
	if args == nil {
		return map[string]any{}, nil
	}
	return args, nil
}

func requiredStringArg(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument: %s", key)
	}
	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}
	return str, nil
}

func optionalStringArg(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", nil
	}
	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}
	return str, nil
}

func optionalBoolArg(args map[string]any, key string) (bool, bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return false, false, nil
	}
	b, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("argument %s must be a boolean", key)
	}
	return b, true, nil
}

func optionalIntArg(args map[string]any, key string) (int, bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return 0, true, fmt.Errorf("argument %s must be an integer", key)
		}
		return int(typed), true, nil
	case int:
		return typed, true, nil
	case int32:
		return int(typed), true, nil
	case int64:
		return int(typed), true, nil
	default:
		return 0, true, fmt.Errorf("argument %s must be an integer", key)
	}
}
