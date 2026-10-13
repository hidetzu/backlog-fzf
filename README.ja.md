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
bkfz --semantic <query>       非対話の意味検索（後述）
bkfz --hybrid <query>         キーワード検索と意味検索の融合（Reciprocal Rank Fusion）
bkfz embed                    意味検索インデックスの作成・更新
bkfz embed --rebuild          全件を埋め込み直す（モデル変更後）
bkfz sync --no-embed          意味検索インデックスを更新せずに同期
bkfz version                  バージョン表示
```

## キー操作（TUI）

| キー | 動作 |
|---|---|
| `Enter` | ブラウザで開いて終了 |
| `Ctrl-Y` | URL をクリップボードにコピー（TUI はそのまま） |
| `Ctrl-O` | ブラウザで開く（TUI はそのまま） |
| `Ctrl-S` / `Alt-S` | 今のクエリを意味検索で検索し直す（opt-in、後述） |
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

## 意味検索（任意）

キーワード検索は、覚えている語がそのまま本文に含まれていないとヒットしません。「移行のあとログインできなくなった件があった気がする」のように内容だけ覚えている場合は、意味検索が意味の近さで探します。

既定では無効で、設定に `embedding:` セクションを追加しない限り従来と同じ動作です。テキストの埋め込み（embedding）は別途動かすサーバが行い、既定はローカルの [Ollama](https://ollama.com) なのでデータは端末の外に出ません。

```bash
# 1) Ollama を入れて多言語 embedding モデルを取得（約 600MB）
brew install ollama          # または https://ollama.com/download
ollama pull embeddinggemma

# 2) config.yaml で有効化（以下は既定値。model や base_url は変更可）
cat >> ~/.config/bkfz/config.yaml <<'EOF'
embedding:
  base_url: http://localhost:11434/v1
  model: embeddinggemma
EOF

# 3) 意味検索インデックスを作成（初回のみ。Apple Silicon Mac で約 10 件/秒）
bkfz embed

# 4) 意味で検索
bkfz --semantic "クラウド移行後にログインできなくなった"
bkfz                         # いつも通りキーワードを入力し、Ctrl-S で同じクエリを意味検索
```

- 以後は `bkfz sync` がインデックスを差分更新します（変更のあった記録だけ再埋め込み）。embedding サーバが止まっていても sync 自体は成功し、警告だけ表示します。
- TUI では `Ctrl-S`（または `Alt-S`）で今のクエリを意味検索に掛け直し、プロンプトが `semantic>` に変わります。文字を打つとキーワード検索に戻ります。サーバに接続できない場合は、エラーが一覧の 1 行として表示されます。
- 埋め込む内容: 課題は件名＋説明の先頭 1,200 文字、ドキュメントはタイトル＋本文を 1,000 文字ごとに分割したもの（1 文書あたり最大 8 個）。状態・担当者・日付は含めません。
- インデックスは 1 記録あたり約 3KB 増えます（768 次元モデルの場合）。`model`（または `dimensions`）を変えたら `bkfz embed --rebuild` が必要です。
- `bkfz --hybrid <query>` はキーワード検索と意味検索の順位を融合します（両方で見つかった記録が上位）。`tui_mode: hybrid` にすると `Ctrl-S` がこれを使います。評価セットでは両者の成績が同じだったため、既定は `semantic` のままです。
- OpenAI 互換の `/v1/embeddings` を持つサーバならどれでも使えます（LM Studio、llama.cpp server、各社のクラウド API）。クラウド API では `api_key_env` にキーを持つ環境変数名を指定します。**Backlog の本文がそのサーバに送信される**ので、端末の外に向ける場合は組織のポリシーを確認してください。URL がローカルでない場合、bkfz は実行時にその旨を表示します。

省略可能なキーと既定値:

```yaml
embedding:
  base_url: http://localhost:11434/v1
  model: embeddinggemma
  dimensions: 0            # 0 = モデルのネイティブ次元。小さくすると（Matryoshka 対応モデル）インデックスは縮むが品質は落ちる
  api_key_env: ""          # 例: OPENAI_API_KEY
  query_prefix: "task: search result | query: "   # EmbeddingGemma のクエリ指示。指示のないモデルでは "" に
  document_prefix: ""      # e5 系なら "passage: " など
  max_issue_chars: 1200
  chunk_chars: 1000
  max_chunks: 8
  tui_mode: semantic       # Ctrl-S の動作: semantic | hybrid
```

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
