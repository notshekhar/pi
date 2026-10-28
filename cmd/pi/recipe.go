package main

import (
	"context"
	"errors"
	"strings"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/recipes"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/tui"
)

// /recipe — loop's recipe-handlers.ts: save the finished workflow of this
// session as ~/.loop/recipes/<name>.md, review and edit it, and run it later
// with values for its {{inputs}}.

const recipeHelp = "Recipes: /recipe save <name> [what should vary] · /recipe list · /recipe show|edit|rm <name> · /recipe <name> [values or input=value]. Quote values containing spaces. Use /recipe run <name> for names matching a subcommand."

func (t *repl) recipeCmd(ctx context.Context, raw string) {
	go func() {
		prompt, err := t.recipeFlow(ctx, strings.TrimSpace(raw))
		switch {
		case errors.Is(err, recipes.ErrNotFound):
			t.dim("%s", err)
		case err != nil:
			t.fail("%s", err)
		case prompt != "":
			// The normal turn path owns permissions, tools and persistence.
			t.submit(ctx, prompt)
		}
	}()
}

func (t *repl) recipePreview(name, body string) {
	t.do(func() { t.app.PrintDraft("Recipe draft: "+name, body) })
}

// recipeReview loops preview → save / edit / cancel; "" is a cancel.
func (t *repl) recipeReview(name, body string) string {
	for {
		t.recipePreview(name, body)
		inputs := recipes.Inputs(body)
		choice := t.choose("Recipe: "+name,
			tui.Item{Value: "save", Label: "save recipe", Description: "inputs: " + joinOrNone(inputs)},
			tui.Item{Value: "edit", Label: "edit draft", Description: "change steps or {{input_name}} placeholders"},
			tui.Item{Value: "cancel", Label: "cancel", Description: "leave the recipe unchanged"})
		if choice == nil || choice.Value == "cancel" {
			return ""
		}
		if choice.Value == "save" {
			return body
		}
		if edited := strings.TrimSpace(t.ask("Recipe Markdown — {{input_name}} marks a value to ask for", body)); edited != "" {
			body = edited
		}
	}
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func (t *repl) recipeRun(r recipes.Recipe, args []string) (string, error) {
	values, err := recipes.BindInputs(r, args)
	if err != nil {
		return "", err
	}
	for _, in := range r.Inputs {
		if strings.TrimSpace(values[in]) != "" {
			continue
		}
		v := t.ask(r.Name+" — "+in+" (blank/Esc cancels)", "")
		if strings.TrimSpace(v) == "" {
			t.dim("Recipe cancelled.")
			return "", nil
		}
		values[in] = v
	}
	return recipes.Render(r, values)
}

func (t *repl) recipeFlow(ctx context.Context, raw string) (string, error) {
	store := recipes.NewStore()
	args, err := recipes.ParseArgs(raw)
	if err != nil {
		return "", err
	}
	action := ""
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	if action == "help" {
		t.dim("%s", recipeHelp)
		return "", nil
	}
	if action == "" {
		items := []tui.Item{{Value: "+save", Label: "+ save this session as a recipe"}}
		for _, n := range store.Names() {
			items = append(items, tui.Item{Value: n, Label: n})
		}
		selected := t.app.Pick("Recipes — choose a workflow", items, 0, "")
		if selected == nil {
			return "", nil
		}
		if selected.Value == "+save" {
			action = "save"
		} else {
			r, err := store.Read(selected.Value)
			if err != nil {
				return "", err
			}
			choice := t.choose(r.Name,
				tui.Item{Value: "run", Label: "run", Description: "inputs: " + joinOrNone(r.Inputs)},
				tui.Item{Value: "show", Label: "view"},
				tui.Item{Value: "edit", Label: "edit"},
				tui.Item{Value: "rm", Label: "delete"})
			if choice == nil {
				return "", nil
			}
			action = choice.Value
			args = append(args, r.Name)
		}
	}
	switch action {
	case "list":
		names := store.Names()
		if len(names) == 0 {
			t.dim("No recipes yet. Finish a task, then /recipe save <name>.")
		} else {
			lines := make([]string, len(names))
			for i, n := range names {
				lines[i] = "/recipe " + n
			}
			t.dim("%s", strings.Join(lines, "\n"))
		}
		t.dim("Recipes live in %s", store.Dir)
		return "", nil
	case "save":
		sess := t.run.Session
		hasMessage := false
		for _, e := range sess.CurrentBranch() {
			if e.Type == session.TypeMessage {
				hasMessage = true
				break
			}
		}
		if !hasMessage {
			t.dim("Send a message first, then save the completed workflow as a recipe.")
			return "", nil
		}
		name := ""
		if len(args) > 0 {
			name, args = args[0], args[1:]
		} else {
			name = t.ask("Recipe name (e.g. add-endpoint)", "")
		}
		if strings.TrimSpace(name) == "" {
			return "", nil
		}
		if err := recipes.ValidateName(name); err != nil {
			return "", err
		}
		for _, n := range store.Names() {
			if n == name {
				return "", errors.New(`Recipe "` + name + `" already exists. Use /recipe edit ` + name + ", or choose another name.")
			}
		}
		focus := strings.Join(args, " ")
		if focus == "" {
			focus = t.ask("What should vary between runs? (e.g. resource name and route)", "Infer reusable inputs from this session")
		}
		if strings.TrimSpace(focus) == "" {
			return "", nil
		}
		t.do(t.app.LoaderStart)
		draft, err := agent.GenerateRecipe(ctx, agent.DraftOptions{ModelID: t.cfg.FullID(), CWD: t.cfg.CWD,
			SessionPub: sess.ID, Tracker: agent.NewCostTracker()}, sess.CurrentBranch(), name, focus)
		t.do(t.app.LoaderStop)
		if err != nil {
			return "", err
		}
		body := t.recipeReview(name, draft)
		if body == "" {
			return "", nil
		}
		saved, err := store.Save(name, body, false)
		if err != nil {
			return "", err
		}
		run := "/recipe run " + name
		for _, in := range saved.Inputs {
			run += " <" + in + ">"
		}
		t.dim("Saved %s\nRun: %s", saved.Path, run)
		return "", nil
	case "show", "edit", "rm", "run":
		if len(args) == 0 {
			t.dim("%s", recipeHelp)
			return "", nil
		}
		name := args[0]
		args = args[1:]
		if action != "run" && len(args) > 0 {
			return "", errors.New("Usage: /recipe " + action + " <name>")
		}
		r, err := store.Read(name)
		if err != nil {
			return "", err
		}
		switch action {
		case "show":
			t.recipePreview(r.Name, r.Body)
			t.dim("%s", r.Path)
			return "", nil
		case "edit":
			edited := strings.TrimSpace(t.ask("Edit "+name+" — {{input_name}} marks an input", r.Body))
			if edited == "" {
				return "", nil
			}
			if body := t.recipeReview(name, edited); body != "" {
				saved, err := store.Save(name, body, true)
				if err != nil {
					return "", err
				}
				t.dim("Saved %s", saved.Path)
			}
			return "", nil
		case "rm":
			choice := t.choose("Delete "+name+"?",
				tui.Item{Value: "keep", Label: "keep recipe"},
				tui.Item{Value: "delete", Label: "delete recipe", Description: r.Path})
			if choice != nil && choice.Value == "delete" {
				if err := store.Remove(name); err != nil {
					return "", err
				}
				t.dim(`Deleted recipe "%s".`, name)
			}
			return "", nil
		}
		return t.recipeRun(r, args)
	}
	r, err := store.Read(action)
	if err != nil {
		return "", err
	}
	return t.recipeRun(r, args)
}
