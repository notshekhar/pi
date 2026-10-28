package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/notshekhar/pi/internal/core/datasources"
	"github.com/notshekhar/pi/internal/tui"
)

// /datasource — loop's datasource-handlers.ts: the database connections the
// data-analyst agent's `sql` tool reads, each edited on one page of rows
// (fields, then test / save / delete), stored in ~/.loop/datasources.json.

var defaultDSPort = map[string]int{"postgres": 5432, "redshift": 5439, "mysql": 3306}

func describeDS(c datasources.Config) string {
	q := func(s string) string {
		if s == "" {
			return "?"
		}
		return s
	}
	ssl, secret := "", ""
	if c.SSL {
		ssl = " · ssl"
	}
	if c.Password == nil || *c.Password == "" {
		secret = " · no password"
	}
	return fmt.Sprintf("%s · %s@%s:%d/%s%s%s", c.Type, q(c.User), q(c.Host), c.Port, q(c.Database), ssl, secret)
}

func passwordLabel(p *string) string {
	switch {
	case p == nil || *p == "":
		return "(none)"
	case strings.HasPrefix(*p, "${env:"):
		return *p
	}
	return "(set)"
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (t *repl) ok(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotSuccess, text)) })
}

func (t *repl) manageDatasources() {
	go func() {
		last := 0
		for {
			items := []tui.Item{{Value: "+new", Label: "+ new datasource", Description: "add a postgres / mysql / redshift connection"}}
			for _, e := range datasources.List() {
				items = append(items, tui.Item{Value: e.ID, Label: e.ID, Description: describeDS(e.Config)})
			}
			if last >= len(items) {
				last = len(items) - 1
			}
			pick := t.app.Select("Datasources (Esc to close)", items, last, "")
			if pick == nil {
				return
			}
			last = indexOf(items, pick.Value)
			if pick.Value == "+new" {
				t.newDatasource()
			} else if c, ok := datasources.Get(pick.Value); ok {
				t.editDatasource(pick.Value, c, false)
			}
		}
	}()
}

func (t *repl) newDatasource() {
	if len(datasources.List()) >= datasources.Max {
		t.fail("maximum %d datasources reached — delete one first", datasources.Max)
		return
	}
	id := strings.TrimSpace(t.ask("connection id (e.g. warehouse)", ""))
	if id == "" {
		return
	}
	if !datasources.IsValidID(id) {
		t.fail("invalid id: %s (alphanumeric, dashes, ≤32 chars)", id)
		return
	}
	if _, exists := datasources.Get(id); exists {
		t.fail(`"%s" already exists`, id)
		return
	}
	t.editDatasource(id, datasources.Config{Type: "postgres", Host: "localhost", Port: 5432}, true)
}

func (t *repl) editDatasource(id string, c datasources.Config, isNew bool) {
	last := 0
	for {
		on := "off"
		if c.SSL {
			on = "on"
		}
		rows := []tui.Item{
			{Value: "type", Label: "type      " + c.Type, Description: "postgres / mysql / redshift"},
			{Value: "host", Label: "host      " + dash(c.Host)},
			{Value: "port", Label: "port      " + strconv.Itoa(c.Port)},
			{Value: "database", Label: "database  " + dash(c.Database)},
			{Value: "user", Label: "user      " + dash(c.User)},
			{Value: "password", Label: "password  " + passwordLabel(c.Password)},
			{Value: "ssl", Label: "ssl       " + on, Description: "toggle TLS"},
			{Value: "test", Label: "test connection", Description: "verify it connects (stays here to retry)"},
			{Value: "save", Label: "save", Description: describeDS(c)},
		}
		if !isNew {
			rows = append(rows, tui.Item{Value: "delete", Label: "delete", Description: "remove this connection"})
		}
		rows = append(rows, tui.Item{Value: "cancel", Label: "cancel", Description: "discard changes"})
		pick := t.app.Select(id+" — edit (Esc to cancel)", rows, last, "")
		if pick == nil || pick.Value == "cancel" {
			return
		}
		last = indexOf(rows, pick.Value)
		switch pick.Value {
		case "type":
			p := t.choose("Datasource type",
				tui.Item{Value: "postgres", Label: "postgres", Description: "PostgreSQL"},
				tui.Item{Value: "mysql", Label: "mysql", Description: "MySQL / MariaDB"},
				tui.Item{Value: "redshift", Label: "redshift", Description: "Amazon Redshift (postgres-compatible, TLS required)"})
			if p != nil {
				// The port follows the type only while it is still the old default.
				if c.Port == defaultDSPort[c.Type] {
					c.Port = defaultDSPort[p.Value]
				}
				c.Type = p.Value
			}
		case "host":
			c.Host = strings.TrimSpace(t.ask("host", c.Host))
		case "port":
			raw := strings.TrimSpace(t.ask("port", strconv.Itoa(c.Port)))
			if port, err := strconv.Atoi(leadingInt(raw)); err != nil || port <= 0 {
				t.fail("invalid port: %s", raw)
			} else {
				c.Port = port
			}
		case "database":
			c.Database = strings.TrimSpace(t.ask("database", c.Database))
		case "user":
			c.User = strings.TrimSpace(t.ask("user", c.User))
		case "password":
			cur := ""
			if c.Password != nil {
				cur = *c.Password
			}
			if v := strings.TrimSpace(t.ask("password (blank for none, or ${env:VAR})", cur)); v != "" {
				c.Password = &v
			} else {
				c.Password = nil
			}
		case "ssl":
			c.SSL = !c.SSL
		case "test":
			// In the background, so the form reopens at once; a snapshot, so
			// later edits do not change the test in flight.
			snap := c
			go func() {
				t.dim("testing %s %s:%d/%s…", snap.Type, snap.Host, snap.Port, snap.Database)
				if r := datasources.Test(snap); r.OK {
					t.ok("connection ok")
				} else {
					t.fail("connection failed: %s", r.Error)
				}
			}()
		case "save":
			if msg := missingDSField(c); msg != "" {
				t.fail("%s", msg)
				continue
			}
			if err := datasources.Save(id, c); err != nil {
				t.fail("%s", err)
				continue
			}
			t.dim(`datasource "%s" saved — %s`, id, describeDS(c))
			return
		case "delete":
			if _, err := datasources.Delete(id); err != nil {
				t.fail("%s", err)
				continue
			}
			t.dim(`datasource "%s" deleted`, id)
			return
		}
	}
}

// leadingInt is the digits parseInt would read.
func leadingInt(s string) string {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func missingDSField(c datasources.Config) string {
	switch {
	case strings.TrimSpace(c.Host) == "":
		return "host is required"
	case strings.TrimSpace(c.Database) == "":
		return "database is required"
	case strings.TrimSpace(c.User) == "":
		return "user is required"
	case c.Port <= 0:
		return "port must be a positive number"
	}
	return ""
}
