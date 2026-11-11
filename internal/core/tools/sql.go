package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/datasources"
)

// `sql` — loop's read-only SQL tool (tools/sql.ts): a saved datasource and
// one query, through the datasources package's two-layer read-only guard.

// noConnections tells the agent it can add a connection itself.
const noConnections = `No datasources are configured yet. You can add one yourself: read loop://docs/config.md ("Add a datasource") for the file location and JSON shape, write the connection, then ask the user to run /reload. (/datasource is the interactive equivalent, and is where the user tests a connection.)`

type sqlArgs struct {
	ConnectionID string `json:"connectionId"`
	Query        string `json:"query"`
}

// SQL returns the sql tool. Its description lists the connections as they
// are now; loop's exported copy carries the empty-home text.
func SQL() ai.Tool {
	def := LoopDef("sql")
	head, _, _ := strings.Cut(def.Description, "\n\n")
	return ai.NewToolWithSchema("sql", head+"\n\n"+connectionCatalog(), def.Schema, func(ctx context.Context, a sqlArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		id := strings.TrimSpace(a.ConnectionID)
		if id == "" {
			return fail(fmt.Errorf("connectionId is required. Known connections: %s", knownConnections()))
		}
		rows, err := datasources.Query(ctx, id, a.Query)
		if err != nil {
			return fail(err)
		}
		text, err := formatRows(rows)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(text), nil
	})
}

func connectionCatalog() string {
	sources := datasources.List()
	if len(sources) == 0 {
		return noConnections
	}
	lines := make([]string, len(sources))
	for i, s := range sources {
		lines[i] = fmt.Sprintf("  - %s (%s · %s/%s)", s.ID, s.Config.Type, s.Config.Host, s.Config.Database)
	}
	return "Available connectionId values:\n" + strings.Join(lines, "\n")
}

func knownConnections() string {
	sources := datasources.List()
	if len(sources) == 0 {
		return "(none configured — see loop://docs/config.md to add one)"
	}
	ids := make([]string, len(sources))
	for i, s := range sources {
		ids[i] = s.ID
	}
	return strings.Join(ids, ", ")
}

// formatRows is loop's: a count, then the rows as indented JSON.
func formatRows(rows []datasources.Row) (string, error) {
	if len(rows) == 0 {
		return "(0 rows)", nil
	}
	body, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d row(s):\n%s", len(rows), body), nil
}
