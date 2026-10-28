package lsp

import (
	"os"
	"path/filepath"
	"strings"
)

// The language servers this extension knows about.
//
// Each entry says how to FIND the server (BinNames, looked up in the
// project's node_modules/.bin then PATH) and how to ROOT it (RootMarkers,
// nearest ancestor of the edited file wins).
//
// DISCOVERY ONLY — no install routes. loop can also provision a server it
// cannot find: npm deps into its config directory, `go install`, or a
// prebuilt release archive. Those are about ACQUIRING a toolchain, and doing
// them here would mean shipping an npm client, an archive extractor, and a
// per-platform release table. The lookup order below is loop's own first
// choice anyway: a server the project already has beats one that was fetched,
// and on a machine where the toolchain is installed the behaviour is
// identical. Where nothing is found the tool says so, by name.

// ServerDef describes one language server.
type ServerDef struct {
	Key string
	// Extensions this server handles, lowercase, with the dot.
	Extensions []string
	// Filenames it also handles ("Dockerfile"), compared case-insensitively.
	Filenames []string
	// LanguageID is the LSP language id; LanguageIDFunc takes precedence when
	// one server spans several languages.
	LanguageID     string
	LanguageIDFunc func(absPath string) string
	// Runtime is "native" (the default), "node", or "java" — the last for a
	// server that is an executable jar rather than an executable.
	Runtime  string
	BinNames []string
	Args     []string
	// JVMArgs go BEFORE -jar. After it they are arguments to the application,
	// not the JVM, and the system properties the server reads its product id
	// from would never be set.
	JVMArgs []string
	// RootMarkers mark the project root; the nearest ancestor of the edited
	// file wins. A monorepo with three tsconfigs gets three servers, each
	// scoped to its package, which is what makes the diagnostics right.
	RootMarkers []string
	// DisqualifyMarkers stand this server down — a deno.json must stop the
	// TypeScript server, which would otherwise double every diagnostic.
	DisqualifyMarkers []string
	// Requires are binaries that must be on PATH for the server to work.
	Requires           []string
	RequiresMinVersion map[string]int
	// MinMajorVersion is the lowest version of a discovered binary that will
	// be spoken to. Needed where a long-lived command only grew LSP support
	// recently: `tsc` has been on PATH for a decade and only v7+ answers
	// `--lsp`, and launching v5 fails the handshake and takes the language
	// down with it.
	MinMajorVersion int
}

func tsLanguageId(absPath string) string {
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	}
	return "typescript"
}

func cLanguageId(absPath string) string {
	if strings.ToLower(filepath.Ext(absPath)) == ".c" {
		return "c"
	}
	return "cpp"
}

// Servers is the built-in table.
var Servers = []ServerDef{
	{
		Key:               "typescript",
		Extensions:        []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
		LanguageIDFunc:    tsLanguageId,
		BinNames:          []string{"tsgo", "tsc"},
		Args:              []string{"--lsp", "--stdio"},
		RootMarkers:       []string{"tsconfig.json", "jsconfig.json", "package.json", "package-lock.json", "bun.lockb", "bun.lock", "pnpm-lock.yaml", "yarn.lock"},
		DisqualifyMarkers: []string{"deno.json", "deno.jsonc"},
		MinMajorVersion:   7,
	},
	{
		Key:            "deno",
		LanguageIDFunc: tsLanguageId,
		BinNames:       []string{"deno"},
		Args:           []string{"lsp"},
		RootMarkers:    []string{"deno.json", "deno.jsonc"},
	},
	{
		Key:         "vue",
		Extensions:  []string{".vue"},
		LanguageID:  "vue",
		Runtime:     "node",
		BinNames:    []string{"vue-language-server"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "package-lock.json", "bun.lockb", "bun.lock", "pnpm-lock.yaml", "yarn.lock"},
	},
	{
		Key:         "svelte",
		Extensions:  []string{".svelte"},
		LanguageID:  "svelte",
		Runtime:     "node",
		BinNames:    []string{"svelteserver", "svelte-language-server"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "package-lock.json", "bun.lockb", "bun.lock", "pnpm-lock.yaml", "yarn.lock"},
	},
	{
		Key:         "astro",
		Extensions:  []string{".astro"},
		LanguageID:  "astro",
		Runtime:     "node",
		BinNames:    []string{"astro-ls"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "package-lock.json", "bun.lockb", "bun.lock", "pnpm-lock.yaml", "yarn.lock"},
	},
	{
		Key:         "biome",
		Extensions:  []string{".json", ".jsonc"},
		LanguageID:  "json",
		BinNames:    []string{"biome"},
		Args:        []string{"lsp-proxy", "--stdio"},
		RootMarkers: []string{"biome.json", "biome.jsonc"},
	},
	{
		Key:            "oxlint",
		LanguageIDFunc: tsLanguageId,
		BinNames:       []string{"oxc_language_server"},
		RootMarkers:    []string{".oxlintrc.json"},
	},
	{
		Key:         "go",
		Extensions:  []string{".go"},
		Filenames:   []string{"go.mod", "go.sum"},
		LanguageID:  "go",
		BinNames:    []string{"gopls"},
		RootMarkers: []string{"go.mod", "go.work"},
		Requires:    []string{"go"},
	},
	{
		Key:         "rust",
		Extensions:  []string{".rs"},
		LanguageID:  "rust",
		BinNames:    []string{"rust-analyzer"},
		RootMarkers: []string{"Cargo.toml", "Cargo.lock"},
	},
	{
		Key:            "clangd",
		Extensions:     []string{".c", ".cpp", ".cc", ".cxx", ".c++", ".h", ".hpp", ".hh", ".hxx", ".h++"},
		LanguageIDFunc: cLanguageId,
		BinNames:       []string{"clangd"},
		Args:           []string{"--background-index", "--clang-tidy"},
		RootMarkers:    []string{"compile_commands.json", "compile_flags.txt", ".clangd"},
	},
	{
		Key:         "zig",
		Extensions:  []string{".zig", ".zon"},
		LanguageID:  "zig",
		BinNames:    []string{"zls"},
		RootMarkers: []string{"build.zig"},
		Requires:    []string{"zig"},
	},
	{
		Key:         "swift",
		Extensions:  []string{".swift"},
		LanguageID:  "swift",
		BinNames:    []string{"sourcekit-lsp"},
		RootMarkers: []string{"Package.swift"},
	},
	{
		Key:         "nix",
		Extensions:  []string{".nix"},
		LanguageID:  "nix",
		BinNames:    []string{"nixd", "nil"},
		RootMarkers: []string{"flake.nix", "default.nix", "shell.nix"},
	},
	{
		Key:                "java",
		Extensions:         []string{".java"},
		LanguageID:         "java",
		Runtime:            "java",
		BinNames:           []string{"jdtls"},
		Args:               []string{"-configuration", "{configDir}", "-data", "{dataDir}"},
		JVMArgs:            []string{"-Declipse.application=org.eclipse.jdt.ls.core.id1", "-Dosgi.bundles.defaultStartLevel=4", "-Declipse.product=org.eclipse.jdt.ls.core.product", "-Dlog.level=ALL", "-Xmx1G", "--add-modules=ALL-SYSTEM", "--add-opens=java.base/java.util=ALL-UNNAMED", "--add-opens=java.base/java.lang=ALL-UNNAMED"},
		RootMarkers:        []string{"pom.xml", "build.gradle", "build.gradle.kts", ".project"},
		Requires:           []string{"java"},
		RequiresMinVersion: map[string]int{"java": 21},
	},
	{
		Key:         "kotlin",
		Extensions:  []string{".kt", ".kts"},
		LanguageID:  "kotlin",
		BinNames:    []string{"kotlin-ls", "kotlin-language-server"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"build.gradle.kts", "build.gradle", "settings.gradle.kts", "pom.xml"},
	},
	{
		Key:         "csharp",
		Extensions:  []string{".cs", ".csx"},
		LanguageID:  "csharp",
		BinNames:    []string{"csharp-ls", "OmniSharp"},
		RootMarkers: []string{".sln", ".slnx", ".csproj", "global.json"},
	},
	{
		Key:         "razor",
		Extensions:  []string{".razor", ".cshtml"},
		LanguageID:  "razor",
		BinNames:    []string{"rzls"},
		RootMarkers: []string{".sln", ".slnx", ".csproj", "global.json"},
	},
	{
		Key:         "fsharp",
		Extensions:  []string{".fs", ".fsi", ".fsx", ".fsscript"},
		LanguageID:  "fsharp",
		BinNames:    []string{"fsautocomplete"},
		RootMarkers: []string{".sln", ".slnx", ".fsproj", "global.json"},
	},
	{
		Key:         "python",
		Extensions:  []string{".py", ".pyi"},
		LanguageID:  "python",
		Runtime:     "node",
		BinNames:    []string{"pyright-langserver"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile", "pyrightconfig.json"},
	},
	{
		Key:         "ruby",
		Extensions:  []string{".rb", ".rake", ".gemspec", ".ru"},
		Filenames:   []string{"Gemfile", "Rakefile"},
		LanguageID:  "ruby",
		BinNames:    []string{"ruby-lsp", "solargraph"},
		RootMarkers: []string{"Gemfile", ".ruby-version"},
	},
	{
		Key:         "php",
		Extensions:  []string{".php"},
		LanguageID:  "php",
		Runtime:     "node",
		BinNames:    []string{"intelephense"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"composer.json", "composer.lock", ".php-version"},
	},
	{
		Key:         "lua",
		Extensions:  []string{".lua"},
		LanguageID:  "lua",
		BinNames:    []string{"lua-language-server"},
		RootMarkers: []string{".luarc.json", ".luarc.jsonc", ".stylua.toml", "stylua.toml"},
	},
	{
		Key:        "bash",
		Extensions: []string{".sh", ".bash", ".zsh", ".ksh"},
		LanguageID: "shellscript",
		Runtime:    "node",
		BinNames:   []string{"bash-language-server"},
		Args:       []string{"start"},
	},
	{
		Key:         "elixir",
		Extensions:  []string{".ex", ".exs"},
		LanguageID:  "elixir",
		BinNames:    []string{"elixir-ls", "language_server.sh"},
		RootMarkers: []string{"mix.exs", "mix.lock"},
	},
	{
		Key:         "dart",
		Extensions:  []string{".dart"},
		LanguageID:  "dart",
		BinNames:    []string{"dart"},
		Args:        []string{"language-server", "--lsp"},
		RootMarkers: []string{"pubspec.yaml", "analysis_options.yaml"},
	},
	{
		Key:         "julia",
		Extensions:  []string{".jl"},
		LanguageID:  "julia",
		BinNames:    []string{"julia"},
		Args:        []string{"--startup-file=no", "--history-file=no", "-e", "using LanguageServer; runserver()"},
		RootMarkers: []string{"Project.toml", "Manifest.toml"},
	},
	{
		Key:         "haskell",
		Extensions:  []string{".hs", ".lhs"},
		LanguageID:  "haskell",
		BinNames:    []string{"haskell-language-server-wrapper", "haskell-language-server"},
		Args:        []string{"--lsp"},
		RootMarkers: []string{"stack.yaml", "cabal.project", "hie.yaml"},
	},
	{
		Key:         "ocaml",
		Extensions:  []string{".ml", ".mli"},
		LanguageID:  "ocaml",
		BinNames:    []string{"ocamllsp"},
		RootMarkers: []string{"dune-project", "dune-workspace", "opam"},
	},
	{
		Key:         "clojure",
		Extensions:  []string{".clj", ".cljs", ".cljc", ".edn"},
		LanguageID:  "clojure",
		BinNames:    []string{"clojure-lsp"},
		Args:        []string{"listen"},
		RootMarkers: []string{"deps.edn", "project.clj", "shadow-cljs.edn", "bb.edn"},
	},
	{
		Key:         "gleam",
		Extensions:  []string{".gleam"},
		LanguageID:  "gleam",
		BinNames:    []string{"gleam"},
		Args:        []string{"lsp"},
		RootMarkers: []string{"gleam.toml"},
	},
	{
		Key:        "yaml",
		Extensions: []string{".yaml", ".yml"},
		LanguageID: "yaml",
		Runtime:    "node",
		BinNames:   []string{"yaml-language-server"},
		Args:       []string{"--stdio"},
	},
	{
		Key:        "json",
		Extensions: []string{".json", ".jsonc"},
		LanguageID: "json",
		Runtime:    "node",
		BinNames:   []string{"vscode-json-language-server"},
		Args:       []string{"--stdio"},
	},
	{
		Key:        "dockerfile",
		Extensions: []string{".dockerfile"},
		Filenames:  []string{"Dockerfile", "Containerfile"},
		LanguageID: "dockerfile",
		Runtime:    "node",
		BinNames:   []string{"docker-langserver"},
		Args:       []string{"--stdio"},
	},
	{
		Key:         "terraform",
		Extensions:  []string{".tf", ".tfvars"},
		LanguageID:  "terraform",
		BinNames:    []string{"terraform-ls"},
		Args:        []string{"serve"},
		RootMarkers: []string{".terraform.lock.hcl", "terraform.tfstate"},
	},
	{
		Key:         "prisma",
		Extensions:  []string{".prisma"},
		LanguageID:  "prisma",
		Runtime:     "node",
		BinNames:    []string{"prisma-language-server"},
		Args:        []string{"--stdio"},
		RootMarkers: []string{"schema.prisma"},
	},
	{
		Key:         "latex",
		Extensions:  []string{".tex", ".bib"},
		LanguageID:  "latex",
		BinNames:    []string{"texlab"},
		RootMarkers: []string{".latexmkrc", "latexmkrc", ".texlabroot"},
	},
	{
		Key:         "typst",
		Extensions:  []string{".typ", ".typc"},
		LanguageID:  "typst",
		BinNames:    []string{"tinymist"},
		RootMarkers: []string{"typst.toml"},
	},
}

// LanguageIDFor is the LSP language id for a file under this server.
func (d ServerDef) LanguageIDFor(absPath string) string {
	if d.LanguageIDFunc != nil {
		return d.LanguageIDFunc(absPath)
	}
	return d.LanguageID
}

// Handles reports whether this server covers a file.
func (d ServerDef) Handles(absPath string) bool {
	ext := strings.ToLower(filepath.Ext(absPath))
	for _, e := range d.Extensions {
		if e == ext {
			return true
		}
	}
	name := strings.ToLower(filepath.Base(absPath))
	for _, f := range d.Filenames {
		if strings.ToLower(f) == name {
			return true
		}
	}
	return false
}

// ServersFor is every server that handles a file and is not disqualified.
//
// Several can apply at once — a .ts file is served by the TypeScript server
// and by biome — and each contributes its own diagnostics.
func ServersFor(absPath, workspace string) []ServerDef {
	var out []ServerDef
	for _, d := range Servers {
		if d.Handles(absPath) && !d.disqualified(absPath, workspace) {
			out = append(out, d)
		}
	}
	return out
}

// FindDef looks a server up by key.
func FindDef(key string) (ServerDef, bool) {
	for _, d := range Servers {
		if d.Key == key {
			return d, true
		}
	}
	return ServerDef{}, false
}

// Root is the directory this server should treat as the project root: the
// nearest ancestor of the file holding one of its markers, else the workspace.
func (d ServerDef) Root(absPath, fallback string) string {
	if len(d.RootMarkers) == 0 {
		return fallback
	}
	dir := filepath.Dir(absPath)
	for {
		for _, marker := range d.RootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if dir == fallback || parent == dir {
			return fallback
		}
		dir = parent
	}
}

// disqualified reports whether a marker stands this server down.
func (d ServerDef) disqualified(absPath, fallback string) bool {
	if len(d.DisqualifyMarkers) == 0 {
		return false
	}
	root := d.Root(absPath, fallback)
	for _, marker := range d.DisqualifyMarkers {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return true
		}
	}
	return false
}
