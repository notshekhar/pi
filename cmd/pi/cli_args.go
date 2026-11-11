package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// cliArgs is one command's argv after parsing — loop's Args, minus the
// command word itself.
type cliArgs struct {
	positional []string
	// flags holds a value flag's text, or "true" for a switch.
	flags map[string]string
	// errors are usage problems. Non-empty means the invocation was not
	// understood, and nothing adjacent to it is run instead.
	errors []string
}

func (a cliArgs) flag(name string) (string, bool) {
	v, ok := a.flags[name]
	return v, ok
}

func (a cliArgs) has(name string) bool {
	_, ok := a.flags[name]
	return ok
}

// shortFlagHints answers the short flags people type from habit. `-p` is
// other agent CLIs' one-shot flag; here one-shot is a command.
var shortFlagHints = map[string]string{
	"p": `one-shot mode is a command here: ` + productName + ` run "<prompt>"`,
}

// parseCommandArgs is loop's parseArgs over the words after a command:
// flags take their arity from flagSpecs, a flag this command does not accept
// is an error rather than silently kept, `--` ends flag parsing, and a lone
// `-` stays positional (run's read-from-stdin marker).
func parseCommandArgs(cmd commandSpec, argv []string) cliArgs {
	out := cliArgs{flags: map[string]string{}}
	var seen []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			out.positional = append(out.positional, argv[i+1:]...)
			i = len(argv)
		case strings.HasPrefix(a, "--"):
			name, inline, hasInline := strings.Cut(a[2:], "=")
			seen = append(seen, name)
			i += out.longFlag(name, inline, hasInline, argv[i+1:])
		case strings.HasPrefix(a, "-") && a != "-":
			for _, letter := range strings.Split(a[1:], "") {
				spec, ok := flagForShort(letter)
				if !ok {
					hint := ""
					if h, known := shortFlagHints[letter]; known {
						hint = " — " + h
					}
					out.errors = append(out.errors, "unknown flag -"+letter+hint)
					continue
				}
				seen = append(seen, spec.name)
				out.flags[spec.name] = "true"
			}
		default:
			out.positional = append(out.positional, a)
		}
	}
	out.checkAllowed(cmd, seen)
	return out
}

// longFlag records one --flag and reports how many following words it
// consumed as its value.
func (a *cliArgs) longFlag(name, inline string, hasInline bool, rest []string) int {
	spec, known := findFlag(name)
	switch {
	case !known:
		// Arity unknown: consume nothing. The error stops the run anyway.
		a.flags[name] = "true"
		return 0
	case !spec.takesValue:
		if hasInline {
			a.errors = append(a.errors, "--"+name+" is a switch and takes no value")
		}
		a.flags[name] = "true"
		return 0
	case hasInline:
		a.setValue(spec, inline)
		return 0
	case len(rest) == 0 || (strings.HasPrefix(rest[0], "-") && rest[0] != "-"):
		a.errors = append(a.errors, "--"+name+" needs a value — "+strings.ToLower(spec.description))
		a.flags[name] = "true"
		return 0
	}
	a.setValue(spec, rest[0])
	return 1
}

func (a *cliArgs) setValue(spec flagSpec, value string) {
	a.flags[spec.name] = value
	if spec.choices != nil && !slices.Contains(spec.choices, value) {
		a.errors = append(a.errors, fmt.Sprintf("--%s must be one of: %s (got %q)", spec.name, strings.Join(spec.choices, ", "), value))
	}
}

// checkAllowed refuses flags the command does not take. A real flag on the
// wrong command reads differently from a typo, which saves a trip to help.
func (a *cliArgs) checkAllowed(cmd commandSpec, seen []string) {
	allowed := allowedFlags(cmd)
	for _, name := range seen {
		if slices.Contains(allowed, name) {
			continue
		}
		suggestion := closest(name, allowed)
		if _, known := findFlag(name); known {
			msg := fmt.Sprintf("--%s is not a flag of `%s`", name, cmd.name)
			if suggestion != "" {
				msg += " (did you mean --" + suggestion + "?)"
			}
			a.errors = append(a.errors, msg)
			continue
		}
		msg := "unknown flag --" + name
		if suggestion != "" {
			msg += " — did you mean --" + suggestion + "?"
		}
		a.errors = append(a.errors, msg)
	}
}

// unknownCommandError is loop's message for a word that is not a command,
// including the sentence someone typed meaning to run it as a prompt.
func unknownCommandError(word string) string {
	if strings.ContainsAny(word, " \t\n") {
		return fmt.Sprintf("unknown command %q — to run a prompt: %s run %q", word, productName, word)
	}
	msg := fmt.Sprintf("unknown command %q", word)
	if s := suggestCommand(word); s != "" {
		msg += " — did you mean `" + s + "`?"
	}
	return msg
}

// printUsageErrors writes the parse errors loop's way and returns exit 1.
func printUsageErrors(w io.Writer, errs []string) int {
	for _, e := range errs {
		fmt.Fprintf(w, "%s: %s\n", productName, e)
	}
	fmt.Fprintf(w, "Run `%s help` for usage.\n", productName)
	return 1
}

// readStdinAll is the prompt piped to `pi run -`.
func readStdinAll() string {
	data, _ := io.ReadAll(os.Stdin)
	return string(data)
}
