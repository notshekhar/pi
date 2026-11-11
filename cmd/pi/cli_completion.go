package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Shell tab completion — loop's completion.ts, generated from commandSpecs
// so a new command is described once. The scripts are static text: a
// completion that ran pi on every Tab would put a process spawn in front of
// a keystroke.

var completionShells = []string{"bash", "zsh", "fish"}

// completionCommand prints the script on stdout and how to install it on
// stderr, so `pi completion zsh > file` yields a file the shell can load.
func completionCommand(c *cliCall) int {
	shell := ""
	if len(c.args.positional) > 0 {
		shell = c.args.positional[0]
	}
	if !slices.Contains(completionShells, shell) {
		fmt.Fprintf(os.Stderr, "Usage: %s completion <%s>\n", productName, strings.Join(completionShells, "|"))
		return 1
	}
	fmt.Println(completionScript(shell, productName))
	fmt.Fprintln(os.Stderr, completionInstallHint(shell, productName))
	return 0
}

func completionScript(shell, name string) string {
	switch shell {
	case "bash":
		return bashCompletion(name)
	case "zsh":
		return zshCompletion(name)
	}
	return fishCompletion(name)
}

// topLevelFlag is a flag offered before or instead of a command.
type topLevelFlag struct{ flag, description string }

// topLevelFlags are what bare `pi` accepts.
func topLevelFlags() []topLevelFlag {
	var out []topLevelFlag
	for _, name := range append(append([]string{}, interactiveFlags...), globalFlags...) {
		f, _ := findFlag(name)
		out = append(out, topLevelFlag{"--" + name, f.description})
	}
	return out
}

// valueFlagNames are flags that take a value, which must complete a value
// rather than another flag.
func valueFlagNames() []string {
	var out []string
	for _, f := range flagSpecs {
		if f.takesValue {
			out = append(out, "--"+f.name)
		}
	}
	return out
}

// commandWords is what may follow a command: its subcommands and its own
// flags, so `--max-steps` completes after `run` and nowhere else. --help is
// offered everywhere; --version after a command is accepted but pointless.
func commandWords(c commandSpec) []string {
	words := append([]string{}, c.subcommands...)
	for _, f := range c.flags {
		words = append(words, "--"+f)
	}
	return append(words, "--help")
}

// commandsWithWords are the commands with anything to offer at word two.
func commandsWithWords() []commandSpec {
	var out []commandSpec
	for _, c := range commandSpecs {
		if len(c.subcommands) > 0 || len(c.flags) > 0 {
			out = append(out, c)
		}
	}
	return out
}

func commandNames() string {
	names := make([]string, 0, len(commandSpecs))
	for _, c := range commandSpecs {
		names = append(names, c.name)
	}
	return strings.Join(names, " ")
}

func topLevelFlagNames() string {
	var names []string
	for _, f := range topLevelFlags() {
		names = append(names, f.flag)
	}
	return strings.Join(names, " ")
}

// shellQuote escapes for a single-quoted POSIX shell string.
func shellQuote(s string) string { return strings.ReplaceAll(s, "'", `'\''`) }

func bashCompletion(name string) string {
	var cases []string
	for _, c := range commandsWithWords() {
		cases = append(cases, fmt.Sprintf("        %s) COMPREPLY=($(compgen -W '%s' -- \"$cur\")); return;;", c.name, strings.Join(commandWords(c), " ")))
	}
	// --cwd wants a directory; every other value flag has nothing to offer.
	opaque := slices.DeleteFunc(valueFlagNames(), func(f string) bool { return f == "--cwd" })
	return fmt.Sprintf(`# %[1]s completion for bash
_%[1]s_complete() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    # Flags that take a value: let the shell complete paths, not our words.
    case "$prev" in
        --cwd) COMPREPLY=($(compgen -d -- "$cur")); return;;
        %[2]s) return;;
    esac

    # Second word onward: complete the command's own subcommands and flags.
    if [ "$COMP_CWORD" -gt 1 ]; then
        case "${COMP_WORDS[1]}" in
%[3]s
        esac
    fi

    COMPREPLY=($(compgen -W '%[4]s %[5]s' -- "$cur"))
}
complete -F _%[1]s_complete %[1]s
`, name, strings.Join(opaque, "|"), strings.Join(cases, "\n"), commandNames(), topLevelFlagNames())
}

// zshCompletion is a native script: zsh shows each candidate's description,
// which is the reason not to reuse bashcompinit.
func zshCompletion(name string) string {
	var described, flagLines, subCases []string
	for _, c := range commandSpecs {
		described = append(described, fmt.Sprintf("        '%s:%s'", c.name, shellQuote(c.description)))
	}
	for _, f := range topLevelFlags() {
		flagLines = append(flagLines, fmt.Sprintf("        '%s:%s'", f.flag, shellQuote(f.description)))
	}
	// compadd rather than _values: `_values 'tag' --flag` would read the
	// leading -- as its own terminator.
	for _, c := range commandsWithWords() {
		subCases = append(subCases, fmt.Sprintf("        %s) compadd -- %s ;;", c.name, strings.Join(commandWords(c), " ")))
	}
	return fmt.Sprintf(`#compdef %[1]s
# %[1]s completion for zsh

_%[1]s() {
    local -a commands flags
    commands=(
%[2]s
    )
    flags=(
%[3]s
    )

    if (( CURRENT == 2 )); then
        _describe -t commands 'command' commands
        _describe -t flags 'flag' flags
        return
    fi

    case "${words[2]}" in
%[4]s
        *) _files ;;
    esac
}

_%[1]s "$@"
`, name, strings.Join(described, "\n"), strings.Join(flagLines, "\n"), strings.Join(subCases, "\n"))
}

func fishCompletion(name string) string {
	// No file completion by default: the first word is a command, not a path.
	lines := []string{"# " + name + " completion for fish", "complete -c " + name + " -f"}
	for _, c := range commandSpecs {
		lines = append(lines, fmt.Sprintf("complete -c %s -n '__fish_use_subcommand' -a '%s' -d '%s'", name, c.name, shellQuote(c.description)))
		for _, sub := range c.subcommands {
			lines = append(lines, fmt.Sprintf("complete -c %s -n '__fish_seen_subcommand_from %s' -a '%s'", name, c.name, sub))
		}
		for _, f := range c.flags {
			spec, _ := findFlag(f)
			lines = append(lines, fmt.Sprintf("complete -c %s -n '__fish_seen_subcommand_from %s' -l '%s' -d '%s'", name, c.name, f, shellQuote(spec.description)))
		}
	}
	for _, f := range topLevelFlags() {
		lines = append(lines, fmt.Sprintf("complete -c %s -l '%s' -d '%s'", name, strings.TrimPrefix(f.flag, "--"), shellQuote(f.description)))
	}
	return strings.Join(lines, "\n") + "\n"
}

// completionInstallHint says where each shell wants the script.
func completionInstallHint(shell, name string) string {
	switch shell {
	case "bash":
		return fmt.Sprintf("# add to ~/.bashrc:\n#   source <(%s completion bash)", name)
	case "zsh":
		// A #compdef script must live on fpath; sourcing it works only after
		// compinit has run, which is the usual footgun.
		return fmt.Sprintf("# write it somewhere on your fpath, then restart the shell:\n"+
			"#   %[1]s completion zsh > \"${fpath[1]}/_%[1]s\"\n"+
			"# or add to ~/.zshrc (after compinit):\n"+
			"#   source <(%[1]s completion zsh)", name)
	}
	return fmt.Sprintf("# write it once:\n#   %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish", name)
}
