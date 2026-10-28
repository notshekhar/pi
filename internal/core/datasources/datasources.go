// Package datasources is loop's datasources (datasources/*): saved database
// connections in ~/.loop/datasources.json for the data-analyst agent's `sql`
// tool, a two-layer read-only guard, and connection tests.
package datasources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// Config is loop's DataSourceConfig.
type Config struct {
	Type     string  `json:"type"` // postgres | mysql | redshift
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Database string  `json:"database"`
	User     string  `json:"user"`
	Password *string `json:"password,omitempty"`
	SSL      bool    `json:"ssl,omitempty"`
}

// Max is loop's MAX_DATASOURCES.
const Max = 50

var validID = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_-]{0,31}$`)

// IsValidID is loop's isValidConnectionId.
func IsValidID(id string) bool { return validID.MatchString(id) }

func all() map[string]Config {
	out := map[string]Config{}
	config.DatasourcesStore().Get("connections", &out)
	return out
}

// Entry is one saved connection.
type Entry struct {
	ID     string
	Config Config
}

// List is every connection by id.
func List() []Entry {
	out := []Entry{}
	for id, c := range all() {
		out = append(out, Entry{id, c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get is one connection.
func Get(id string) (Config, bool) {
	c, ok := all()[id]
	return c, ok
}

// Save creates or overwrites a connection.
func Save(id string, c Config) error {
	if !IsValidID(id) {
		return fmt.Errorf("invalid connection id: %s", id)
	}
	conns := all()
	if _, exists := conns[id]; !exists && len(conns) >= Max {
		return fmt.Errorf("maximum %d datasources allowed — delete one first", Max)
	}
	ClosePool(id)
	return config.DatasourcesStore().Mutate(func(o *jsonstore.Object) {
		inner := jsonstore.NewObject()
		if raw := o.Raw("connections"); raw != nil {
			if parsed, err := jsonstore.ParseObject(raw); err == nil {
				inner = parsed
			}
		}
		_ = inner.Set(id, c)
		raw, _ := inner.MarshalJSON()
		o.SetRaw("connections", raw)
	})
}

// Delete removes a connection; false when it did not exist.
func Delete(id string) (bool, error) {
	if _, ok := all()[id]; !ok {
		return false, nil
	}
	ClosePool(id)
	err := config.DatasourcesStore().Mutate(func(o *jsonstore.Object) {
		if raw := o.Raw("connections"); raw != nil {
			if inner, err := jsonstore.ParseObject(raw); err == nil {
				inner.Delete(id)
				out, _ := inner.MarshalJSON()
				o.SetRaw("connections", out)
			}
		}
	})
	return err == nil, err
}

var envPlaceholder = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolveSecrets expands ${env:VAR} placeholders.
func ResolveSecrets(v string) string {
	return envPlaceholder.ReplaceAllStringFunc(v, func(m string) string {
		return os.Getenv(envPlaceholder.FindStringSubmatch(m)[1])
	})
}

var defaultPorts = map[string]int{"postgres": 5432, "mysql": 3306, "redshift": 5439}

// Parse is loop's parseDatasourceConfig (wire validation).
func Parse(input any) (Config, error) {
	raw, ok := input.(map[string]any)
	if !ok {
		return Config{}, errors.New("datasource config required")
	}
	s := func(k string) string {
		if v, ok := raw[k]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	c := Config{Type: s("type"), Host: s("host"), Database: s("database"), User: s("user")}
	if _, ok := defaultPorts[c.Type]; !ok {
		return Config{}, fmt.Errorf("unknown datasource type: %v (expected postgres, mysql, redshift)", raw["type"])
	}
	if c.Host == "" {
		return Config{}, errors.New("a datasource needs a host")
	}
	if c.Database == "" {
		return Config{}, errors.New("a datasource needs a database")
	}
	if c.User == "" {
		return Config{}, errors.New("a datasource needs a user")
	}
	switch p := raw["port"].(type) {
	case nil:
		c.Port = defaultPorts[c.Type]
	case string:
		if p == "" {
			c.Port = defaultPorts[c.Type]
		} else if _, err := fmt.Sscanf(p, "%d", &c.Port); err != nil || fmt.Sprint(c.Port) != p {
			return Config{}, fmt.Errorf("not a valid port: %v", p)
		}
	case float64:
		c.Port = int(p)
		if float64(c.Port) != p {
			return Config{}, fmt.Errorf("not a valid port: %v", p)
		}
	default:
		return Config{}, fmt.Errorf("not a valid port: %v", p)
	}
	if c.Port < 1 || c.Port > 65535 {
		return Config{}, fmt.Errorf("not a valid port: %v", raw["port"])
	}
	if pw, ok := raw["password"]; ok && pw != nil {
		v := fmt.Sprint(pw)
		c.Password = &v
	}
	c.SSL = raw["ssl"] == true
	return c, nil
}

// ─── connections ─────────────────────────────────────────────────────────

func open(c Config) (*sql.DB, string, error) {
	pw := ""
	if c.Password != nil {
		pw = ResolveSecrets(*c.Password)
	}
	tls := c.SSL || c.Type == "redshift"
	if c.Type == "mysql" {
		cfg := mysqldriver.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net = c.User, pw, "tcp"
		cfg.Addr = fmt.Sprintf("%s:%d", c.Host, c.Port)
		cfg.DBName = c.Database
		cfg.Timeout = 10 * time.Second
		if tls {
			cfg.TLSConfig = "skip-verify"
		}
		db, err := sql.Open("mysql", cfg.FormatDSN())
		return db, "mysql", err
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, pw), Host: fmt.Sprintf("%s:%d", c.Host, c.Port), Path: "/" + c.Database}
	q := url.Values{"connect_timeout": {"10"}}
	if tls {
		q.Set("sslmode", "require")
	} else {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	return db, "postgres", err
}

var (
	poolsMu sync.Mutex
	pools   = map[string]*sql.DB{}
)

func pool(id string, c Config) (*sql.DB, error) {
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[id]; ok {
		return p, nil
	}
	db, _, err := open(c)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	pools[id] = db
	return db, nil
}

// ClosePool drops a cached pool.
func ClosePool(id string) {
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[id]; ok {
		_ = p.Close()
		delete(pools, id)
	}
}

// CloseAll closes every pool.
func CloseAll() {
	poolsMu.Lock()
	defer poolsMu.Unlock()
	for id, p := range pools {
		_ = p.Close()
		delete(pools, id)
	}
}

// TestResult is loop's TestResult.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Test probes a (possibly unsaved) config with a throwaway connection.
func Test(c Config) TestResult {
	db, _, err := open(c)
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "SELECT 1"); err != nil {
		if ctx.Err() != nil {
			return TestResult{Error: "timed out after 15s"}
		}
		return TestResult{Error: err.Error()}
	}
	return TestResult{OK: true}
}

// ─── read-only guard (validate.ts) ───────────────────────────────────────

const readOnlyError = "insert/update/alter operations are not allowed"

var (
	allowedLeading = map[string]bool{"select": true, "with": true, "explain": true, "show": true, "describe": true, "desc": true, "values": true, "table": true}
	forbidden      = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|upsert|replace|drop|alter|create|truncate|grant|revoke|call|do|copy|lock|pg_read_file|pg_read_binary_file|pg_ls_dir|lo_import|lo_export)\b`)
	intoFile       = regexp.MustCompile(`(?i)\binto\s+(out|dump)file\b`)
	leadingWord    = regexp.MustCompile(`(?i)^[(\s]*([a-z]+)`)
	stripRules     = []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`/\*[\s\S]*?\*/`), " "},
		{regexp.MustCompile(`--[^\n]*`), " "},
		{regexp.MustCompile(`#[^\n]*`), " "},
		{regexp.MustCompile(`'(?:[^'\\]|\\.|'')*'`), " '' "},
		{regexp.MustCompile(`"(?:[^"\\]|\\.|"")*"`), ` "" `},
		{regexp.MustCompile("`(?:[^`]|``)*`"), " `` "},
	}
)

// AssertReadOnly is layer 1 of the read-only guard.
func AssertReadOnly(query string) error {
	s := query
	for _, r := range stripRules {
		s = r.re.ReplaceAllString(s, r.with)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("empty query")
	}
	n := 0
	for _, st := range strings.Split(s, ";") {
		if strings.TrimSpace(st) != "" {
			n++
		}
	}
	if n > 1 {
		return errors.New("only a single SQL statement is allowed")
	}
	m := leadingWord.FindStringSubmatch(s)
	if m == nil || !allowedLeading[strings.ToLower(m[1])] {
		return errors.New(readOnlyError)
	}
	if forbidden.MatchString(s) || intoFile.MatchString(s) {
		return errors.New(readOnlyError)
	}
	return nil
}

// Query runs a validated query in a rolled-back READ ONLY transaction
// (layer 2) and returns the rows as objects.
func Query(ctx context.Context, id, query string) ([]map[string]any, error) {
	if err := AssertReadOnly(query); err != nil {
		return nil, err
	}
	c, ok := Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown datasource: %s — add it with /datasource", id)
	}
	db, err := pool(id, c)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	begin := "BEGIN READ ONLY"
	if c.Type == "mysql" {
		begin = "START TRANSACTION READ ONLY"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
