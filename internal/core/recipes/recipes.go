// Package recipes is loop's recipes (recipes/*): personal, editable workflows
// in ~/.loop/recipes/<name>.md with {{input}} placeholders, extracted from a
// finished session and run as an ordinary prompt.
package recipes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/home"
)

// Recipe is one saved workflow.
type Recipe struct {
	Name   string
	Body   string
	Inputs []string
	Path   string
}

var (
	placeholder = regexp.MustCompile(`\{\{([a-zA-Z][a-zA-Z0-9_]*)\}\}`)
	nameRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	fileRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}\.md$`)
	keyValue    = regexp.MustCompile(`(?s)^([a-zA-Z][a-zA-Z0-9_]*)=(.*)$`)
)

// Inputs are the distinct placeholders, in order of first use.
func Inputs(body string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range placeholder.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ValidateName is loop's validateRecipeName.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return errors.New("Recipe names use lowercase letters, numbers and dashes (1–64 characters).")
	}
	return nil
}

// ErrNotFound is a recipe that does not exist.
var ErrNotFound = errors.New("Recipe not found. Use /recipe list to see saved workflows.")

// Store is ~/.loop/recipes.
type Store struct{ Dir string }

// NewStore is the store under ~/.loop.
func NewStore() *Store { return &Store{Dir: home.Path("recipes")} }

func (s *Store) path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, name+".md"), nil
}

// Read loads a recipe.
func (s *Store) Read(name string) (Recipe, error) {
	path, err := s.path(name)
	if err != nil {
		return Recipe{}, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Recipe{}, ErrNotFound
	}
	if err != nil {
		return Recipe{}, err
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return Recipe{}, fmt.Errorf("Recipe %q is empty — edit %s.", name, path)
	}
	return Recipe{Name: name, Body: body, Inputs: Inputs(body), Path: path}, nil
}

// Names lists saved recipes, sorted.
func (s *Store) Names() []string {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && fileRE.MatchString(e.Name()) {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// Save writes a recipe; without replace an existing one is refused.
func (s *Store) Save(name, body string, replace bool) (Recipe, error) {
	path, err := s.path(name)
	if err != nil {
		return Recipe{}, err
	}
	if strings.TrimSpace(body) == "" {
		return Recipe{}, errors.New("A recipe cannot be empty.")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Recipe{}, err
	}
	data := []byte(strings.TrimSpace(body) + "\n")
	if !replace {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return Recipe{}, err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return Recipe{}, err
		}
	} else {
		tmp, err := os.CreateTemp(s.Dir, name+".*.tmp")
		if err != nil {
			return Recipe{}, err
		}
		_, werr := tmp.Write(data)
		tmp.Chmod(0o600)
		tmp.Close()
		if werr != nil {
			os.Remove(tmp.Name())
			return Recipe{}, werr
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return Recipe{}, err
		}
	}
	return s.Read(name)
}

// Remove deletes a recipe.
func (s *Store) Remove(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); os.IsNotExist(err) {
		return ErrNotFound
	} else {
		return err
	}
}

// ParseArgs is loop's parseRecipeArgs: quotes group values; nothing here
// evaluates shell syntax.
func ParseArgs(raw string) ([]string, error) {
	var args []string
	var value strings.Builder
	quote := rune(0)
	started := false
	rs := []rune(raw)
	isSpace := func(c rune) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' }
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && quote != '\'' && i+1 < len(rs) && (isSpace(rs[i+1]) || rs[i+1] == '\'' || rs[i+1] == '"' || rs[i+1] == '\\'):
			i++
			value.WriteRune(rs[i])
			started = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				value.WriteRune(c)
			}
		case c == '"' || c == '\'':
			quote, started = c, true
		case isSpace(c):
			if started {
				args = append(args, value.String())
			}
			value.Reset()
			started = false
		default:
			value.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, errors.New("Unclosed quote in recipe arguments.")
	}
	if started {
		args = append(args, value.String())
	}
	return args, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// BindInputs is loop's bindRecipeInputs: name=value in any order, positional
// values fill the rest in placeholder order.
func BindInputs(r Recipe, args []string) (map[string]string, error) {
	values := map[string]string{}
	var positional []string
	for _, a := range args {
		m := keyValue.FindStringSubmatch(a)
		if m == nil {
			positional = append(positional, a)
			continue
		}
		if !contains(r.Inputs, m[1]) {
			return nil, fmt.Errorf("Unknown input %q. Inputs: %s.", m[1], orNone(r.Inputs))
		}
		if _, dup := values[m[1]]; dup {
			return nil, fmt.Errorf("Input %q was supplied twice.", m[1])
		}
		values[m[1]] = m[2]
	}
	var remaining []string
	for _, k := range r.Inputs {
		if _, ok := values[k]; !ok {
			remaining = append(remaining, k)
		}
	}
	if len(positional) > len(remaining) {
		return nil, fmt.Errorf("Too many values. Inputs: %s.", orNone(r.Inputs))
	}
	for i, v := range positional {
		values[remaining[i]] = v
	}
	return values, nil
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// Render is loop's renderRecipe: the prompt a run submits.
func Render(r Recipe, values map[string]string) (string, error) {
	var missing []string
	for _, k := range r.Inputs {
		if strings.TrimSpace(values[k]) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("Missing recipe inputs: %s.", strings.Join(missing, ", "))
	}
	body := placeholder.ReplaceAllStringFunc(r.Body, func(m string) string {
		return values[placeholder.FindStringSubmatch(m)[1]]
	})
	return fmt.Sprintf("Run the saved recipe %q in the current workspace. Inspect the current code before adapting its steps. Follow workspace instructions and the active agent's permissions. Check the stated success criteria and report what was verified.\n\n%s", r.Name, body), nil
}

// Generator writes a draft from a conversation (agent.GenerateRecipe).
type Generator func(ctx context.Context, name, focus string) (string, error)
