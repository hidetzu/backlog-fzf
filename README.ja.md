# backlog-fzf

[![CI](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml/badge.svg)](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hidetzu/backlog-fzf)](https://github.com/hidetzu/backlog-fzf/releases/latest)

[English](./README.md)

![bkfz のデモ: 2 文字の日本語クエリで課題とドキュメントがその場で絞り込まれ、右側にプレビューが表示される](docs/demo.gif)

Nulab Backlog の課題・ドキュメントを `fzf` で横断検索できる CLI です（バイナリ名: `bkfz`）。

Backlog API のデータをローカルに同期し、複数プロジェクトをまたいですばやく検索できます。

pure-Go の単一バイナリで動作し、TUI は外部 `fzf` を起動して連携します。

## 何ができるか

- 課題（issues）とドキュメント（documents）を横断検索
- `fzf` のインクリメンタルフィルタと preview
- 選択対象をブラウザで開く・URL をコピーする（TUI を抜けずに操作可能）
- 同期進捗（件数と ETA）を表示

## 必要なもの

- Backlog API キー（`BACKLOG_API_KEY`）
- [fzf](https://github.com/junegunn/fzf) 0.40+（TUI 利用時。Homebrew なら自動でインストールされます）

## インストール

### Homebrew（macOS、v0.2.0 以降）

```bash
brew install hidetzu/tap/bkfz
```

`fzf` も一緒にインストールされます。Linux では下記のビルド済みバイナリを使ってください。

### ビルド済みバイナリ

[Releases](https://github.com/hidetzu/backlog-fzf/releases/latest) から OS に合ったアーカイブをダウンロードし、`bkfz` を `PATH` の通った場所に置いてください。Linux x86_64 の例:

```bash
VERSION=0.2.0  # 最新バージョンは Releases ページで確認してください
curl -fsSL "https://github.com/hidetzu/backlog-fzf/releases/download/v${VERSION}/backlog-fzf_${VERSION}_Linux_x86_64.tar.gz" | tar xz bkfz
sudo mv bkfz /usr/local/bin/
```

アーカイブは `macOS_arm64`、`macOS_x86_64`、`Linux_arm64`、`Linux_x86_64`（`.tar.gz`）と `Windows_x86_64`（`.zip`）です。fzf は別途インストールしてください（Windows なら `winget install junegunn.fzf`）。

### go install（Go 1.26+）

```bash
go install github.com/hidetzu/backlog-fzf/cmd/bkfz@latest
```

### ソースからビルド（Go 1.26+）

```bash
git clone https://github.com/hidetzu/backlog-fzf.git
cd backlog-fzf
make build   # → bin/bkfz
```

インストール後は `bkfz version` で確認できます。

## クイックスタート

```bash
# 1) 設定ファイル作成
bkfz init

# 2) API キー設定
export BACKLOG_API_KEY=your_personal_api_key

# 3) 同期
bkfz sync

# 4) 検索（TUI）
bkfz
```

## コマンド

```text
bkfz                          fzf TUI を起動
bkfz <query>                  非対話検索（stdout 出力）
bkfz init                     設定ファイル作成
bkfz sync                     差分同期
bkfz sync --refetch           watermark を無視して再取得
bkfz sync -p PROJ             プロジェクト限定同期
bkfz open <KEY>               課題を開く
bkfz open doc <DOC_ID>        ドキュメントを開く
bkfz preview <type> <KEY>     preview 出力
bkfz url <type> <KEY>         URL を表示（type = issue | doc）
bkfz url --copy <type> <KEY>  URL をクリップボードにコピー
bkfz --list <query>           fzf reload 用出力
bkfz version                  バージョン表示
```

## キー操作（TUI）

| キー | 動作 |
|---|---|
| `Enter` | ブラウザで開いて終了 |
| `Ctrl-Y` | URL をクリップボードにコピー（TUI はそのまま） |
| `Ctrl-O` | ブラウザで開く（TUI はそのまま） |
| `Ctrl-/` | プレビューの表示切替 |
| `Shift-↑` / `Shift-↓` | プレビューをスクロール |
| `Esc` / `Ctrl-C` | 終了 |

検索結果は bkfz のインデックスの結果をそのまま表示します（fzf 側の絞り込みは無効）。そのため、説明文やドキュメント本文だけにヒットしたものも一覧に出ます。

`Ctrl-Y` は、fzf 標準のクエリ行での「ヤンク（貼り戻し）」の代わりに割り当てています。

クリップボード: `pbcopy`（macOS）、`clip`（Windows）、`wl-copy` / `xclip` / `xsel`（Linux）。

## 設定

設定ファイル:

```text
$XDG_CONFIG_HOME/bkfz/config.yaml
(未設定なら ~/.config/bkfz/config.yaml)
```

例:

```yaml
space_domain: example.backlog.com
projects:
  - PROJ
  - DOCS
```

DB ファイル:

```text
$XDG_DATA_HOME/bkfz/index.db
(未設定なら ~/.local/share/bkfz/index.db)
```

API キーは設定ファイルに保存せず、`BACKLOG_API_KEY` 環境変数から読み込みます。

## 既知の制限

- 1 文字のクエリはヒットしない（全文検索インデックスは 2 文字 gram で構築）
- comments 検索は未対応（将来対応予定）
- 複数 space は未対応
- 添付ファイル本文（PDF/OCR）は未対応
- Backlog 側で削除されたデータはローカルに残る場合がある

## 今後の予定

- comments 検索への対応
- 複数 space 対応
- 添付ファイル本文（PDF/OCR）検索
- 同期の daemon 化

## 補足

- 同期は手動 `bkfz sync` が前提
- 検索処理はローカルで完結（オフライン利用可）
- 上のデモは架空のデータです。`make demo` で再生成できます（[VHS](https://github.com/charmbracelet/vhs) が必要）

## ライセンス

[MIT](LICENSE)
