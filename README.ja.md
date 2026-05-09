# backlog-fzf

[![CI](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml/badge.svg)](https://github.com/hidetzu/backlog-fzf/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hidetzu/backlog-fzf)](https://github.com/hidetzu/backlog-fzf/releases/latest)

[English](./README.md)

Nulab Backlog の課題・ドキュメントを `fzf` で横断検索できる CLI です（バイナリ名: `bkfz`）。

Backlog API のデータをローカルに同期し、複数プロジェクトをまたいですばやく検索できます。

pure-Go の単一バイナリで動作し、TUI は外部 `fzf` を起動して連携します。

## 何ができるか

- 課題（issues）とドキュメント（documents）を横断検索
- `fzf` のインクリメンタルフィルタと preview
- 選択対象をブラウザで直接オープン
- 同期進捗（件数と ETA）を表示

## 必要なもの

- [fzf](https://github.com/junegunn/fzf)（TUI 利用時）
- Backlog API キー（`BACKLOG_API_KEY`）
- Go 1.26+（ソースからビルドする場合）

## インストール

```bash
go install github.com/hidetzu/backlog-fzf/cmd/bkfz@latest
```

または次の手順でもビルドできます:

```bash
git clone https://github.com/hidetzu/backlog-fzf.git
cd backlog-fzf
make build
```

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
bkfz --list <query>           fzf reload 用出力
```

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
- URL コピーなど、TUI キーバインドの拡張（例: `Ctrl-Y`）
- 複数 space 対応
- 添付ファイル本文（PDF/OCR）検索
- 同期の daemon 化

## 補足

- 同期は手動 `bkfz sync` が前提
- 検索処理はローカルで完結（オフライン利用可）

## ライセンス

[MIT](LICENSE)
