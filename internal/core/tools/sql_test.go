package tools

import (
	"encoding/json"
	"testing"

	"github.com/notshekhar/pi/internal/core/datasources"
)

// Rows keep the query's column order, as loop's JSON does.
func TestFormatRowsKeepsColumnOrder(t *testing.T) {
	out, err := formatRows([]datasources.Row{{Columns: []string{"name", "id"}, Values: []any{"a", 1}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "1 row(s):\n[\n  {\n    \"name\": \"a\",\n    \"id\": 1\n  }\n]"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if empty, _ := formatRows(nil); empty != "(0 rows)" {
		t.Errorf("empty = %q", empty)
	}
	var back []map[string]any
	if json.Unmarshal([]byte(out[len("1 row(s):\n"):]), &back) != nil {
		t.Error("rows are not valid JSON")
	}
}

// A mutation never reaches a database.
func TestSQLRefusesWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, failed := runTool(t, SQL(), map[string]any{"connectionId": "db", "query": "DELETE FROM users"})
	if !failed || out == "" {
		t.Errorf("got %q (failed=%v)", out, failed)
	}
}
