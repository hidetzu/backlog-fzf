# backlog-fzf

[![CI](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml/badge.svg)](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hidetzu/backlog-fzf)](https://github.com/hidetzu/backlog-fzf/releases/latest)

[日本語](./README.ja.md)

A CLI for cross-project fuzzy search across Nulab Backlog issues and documents (binary name: `bkfz`).

It mirrors Backlog API data into a local store so you can search across multiple projects quickly.

The binary is pure-Go and self-contained; the TUI launches an external `fzf` process for interaction.

## Features

- Fuzzy search across both issues and documents
- Incremental filtering and preview via `fzf`
- Open the selected entry directly in a browser
- Sync progress (count and ETA) shown live

## Requirements

- [fzf](https://github.com/junegunn/fzf) (when using the TUI)
- A Backlog API key (`BACKLOG_API_KEY`)
- Go 1.26+ (when building from source)

## Install

```bash
go install github.com/hidetzu/backlog-fzf/cmd/bkfz@latest
```

Or build from source:

```bash
git clone https://github.com/hidetzu/backlog-fzf.git
cd backlog-fzf
make build
```

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

- Queries shorter than 3 characters never match (trigram tokenizer constraint)
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

## License

[MIT](LICENSE)
