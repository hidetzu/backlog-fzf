# backlog-fzf

[![CI](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml/badge.svg)](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hidetzu/backlog-fzf)](https://github.com/hidetzu/backlog-fzf/releases/latest)

[日本語](./README.ja.md)

![bkfz demo: a 2-character Japanese query filters issues and documents live, with a preview pane](docs/demo.gif)

A CLI for cross-project fuzzy search across Nulab Backlog issues and documents (binary name: `bkfz`).

It mirrors Backlog API data into a local store so you can search across multiple projects quickly.

The binary is pure-Go and self-contained; the TUI launches an external `fzf` process for interaction.

## Features

- Fuzzy search across both issues and documents
- Incremental filtering and preview via `fzf`
- Open the selected entry in a browser, or copy its URL, without leaving the TUI
- Sync progress (count and ETA) shown live

## Requirements

- A Backlog API key (`BACKLOG_API_KEY`)
- [fzf](https://github.com/junegunn/fzf) 0.40+ for the TUI (installed automatically with Homebrew)

## Install

### Homebrew (macOS, v0.2.0+)

```bash
brew install hidetzu/tap/bkfz
```

This also installs `fzf`. On Linux, use the prebuilt binaries below.

### Prebuilt binaries

Download the archive for your OS from [Releases](https://github.com/hidetzu/backlog-fzf/releases/latest) and put `bkfz` on your `PATH`. For example, on Linux x86_64:

```bash
VERSION=0.2.0  # see the Releases page for the latest version
curl -fsSL "https://github.com/hidetzu/backlog-fzf/releases/download/v${VERSION}/backlog-fzf_${VERSION}_Linux_x86_64.tar.gz" | tar xz bkfz
sudo mv bkfz /usr/local/bin/
```

Archives: `macOS_arm64`, `macOS_x86_64`, `Linux_arm64`, `Linux_x86_64` (`.tar.gz`) and `Windows_x86_64` (`.zip`). Install fzf separately (`winget install junegunn.fzf` on Windows).

### go install (Go 1.26+)

```bash
go install github.com/hidetzu/backlog-fzf/cmd/bkfz@latest
```

### From source (Go 1.26+)

```bash
git clone https://github.com/hidetzu/backlog-fzf.git
cd backlog-fzf
make build   # → bin/bkfz
```

Check the installation with `bkfz version`.

## Quick start

```bash
# 1) Create the config file
bkfz init

# 2) Set the API key
export BACKLOG_API_KEY=your_personal_api_key

# 3) Sync
bkfz sync

# 4) Search (TUI)
bkfz
```

## Commands

```text
bkfz                          Launch the fzf TUI
bkfz <query>                  Non-interactive search (stdout)
bkfz init                     Create the config file
bkfz sync                     Incremental sync
bkfz sync --refetch           Re-fetch ignoring the watermark
bkfz sync -p PROJ             Limit sync to one project key
bkfz open <KEY>               Open an issue in the browser
bkfz open doc <DOC_ID>        Open a document
bkfz preview <type> <KEY>     Preview output
bkfz url <type> <KEY>         Print the URL (type = issue | doc)
bkfz url --copy <type> <KEY>  Copy the URL to the clipboard
bkfz --list <query>           List output for fzf reload
bkfz --semantic <query>       Non-interactive semantic search (see below)
bkfz embed                    Build / update the semantic index
bkfz embed --rebuild          Re-embed everything (after changing the model)
bkfz sync --no-embed          Sync without touching the semantic index
bkfz version                  Print version
```

## Keybindings (TUI)

| Key | Action |
|---|---|
| `Enter` | Open in the browser and exit |
| `Ctrl-Y` | Copy the URL to the clipboard (stay in the TUI) |
| `Ctrl-O` | Open in the browser (stay in the TUI) |
| `Ctrl-S` / `Alt-S` | Re-run the current query as a semantic search (opt-in, see below) |
| `Ctrl-/` | Toggle the preview |
| `Shift-↑` / `Shift-↓` | Scroll the preview |
| `Esc` / `Ctrl-C` | Quit |

Results come from bkfz's index as-is (fzf's own filtering is disabled), so hits in descriptions and document bodies are listed too.

`Ctrl-Y` replaces fzf's default "yank" in the query line.

Clipboard: `pbcopy` (macOS), `clip` (Windows), `wl-copy` / `xclip` / `xsel` (Linux).

## Configuration

Config file:

```text
$XDG_CONFIG_HOME/bkfz/config.yaml
(falls back to ~/.config/bkfz/config.yaml)
```

Example:

```yaml
space_domain: example.backlog.com
projects:
  - PROJ
  - DOCS
```

DB file:

```text
$XDG_DATA_HOME/bkfz/index.db
(falls back to ~/.local/share/bkfz/index.db)
```

The API key is not stored in the config; it is read from the `BACKLOG_API_KEY` environment variable.

## Semantic search (optional)

Keyword search needs the words you remember to appear in the record. When you only remember *what it was about* ("there was an issue where login broke after the migration..."), semantic search finds records by meaning instead.

It is off by default. Nothing changes for you unless you add an `embedding:` section to the config. Text is embedded by a separate server that you run; the default is a local [Ollama](https://ollama.com), so nothing leaves your machine.

```bash
# 1) Install Ollama and pull a multilingual embedding model (~600MB)
brew install ollama          # or https://ollama.com/download
ollama pull embeddinggemma

# 2) Enable it in config.yaml (these are the defaults; model and base_url can be changed)
cat >> ~/.config/bkfz/config.yaml <<'EOF'
embedding:
  base_url: http://localhost:11434/v1
  model: embeddinggemma
EOF

# 3) Build the semantic index (one-off; about 10 records/s on an Apple Silicon Mac)
bkfz embed

# 4) Search by meaning
bkfz --semantic "login stopped working after moving to the cloud"
bkfz                         # type keywords as usual; press Ctrl-S to search the same query by meaning
```

- `bkfz sync` keeps the semantic index up to date afterwards (only changed records are re-embedded). If the embedding server is down, sync still succeeds and prints a warning.
- In the TUI, `Ctrl-S` (or `Alt-S`) re-runs the current query through semantic search; the prompt changes to `semantic>`. Typing returns to keyword search. If the server cannot be reached, the error is shown as the only line in the list.
- What gets embedded: the issue summary plus the first 1,200 characters of its description, and document titles plus body text in 1,000-character chunks (at most 8 per document). Status, assignee and dates are not embedded.
- The index grows by about 3KB per record (768-dimensional model). Changing `model` (or `dimensions`) requires `bkfz embed --rebuild`.
- Any OpenAI-compatible `/v1/embeddings` endpoint works (LM Studio, llama.cpp server, hosted APIs). For hosted APIs set `api_key_env` to the name of the environment variable holding the key. **Backlog content is sent to that server**, so check your organization's policy before pointing it outside your machine; bkfz prints a notice when the URL is not local.

Optional keys and their defaults:

```yaml
embedding:
  base_url: http://localhost:11434/v1
  model: embeddinggemma
  dimensions: 0            # 0 = model native. Smaller values (Matryoshka models) shrink the index but reduce quality
  api_key_env: ""          # e.g. OPENAI_API_KEY
  query_prefix: "task: search result | query: "   # EmbeddingGemma's query instruction; set "" for models without one
  document_prefix: ""      # e.g. "passage: " for e5-style models
  max_issue_chars: 1200
  chunk_chars: 1000
  max_chunks: 8
```

## Known limitations

- Single-character queries never match (the full-text index is built on 2-character grams)
- Comments search is not supported yet (planned)
- Multiple spaces are not supported
- Attachment bodies (PDF / OCR) are not searchable
- Records deleted on the Backlog side may remain in the local index

## Roadmap

- Comments search
- Multi-space support
- Attachment body search (PDF / OCR)
- Sync as a daemon

## Notes

- Syncing is manual: run `bkfz sync` to update
- Search runs entirely locally (works offline)
- The demo above uses fictional data; regenerate it with `make demo` (requires [VHS](https://github.com/charmbracelet/vhs))

## License

[MIT](LICENSE)
