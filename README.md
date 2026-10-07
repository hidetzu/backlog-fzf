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
- Open the selected entry directly in a browser
- Sync progress (count and ETA) shown live

## Requirements

- A Backlog API key (`BACKLOG_API_KEY`)
- [fzf](https://github.com/junegunn/fzf) for the TUI (installed automatically with Homebrew)

## Install

### Homebrew (macOS / Linux, v0.2.0+)

```bash
brew install hidetzu/tap/bkfz
```

This also installs `fzf`.

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

### From source

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
bkfz --list <query>           List output for fzf reload
bkfz version                  Print version
```

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

## Known limitations

- Single-character queries never match (the full-text index is built on 2-character grams)
- Comments search is not supported yet (planned)
- Multiple spaces are not supported
- Attachment bodies (PDF / OCR) are not searchable
- Records deleted on the Backlog side may remain in the local index

## Roadmap

- Comments search
- TUI keybinding extensions (e.g. `Ctrl-Y` to copy URL)
- Multi-space support
- Attachment body search (PDF / OCR)
- Sync as a daemon

## Notes

- Syncing is manual: run `bkfz sync` to update
- Search runs entirely locally (works offline)
- The demo above uses fictional data; regenerate it with `make demo` (requires [VHS](https://github.com/charmbracelet/vhs))

## License

[MIT](LICENSE)
