package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	faktory "github.com/sderosiaux/faktory"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer(
		"faktory",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// --- memory_add ---
	s.AddTool(
		mcp.NewTool("memory_add",
			mcp.WithDescription("Extract and store facts and entity relations from a conversation"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("messages", mcp.Required(), mcp.Description("Conversation messages as JSON array of {role, content}")),
		),
		handleAdd,
	)

	// --- memory_search ---
	s.AddTool(
		mcp.NewTool("memory_search",
			mcp.WithDescription("Search facts by semantic similarity"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 10)")),
		),
		handleSearch,
	)

	// --- memory_recall ---
	s.AddTool(
		mcp.NewTool("memory_recall",
			mcp.WithDescription("Retrieve relevant facts and relations in one call, with a pre-formatted summary"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithNumber("max_facts", mcp.Description("Max facts (default 10)")),
			mcp.WithNumber("max_relations", mcp.Description("Max relations (default 10)")),
			mcp.WithBoolean("include_profile", mcp.Description("Prepend a generated user profile summary (default false)")),
		),
		handleRecall,
	)

	// --- memory_profile ---
	s.AddTool(
		mcp.NewTool("memory_profile",
			mcp.WithDescription("Generate a concise user profile summary from all stored facts and relations"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
		),
		handleProfile,
	)

	// --- memory_get_all ---
	s.AddTool(
		mcp.NewTool("memory_get_all",
			mcp.WithDescription("Get all stored facts for a user"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 100)")),
		),
		handleGetAll,
	)

	// --- memory_search_relations ---
	s.AddTool(
		mcp.NewTool("memory_search_relations",
			mcp.WithDescription("Search entity relations by semantic similarity"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 10)")),
		),
		handleSearchRelations,
	)

	// --- memory_delete ---
	s.AddTool(
		mcp.NewTool("memory_delete",
			mcp.WithDescription("Delete a specific fact by ID"),
			mcp.WithString("fact_id", mcp.Required(), mcp.Description("Fact ID to delete")),
		),
		handleDelete,
	)

	// --- memory_delete_all ---
	s.AddTool(
		mcp.NewTool("memory_delete_all",
			mcp.WithDescription("Delete all facts, entities, and relations for a user"),
			mcp.WithString("user_id", mcp.Required(), mcp.Description("User ID")),
		),
		handleDeleteAll,
	)

	if err := server.ServeStdio(s); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}

func withMemory(fn func(mem *faktory.Memory) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	mem, err := faktory.New(faktory.LoadConfig())
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("init error: %v", err)), nil
	}
	defer mem.Close()
	return fn(mem)
}

func handleAdd(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	msgsJSON := req.GetString("messages", "")
	if userID == "" || msgsJSON == "" {
		return mcp.NewToolResultError("user_id and messages are required"), nil
	}

	var messages []faktory.Message
	if err := json.Unmarshal([]byte(msgsJSON), &messages); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid messages JSON: %v", err)), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		result, err := mem.Add(ctx, messages, userID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(out)), nil
	})
}

func handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	query := req.GetString("query", "")
	limit := int(req.GetFloat("limit", 10))
	if userID == "" || query == "" {
		return mcp.NewToolResultError("user_id and query are required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		facts, err := mem.Search(ctx, query, userID, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(facts)
		return mcp.NewToolResultText(string(out)), nil
	})
}

func handleRecall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	query := req.GetString("query", "")
	if userID == "" || query == "" {
		return mcp.NewToolResultError("user_id and query are required"), nil
	}

	maxFacts := int(req.GetFloat("max_facts", 10))
	maxRels := int(req.GetFloat("max_relations", 10))
	includeProfile := req.GetBool("include_profile", false)

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		result, err := mem.Recall(ctx, query, userID, &faktory.RecallOptions{
			MaxFacts:       maxFacts,
			MaxRelations:   maxRels,
			IncludeProfile: includeProfile,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if result.Summary == "" {
			return mcp.NewToolResultText("No relevant memories found."), nil
		}
		return mcp.NewToolResultText(result.Summary), nil
	})
}

func handleGetAll(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	limit := int(req.GetFloat("limit", 100))
	if userID == "" {
		return mcp.NewToolResultError("user_id is required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		facts, err := mem.GetAll(ctx, userID, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var sb strings.Builder
		for _, f := range facts {
			fmt.Fprintf(&sb, "- %s\n", f.Text)
		}
		if sb.Len() == 0 {
			return mcp.NewToolResultText("No facts stored."), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func handleSearchRelations(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	query := req.GetString("query", "")
	limit := int(req.GetFloat("limit", 10))
	if userID == "" || query == "" {
		return mcp.NewToolResultError("user_id and query are required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		rels, err := mem.SearchRelations(ctx, query, userID, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var sb strings.Builder
		for _, r := range rels {
			fmt.Fprintf(&sb, "%s --%s--> %s\n", r.Source, r.Relation, r.Target)
		}
		if sb.Len() == 0 {
			return mcp.NewToolResultText("No matching relations found."), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func handleProfile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	if userID == "" {
		return mcp.NewToolResultError("user_id is required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		profile, err := mem.Profile(ctx, userID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if profile == "" {
			return mcp.NewToolResultText("No facts stored for this user."), nil
		}
		return mcp.NewToolResultText(profile), nil
	})
}

func handleDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	factID := req.GetString("fact_id", "")
	if factID == "" {
		return mcp.NewToolResultError("fact_id is required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		if err := mem.Delete(ctx, factID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("deleted"), nil
	})
}

func handleDeleteAll(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := req.GetString("user_id", "")
	if userID == "" {
		return mcp.NewToolResultError("user_id is required"), nil
	}

	return withMemory(func(mem *faktory.Memory) (*mcp.CallToolResult, error) {
		if err := mem.DeleteAll(ctx, userID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("deleted all data for user"), nil
	})
}
