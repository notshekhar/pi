package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/core/config"
)

// `pi login`, `pi logout`, `pi whoami` — loop's cmdLogin / cmdLogout /
// cmdWhoami over the same auth.json.

// defaultLoginProvider is what bare `login` signs in to, as in loop.
const defaultLoginProvider = "xai"

// stdinLines is shared so two prompts in a row read consecutive lines rather
// than the second losing what the first buffered.
var stdinLines = bufio.NewReader(os.Stdin)

// readStdinLine prints prompt on stdout and reads one trimmed line.
func readStdinLine(prompt string) string {
	fmt.Print(prompt)
	line, _ := stdinLines.ReadString('\n')
	return strings.TrimSpace(line)
}

func loginCommand(c *cliCall) int {
	provider := defaultLoginProvider
	if len(c.args.positional) > 0 {
		provider = c.args.positional[0]
	}
	if provider == "xai" {
		return loginXai(c)
	}
	return loginAPIKey(provider, readStdinLine(strings.ToUpper(provider)+"_API_KEY: "), provider+" API key saved.")
}

// loginXai offers xAI's two ways in: the SuperGrok subscription over OAuth,
// or a plain API key.
func loginXai(c *cliCall) int {
	if readStdinLine("xAI: [1] OAuth subscription  [2] API key  > ") == "2" {
		return loginAPIKey("xai", readStdinLine("XAI_API_KEY: "), "xAI API key saved.")
	}
	err := config.XaiLogin(c.ctx, func(url, instructions string) {
		fmt.Println(instructions)
		fmt.Println(url)
		if openBrowser(url) {
			fmt.Println("(opened in browser)")
		} else {
			fmt.Println("(open this URL in a browser)")
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("xAI OAuth login complete.")
	return 0
}

func loginAPIKey(provider, key, saved string) int {
	if err := config.SaveKey(provider, key); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(saved)
	return 0
}

// logoutCommand removes one provider's credential, or every one. A provider
// with nothing stored is already logged out, which loop reports as done.
func logoutCommand(c *cliCall) int {
	target := ""
	if len(c.args.positional) > 0 {
		target = c.args.positional[0]
	}
	if target == "" || config.AuthMode(target) != "missing" {
		if err := config.Logout(target); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	if target == "" {
		fmt.Println("Logged out of all providers.")
	} else {
		fmt.Printf("Logged out of %s.\n", target)
	}
	return 0
}

func whoamiCommand(*cliCall) int {
	active := config.ActiveProvider()
	if active == "" {
		active = "none"
	}
	authorized := strings.Join(config.ListAuthorizedProviders(), ", ")
	if authorized == "" {
		authorized = "none"
	}
	fmt.Printf("Active provider: %s\n", active)
	fmt.Printf("Authorized: %s\n", authorized)
	return 0
}
