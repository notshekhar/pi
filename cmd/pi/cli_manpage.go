package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
)

// The pi(1) man page — loop's manpage.ts, generated from the same table as
// completion so the two cannot describe different CLIs.

// manCommand: `pi man` opens it through man(1), `--path` prints the roff
// source, `--install` writes it to the user manpath so plain `man pi` works.
func manCommand(c *cliCall) int {
	switch {
	case c.args.has("install"):
		path, err := installManPage(productName, version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Println("installed " + path)
	case c.args.has("path"):
		fmt.Println(manPageSource(productName, version, time.Now()))
	default:
		return showManPage(productName, version)
	}
	return 0
}

// roffEscape keeps text from being read as roff: a backslash is doubled, a
// line-leading dot would be a request, and a bare - may become a hyphen.
func roffEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, ".") {
			lines[i] = `\&` + l
		}
	}
	return strings.ReplaceAll(strings.Join(lines, "\n"), "-", `\-`)
}

func manPageSource(name, ver string, now time.Time) string {
	cfgDir := "~/" + home.DirName
	lines := []string{
		fmt.Sprintf(`.TH %s 1 "%s" "%s %s" "User Commands"`, strings.ToUpper(name), now.Format("2006-01-02"), name, ver),
		".SH NAME",
		roffEscape(name) + ` \- terminal coding agent`,
		".SH SYNOPSIS",
		".B " + name,
		// One .RI per bracket group: .RI alternates roman and italic per
		// argument, so one line for both would italicise the wrong halves.
		".RI [ command ]",
		".RI [ options ]",
		".SH DESCRIPTION",
		"Run " + roffEscape(name) + " with no arguments to open the interactive TUI in the current",
		"directory. With a command, it performs that task and exits.",
		".PP",
		"The agent reads and writes files, runs shell commands, and calls model",
		"providers on your behalf, inside your own user account. It has no sandbox of",
		`its own \(em see SECURITY.md in the repository for the trust boundary.`,
		".SH COMMANDS",
	}
	for _, c := range commandSpecs {
		args := ""
		if len(c.subcommands) > 0 {
			args = ` \fI` + strings.Join(c.subcommands, `\fR|\fI`) + `\fR`
		}
		// Aliases dispatch but are not listed as commands, so this is the
		// one place that has to name them.
		aka := ""
		if len(c.aliases) > 0 {
			aka = " (also: " + strings.Join(c.aliases, ", ") + ")"
		}
		lines = append(lines, ".TP", ".B "+name+" "+c.name+args, roffEscape(c.description+aka))
	}
	lines = append(lines, ".SH OPTIONS")
	// Every flag, not just the global ones: each command accepts only its
	// own, so the page has to say which is which.
	for _, f := range flagSpecs {
		short, value := "", ""
		if f.short != "" {
			short = `, \-` + f.short
		}
		if f.takesValue {
			value = ` \fIvalue\fR`
		}
		where := ""
		if owners := commandsForFlag(f.name); len(owners) > 0 {
			where = " [" + strings.Join(owners, ", ") + "]"
		}
		lines = append(lines, ".TP", `.B \-\-`+roffEscape(f.name)+short+value, roffEscape(f.description+where))
	}
	lines = append(lines,
		".SH FILES",
		".TP", ".I "+cfgDir+"/settings.json",
		"Global settings: default model, hooks, MCP servers, toggles.",
		".TP", ".I "+cfgDir+"/auth.json",
		"Provider credentials and custom provider definitions.",
		".TP", ".I <cwd>/"+home.DirName+"/settings.json",
		"Project settings, overriding the global file.",
		".TP", ".I AGENTS.md, CLAUDE.md",
		"Workspace context files, read into the agent's prompt.",
		".SH ENVIRONMENT",
		".TP", ".B <PROVIDER>_API_KEY",
		"Provider API keys are read from the environment when not stored by",
		".BR "+name+` " login" .`,
		".SH EXAMPLES",
		".TP", ".B "+name,
		"Open the interactive agent in the current directory.",
		".TP", ".B "+name+` run "explain this repo"`,
		"Run one prompt and exit.",
		".TP", ".B "+name+` completion zsh > "${fpath[1]}/_`+name+`"`,
		"Install shell tab completion.",
		".SH SEE ALSO",
		"Full documentation at https://github.com/notshekhar/"+name,
	)
	return strings.Join(lines, "\n") + "\n"
}

// manInstallPath is on the default manpath on macOS and most Linux, so
// `man pi` finds it without configuration.
func manInstallPath(name string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "man", "man1", name+".1")
}

func installManPage(name, ver string) (string, error) {
	path := manInstallPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(manPageSource(name, ver, time.Now())), 0o644)
}

// showManPage renders through man(1) for the pager and terminal width the
// user expects, falling back to the raw source where there is no man.
func showManPage(name, ver string) int {
	path, err := installManPage(name, ver)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	cmd := exec.Command("man", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var notFound *exec.Error
	if err := cmd.Run(); errors.As(err, &notFound) {
		fmt.Println(manPageSource(name, ver, time.Now()))
	}
	return 0
}
