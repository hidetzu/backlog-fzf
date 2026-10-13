# backlog-fzf 意味検索（semantic search）導入調査

- 作成日: 2026-10-13
- 対象: `main` @ 142a715（v0.2.x 相当）
- ステータス: 調査・設計 → レビュー（2026-10-13、23 件の指摘）反映 → **MVP（issues.md S2〜S9）実装済み**。反映内容は §14、実装時の差分は §15
- 関連: [ADR 案](./adr-0001-semantic-search.md) / [Issue 分割案](./issues.md) / [PoC 結果](./poc/README.md)

## 0. 結論（先に要点）

**ゴール**: backlog-fzf のシンプルさ（pure-Go 単一バイナリ、ローカル完結、fzf の即応性）を維持したまま、「キーワードを思い出せない過去の課題・文書」を見つけられるようにする。

**推奨**:

| 項目 | 推奨 | 一言の根拠 |
|---|---|---|
| 位置づけ | 既存 FTS5 はそのまま。意味検索は **opt-in の追加機能** | 置き換えは不採用（依頼の前提）。未設定ユーザーには一切コストを載せない |
| UX（MVP） | `bkfz --semantic <query>`（非対話）＋ TUI の **ワンショットキー（ctrl-s）** | 逐次入力のたびに embedding を作らない。FTS で見つからない時だけ「意味で探す」 |
| ベクトル格納・検索 | 通常テーブルの BLOB ＋ **純 Go 総当たり**（cosine） | 新規依存ゼロ。実測 1.5 万件×768 次元で 70ms、256 次元で 35ms |
| 拡張の扱い | sqlite-vec（modernc 同梱）は差し替え候補として温存。Vec1 は不採用（未リリース＋ドライバ変更が必要） | 詳細は §6 |
| embedding 生成 | **OpenAI 互換 HTTP（`/v1/embeddings`）のみ実装**。既定はローカル Ollama（`embeddinggemma`、ネイティブ 768 次元） | モデル推論をバイナリに内蔵しない。ローカル／クラウドをユーザーが選べる。256 次元への切り詰めは品質が落ちた（poc §4.1）ので既定にしない |
| 対象テキスト | issue = `summary` + `description` 先頭 1,200 字で 1 ベクトル。document = `title` + 本文 1,000 字 chunk（最大 8） | 本文長の分布から決定（§8） |
| 更新判定 | 埋め込み対象テキストの hash を保存し、差分のみ再生成。初回は `bkfz embed` の明示実行。以後は `bkfz sync` の末尾で差分更新 | 既存 watermark 方式に影響を与えない。初回の重さを `sync` に載せない |
| Hybrid | **Phase 2**（RRF で FTS ＋ vector を融合） | ワンショット前提なら追加コストは FTS 1 回分だけ。見送る理由は評価セット（S10）が無いこと |

**実データでの確認**（この端末のローカル index、10,559 issues / 2,799 documents のうち直近 2,500 issues + 300 documents を Ollama でローカル埋め込み。**768 次元・本文先頭 1,200 字・chunk なし**で実施）: 言い換えクエリ 6 件すべてで現行 FTS はヒット 0 件、意味検索は 6 件中 5 件で意図した課題が 1〜2 位、残り 1 件も同種の課題群が上位に並んだ。同じ 6 クエリを 256 次元で再実行すると 4 件に落ち、1 件は圏外になった（[poc/README.md §4.1](./poc/README.md)）。chunk 集約の品質影響は未測定で、S10 の評価セットで検証する。

クエリ 1 件の見込み: embedding 27〜65ms ＋ 総当たり 約 70ms（17,000 件 × 768 次元の見込み）≒ **100〜140ms**。

**依頼の評価軸との対応**:

| 評価軸 | 評価 | 根拠 |
|---|---|---|
| OSS として導入が簡単か | ○ YAML に 3 行 ＋ `ollama pull` 1 回 | §4.2, §5.2 |
| 外部サービスなしで使えるか | ○ 既定がローカル Ollama。クラウドは明示 opt-in | §5, §5.1 |
| バイナリ配布が複雑にならないか | ○ 新規依存ゼロ、goreleaser 設定不変 | §6, §7 |
| 初回 sync が極端に重くならないか | ○ `sync` には載せない。初回は別コマンド `bkfz embed`（約 30 分、再開可能） | §7.2, §9 |
| 意味検索を使わないユーザーにコストを押し付けないか | ○ 未設定ならテーブルも HTTP も発生しない。キーバインド 1 つ増えるのみ | §7 |
| 実際の Backlog 検索体験が改善するか | ○（6 クエリの目視） | §2, poc/README §4 |

## 1. 現状アーキテクチャの整理

### 1.1 構成

```
cmd/bkfz/main.go        CLI ディスパッチ、fzf 連携（--list / preview / --action）、同期の進捗表示
internal/backlog        Backlog REST クライアント（rate limit 対応、ページング）
internal/syncer         watermark による増分同期（issues: updatedSince、documents: 降順走査＋early stop）
internal/index          SQLite（modernc.org/sqlite v1.50.0）、contentless FTS5 + bigram、LIKE 後置フィルタ
internal/tui            fzf の起動と --bind 組み立て
internal/config         XDG 準拠の config.yaml / index.db パス
```

- **pure-Go・CGO なし・単一バイナリ**。goreleaser で linux/darwin/windows × amd64/arm64（windows は amd64 のみ）を配布。Homebrew cask あり。バイナリ約 6.5MB（`-s -w`）。
- SQLite ドライバは `modernc.org/sqlite`（C→Go トランスパイル）。`BIGRAM()` SQL 関数を Go で登録し、FTS5 トリガから呼んでいる。
- 検索フロー（TUI）: fzf は `--disabled` で自前フィルタを無効化し、**キー入力のたびに `bkfz --list {q}` を新プロセスで起動**して結果を差し替える（`change:reload`）。プレビューも `bkfz preview {1} {2}` を都度起動。つまり「1 クエリ = 1 プロセス = DB を開いて閉じる」が前提。
- 検索は issues / documents を別々に FTS5 で引き、**両者を `updated_at` 降順でマージして上位 N 件**（FTS の `rank` はマージ時点で捨てられる）。

### 1.2 データ規模（この端末の実 index）

| 項目 | 値 |
|---|---|
| issues | 10,559 件（description 平均 636 字、最大 74,193 字、1,200 字超は 1,173 件） |
| documents | 2,799 件（body 平均 2,760 字、最大 182,640 字、1,000 字超は 1,291 件、1,000 字 chunk 換算で平均 2.3 本、8 本を超えるのは 209 件） |
| index.db | 72MB |

### 1.3 現行検索の性質（意味検索との役割分担に効く点）

- bigram FTS ＋ LIKE 後置フィルタのため、**クエリの各語が部分文字列として実在**しないとヒットしない。言い換え・表記ゆれ・英日混在（`Playwright` / `PlayWight` / `ブラウザ自動テスト`）はすべて 0 件になる。
- 逆に、課題キー・固有名詞・ステータス・担当者など「文字列として覚えているもの」は高速かつ確実。
- 1 文字クエリは不可（既知の制限）。

## 2. 意味検索で解決したい具体的ユースケース

| # | ユースケース | 例（抽象化） | 現行 FTS | 意味検索 |
|---|---|---|---|---|
| U1 | 症状を言い換えて過去の障害を探す | 「移行後にログインできなくなった」→ 実際の件名は「〜化後のログイン不可事象」 | ✗ 0 件 | ◎ 1〜2 位 |
| U2 | 性能問題の過去対応を探す | 「処理が遅いので高速化したい」→ 「速度チューニング」「パフォーマンス改善」系 | ✗ | ◎ 上位に複数 |
| U3 | ツール名を思い出せない | 「ブラウザの自動テストスクリプトを直す」→ 件名はツールの固有名（しかも綴りゆれ） | ✗ | ◎ 1 位 |
| U4 | 手順書・環境構築ドキュメントを探す | 「開発環境を仮想マシンにする」→ 関連 issue と「[手順] 〜環境構築」文書が混在して上位 | ✗ | ◎ issue と doc が並ぶ |
| U5 | 制度・仕様変更の類似対応を探す | 「法改正対応でマスタの項目を増やす」 | ✗ | ○ 同カテゴリの課題群 |
| U6 | 定型課題のシリーズを探す | 「週次の作業予定」→ 「週作業Todo（期間）」 | ✗ | ◎ 同シリーズが上位独占 |
| U7 | 起票前の重複確認 | 書きかけの課題本文をそのままクエリに | ✗（語が長い） | ◎（長文クエリに強い） |

**意味検索が向かない（FTS に任せる）もの**: 課題キーで開く、担当者／ステータス／プロジェクトで絞る、正確な語句の検索、1〜2 文字の逐次入力。

### 2.1 既存 FTS5 との役割分担

| | FTS5（現行） | 意味検索（追加） |
|---|---|---|
| 得意 | 既知の語・キー・固有名詞、絞り込み、逐次入力 | 言い換え、説明文での探索、長文クエリ、重複探し |
| レイテンシ | 数 ms | 約 100〜140ms（embedding ＋ 総当たり） |
| 外部依存 | なし | embedding サーバ（ローカル Ollama など） |
| 既定 | ON | OFF（設定した人だけ） |

役割分担の原則: **「まず FTS で打つ。出なければ意味で探す」**。この順序を UX に落としたのが §3 のワンショットキー。

## 3. UX 候補と比較

依頼の 4 案に、PoC で確認した制約（fzf の挙動、embedding レイテンシ）を当てはめた。

| 案 | 内容 | 利点 | 欠点 | 判定 |
|---|---|---|---|---|
| (1) FTS5 のみ | 現状維持 | 変更なし | U1〜U7 を解決しない | ベースライン（維持） |
| (2) `bkfz --semantic "..."` | 明示的に意味検索を起動（非対話）。引数なしは usage エラー | 実装が最小。非対話・スクリプトから使える。FTS の検索関数に手を入れない | 単独では TUI から使えない（(4b) と組み合わせる） | **MVP 採用** |
| (3) `bkfz --hybrid "..."` | FTS ＋ vector を RRF で融合 | 「一部のキーワードは覚えている」ケースに最強 | ワンショット前提なら追加コストは FTS 1 回分で小さい。見送る理由は、semantic 単独との優劣を測る評価セット（S10）が未整備で、融合パラメータ（RRF の k など）を根拠なく決めることになる点 | **Phase 2**（S10 の評価セットで semantic / hybrid を比較してから） |
| (4) TUI 内で切替 | (4a) モードトグル / (4b) ワンショットキー | 再起動不要。FTS の即応性を維持したまま必要な時だけ意味検索 | fzf のバージョン要件（トグル実装は `transform` = 0.45+） | **(4b) を MVP 採用**（0.40 の範囲で実装可） |

### 3.1 採用する UX（MVP）

```
bkfz                               # 従来通り。入力中は FTS
  ctrl-s / alt-s                   # 今のクエリ文字列を「意味で」検索し直す（ワンショット）
                                   # prompt が "semantic> " に変わる。再度文字を打つと FTS に戻る
bkfz --semantic <query>            # 非対話。スコア順に出力。引数なしは usage エラー
bkfz --list --semantic {q}         # fzf 用（内部）。常に exit 0
```

fzf 側のキーバインド（既存 `--bind` への追加。要件 0.40 は据え置き）:

```
--bind 'ctrl-s:reload(bkfz --list --semantic {q})+change-prompt(semantic> )+change-header:<key help>'
--bind 'alt-s:…同上…'                                  # ctrl-s が端末の XOFF に取られる環境向けの予備
--bind 'change:reload(bkfz --list {q})+change-prompt(> )+change-header:<key help>'
```

- `change-header:` はコロン形式で**行末まで取る**ため常に末尾に置く（既存コードのコメント通り）。`change-prompt` はその前。
- `--list` の引数解釈: `runList` は現在すべての引数を結合してクエリにしているため、先頭の `--semantic` を剥がす処理を加える。空クエリで ctrl-s を押した場合は embedding を呼ばず、FTS の空クエリと同じ「最近更新順」を返す。
- **エラーの見せ方（fzf 内では stderr が画面を壊す）**: `--list --semantic` は常に exit 0 とし、失敗時は stdout に 1 行の擬似行 `error\t-\t-\t-\t✗ <message>\t-` を出す。例: `✗ embedding server unreachable: http://localhost:11434 (see README)`、`✗ semantic index not built; run: bkfz embed`。`runTUI` は kind が `error` の行で Enter が押されても何もしない（`parseSelectedRef` の後で弾く）。`bkfz preview error -` は案内文を返す。
- embedding 未設定で ctrl-s を押した場合は、`change-header(semantic search is not configured; see README)` のみを束ねる（bkfz を呼ばない）。
- 非対話の `bkfz --semantic` は通常通り stderr にエラーを出して exit 1。
- 結果順序は **スコア順**（現行の `updated_at` マージは適用しない）。issue / doc は同じスコア空間で混ぜる。
- 検索側でも整合性を確認する: `embeddings` テーブルが無ければ「`bkfz embed` を実行してください」、`embedding_meta` の model / dimensions が設定（または応答ベクトルの次元）と食い違えば「`bkfz embed --rebuild`」を案内する。

### 3.2 逐次入力と embedding 生成の両立（検証済みの選択肢）

| 方式 | 仕組み | 評価 |
|---|---|---|
| A. 逐次入力では embedding しない（ワンショット） | ctrl-s / `--semantic` でのみ生成 | **MVP**。最も単純で、FTS の即応性を一切損なわない |
| B. bkfz 内 debounce | `--list --semantic --delay 300ms {q}` のように bkfz が sleep してから embedding。fzf は新しい reload が来ると**実行中の reload プロセスを終了させる**（`src/core.go`: `if reading { reader.terminate(); nextCommand = command }` を確認）ので、途中のキー入力分は embedding に到達しない | Phase 2 の「逐次意味検索モード」で採用可。Windows は fzf 0.71 で子プロセス終了が修正されている点に注意 |
| C. クエリ embedding のキャッシュ | `sha256(model|dimensions|prefix|text) → vec` を SQLite に保存 | 同じクエリの再実行にしか効かない一方、読み取り専用だった `--list` プロセスが DB に書くことになり（`sync` / `embed` と同時実行で SQLITE_BUSY の可能性）、上限管理や `--rebuild` 時の破棄も要る。**MVP から外し Phase 2** |
| D. 常駐プロセス | ベクトルとモデル接続をメモリに保持 | Roadmap の「Sync as a daemon」と合流する将来案。MVP では不要 |

クエリ 1 件の embedding は Ollama（embeddinggemma、M2 Max、モデルがウォーム状態）で **27〜65ms** を実測。ワンショットなら体感上問題ない。

## 4. 推奨アーキテクチャ

```
                 bkfz sync ──► syncer ──► index(issues/documents, FTS5)   ← 従来通り
                      │
                      └─(embedding 有効 かつ 索引構築済み)─► embed stage（差分のみ。失敗は警告）
                 bkfz embed ──────────────────────────────► embed stage（初回構築 / --rebuild）
                                               │  対象テキスト生成 → hash 比較 → 差分のみ
                                               ▼
                           embedder (HTTP, OpenAI 互換 /v1/embeddings)  ← Ollama / LM Studio / llama.cpp / クラウド
                                               │
                                               ▼
                           index.VectorStore (embeddings テーブル, BLOB float32)

 bkfz --semantic q / ctrl-s ──► embedder(q) ──► VectorStore.Search(k) ──► issues/documents を JOIN ──► スコア順出力
```

### 4.1 パッケージ境界（案）

| パッケージ | 役割 | 備考 |
|---|---|---|
| `internal/embed` | `Embedder` interface（`Embed(ctx, []string) ([][]float32, error)`）と OpenAI 互換クライアント | リトライ／タイムアウト／バッチ分割（既定 32 件）。`httptest` で単体テスト |
| `internal/embed`（同上） | 対象テキスト生成（issue 1 本 / document chunk）、hash | 純関数。テーブルテスト |
| `internal/index`（拡張） | `embeddings` / `embedding_meta` テーブル、`VectorStore`（BLOB 総当たり） | FTS 周りのコードには触れない |
| `internal/syncer`（拡張） | `Embed(ctx, embedder, db, opts)` ステージ。進捗は既存 `OnProgress` に `StageEmbedded` を追加し `[embedded: N/total ETA]` と表示 | watermark とは独立（hash 差分） |
| `cmd/bkfz` | `--semantic`、`embed` サブコマンド、ctrl-s / alt-s のキーバインド、擬似行 | |
| `internal/config` | `embedding:` セクション | 未設定 = 機能 OFF |

### 4.2 設定（案）

```yaml
space_domain: example.backlog.com
projects: [PROJ, DOCS]

# 省略時: 意味検索は無効。従来と完全に同じ動作
embedding:
  base_url: http://localhost:11434/v1   # OpenAI 互換。Ollama の既定
  model: embeddinggemma
  # dimensions: 256                     # 省略時はモデルのネイティブ次元（embeddinggemma は 768）。
                                        # Matryoshka 対応モデルで索引サイズを抑えたい時だけ指定（品質は落ちる。poc §4.1）
  api_key_env: BKFZ_EMBEDDING_API_KEY   # 任意。クラウド API 利用時のみ（キー自体は環境変数）
  # 以下は省略可（既定値を示す）
  # query_prefix: "task: search result | query: "   # embeddinggemma の推奨。他モデルでは "query: " 等
  # document_prefix: ""                             # e5 系なら "passage: " 等
  # max_issue_chars: 1200
  # chunk_chars: 1000
  # max_chunks: 8
```

キー一覧は issues.md S2 と同一。`base_url` が localhost 以外のときは、`bkfz embed` / `sync` の embed ステージ実行時に「Backlog のデータを外部へ送信します」と stderr に明示する（§7.3）。

### 4.3 スキーマ（案）

```sql
-- embedding 有効時にだけ作成する（既存 DB は無変更）
CREATE TABLE IF NOT EXISTS embeddings (
  kind         TEXT    NOT NULL,   -- 'issue' | 'doc'
  ref_id       INTEGER NOT NULL,   -- issues.id / documents.id
  chunk        INTEGER NOT NULL,   -- 0.. (issue は常に 0)
  content_hash TEXT    NOT NULL,   -- sha256(model | text)。dimensions は embedding_meta で照合するので含めない
  vec          BLOB    NOT NULL,   -- float32 little-endian, L2 正規化済み
  PRIMARY KEY (kind, ref_id, chunk)
);
CREATE TABLE IF NOT EXISTS embedding_meta (
  key TEXT PRIMARY KEY, value TEXT NOT NULL   -- model, dimensions, schema_version
);
```

- `embedding_meta.dimensions` は、設定で `dimensions` を省略した場合は**最初の応答ベクトルの長さ**を記録する。以後、embed ステージと検索の両方でこれと照合し、model / dimensions が食い違ったら `bkfz embed --rebuild` を促すエラーにする（黙って混在させない）。
- 検索は `SELECT kind, ref_id, chunk, vec FROM embeddings` を全件走査し、Go で内積を取りながら **(kind, ref_id) ごとの最大スコア**を保持し、集約後の上位 k を返す（chunk 単位で top-k を取ってから畳むと k 件未満になるため）。`Hit.Chunk` は最大スコアを出した chunk 番号（表示・デバッグ用）。
- `query_cache` は MVP に含めない（§3.2-C）。

### 4.4 ベクトル格納の抽象化は必要か

**小さな interface 1 つで十分**（backend プラグイン機構は作らない）。

```go
type VectorStore interface {
    Upsert(ctx, kind string, refID int64, chunks []Chunk) error   // (kind, ref_id) の chunk 集合を丸ごと置換（余った chunk 番号は削除）
    Delete(ctx, kind string, refID int64) error
    Search(ctx, query []float32, k int) ([]Hit, error)           // (kind, ref_id) 単位に集約済みの上位 k。Hit{Kind, RefID, Chunk, Score}
    Hashes(ctx, kind string) (map[int64][]string, error)          // 差分判定用（ref_id → chunk 順の hash）
    Rebuild(ctx) error                                            // Vec1 のような「学習→再構築」型のために残す
}
```

- 純 Go 総当たり → sqlite-vec（`vec0`）→ Vec1 の順に差し替える余地はこれで足りる。Vec1 は学習が必要なため `Rebuild` 相当のライフサイクルが要る点だけ先に interface に含めておく。
- SQL で issues / documents と JOIN する部分は store の外に置く（backend が変わっても JOIN は変わらない）。

## 5. embedding 方式の比較

| 方式 | 品質 | 速度（実測／公称） | 外部送信 | 導入の手間 | 単一バイナリ維持 | 判定 |
|---|---|---|---|---|---|---|
| ローカル Ollama（embeddinggemma 300M, 768d, Matryoshka 128〜768） | 多言語 100+ 言語。日本語の言い換えで実用（§2） | 索引 **9.7 件/s**（M2 Max, 1 件平均 390 トークン, batch 32）、クエリ 27〜65ms | なし | `brew install ollama && ollama pull embeddinggemma` | ○（HTTP 経由） | **既定** |
| ローカル Ollama（bge-m3 1024d / qwen3-embedding 0.6B / multilingual-e5） | 同等〜上。bge-m3 は 8k ctx | より重い（モデル 1〜2GB） | なし | 同上 | ○ | 設定で選択可 |
| ローカル LM Studio / llama.cpp server | モデル次第 | 同上 | なし | GUI or 自前ビルド | ○ | OpenAI 互換なので追加実装なし |
| クラウド API（OpenAI text-embedding-3-small $0.02/1M tok、Voyage、Gemini） | 高い | 速い（索引は分単位）。クエリは往復 100〜300ms | **あり** | API キー | ○ | 明示 opt-in のみ。既定にしない |
| bkfz 内蔵推論（ONNX Runtime / gomlx 等） | モデル次第 | — | なし | なし | ✗（ONNX は共有ライブラリ or CGO。純 Go 推論は遅く依存が巨大） | 不採用 |
| bkfz 内蔵 静的埋め込み（Model2Vec `potion-multilingual-128M`, 256d） | 文単位の言い換えには Transformer 系より劣る（ただし bigram FTS よりは上） | 極めて速い（トークン→ベクトル表引き＋平均） | なし | モデル（数百 MB）の初回ダウンロード | ○（純 Go で実装可能だが tokenizer 移植が必要。Go 実装は未発見） | 将来案（「外部プロセスすら不要」を狙う時） |

**費用感**: 実測 1 件 390 トークン × 約 17,000 ベクトル ≒ 6.6M トークン。OpenAI small なら約 $0.13。ローカルなら 0 円だが時間がかかる（§8）。

**Anthropic 自身は embedding API を提供していない**（Voyage を推奨）ため、クラウド利用時も OpenAI 互換の 1 クライアントで Ollama / OpenAI / Gemini（互換 endpoint）/ LM Studio を同時にカバーできる。Voyage のみ API 形が異なるため、必要になった時点で別対応。

### 5.1 Backlog 社内情報の外部送信について

- 課題本文・文書本文は多くの組織で社外送信が禁止または承認制。**既定をローカルにし、クラウドは明示設定＋警告表示**とする。
- bkfz は API キーを config に保存しない（既存方針と同じく環境変数）。
- README に「何がどこへ送られるか」（summary / description 先頭 1,200 字 / 文書本文 chunk）を明記する。

### 5.2 完全 offline / local での現実的な方法

1. **Ollama**（推奨）: 1 コマンドで導入でき、モデル管理・ウォーム保持・バッチ API（`/api/embed`, `/v1/embeddings` 両方。`dimensions` 対応を確認済み）が揃う。
2. LM Studio / llama.cpp server: OpenAI 互換なら設定の `base_url` を変えるだけ。
3. 将来: Model2Vec 系の静的埋め込みを純 Go で内蔵（外部プロセス不要だが品質は落ちる）。

## 6. Vec1 / sqlite-vec / その他候補の技術比較

| 候補 | 実体 | 状態（2026-10 時点） | modernc.org/sqlite（現行ドライバ）で使えるか | 検索方式 | 判定 |
|---|---|---|---|---|---|
| **Vec1**（sqlite.org 公式、Dan Kennedy） | IVFADC ＋ OPQ の ANN。L2 / cosine。学習（`vec1_train`）が必要。NN モード（exact）は 5,000 件未満向け | **未リリース**（2026-03〜04 のフォーラム: "no release yet, getting closer"。API は "proposed"）。ARM / WASM の SIMD 未整備、テスト不足と自己申告 | **使えない**。Go で使う手段は `ncruces/go-sqlite3/ext/vec1`（wasm ビルド）のみ → **ドライバ総入れ替え**（`BIGRAM` 登録、`:memory:` 挙動、VFS、全テストの再検証） | ANN（要学習）／exact | **不採用**（将来、正式リリース後に再評価） |
| **sqlite-vec**（asg017） | `vec0` 仮想テーブル。float32 / int8 / bit、metadata・partition・aux 列。**総当たり**（ANN 索引なし） | v0.1.x（pre-v1、破壊的変更あり得る） | **使える**: `modernc.org/sqlite/vec` が v0.1.9 を純 Go で同梱（**v1.60.1 以降**。v1.60.0 以前には無い）。`import _ "modernc.org/sqlite/vec"` だけ。バイナリ **+0.3MB**（6.52→6.83MB）。5 ターゲットのクロスビルド成功を確認。制約: `sqlite3_auto_extension` でグローバル登録、`RegisterPageCache` と併用不可（bkfz は未使用） | 総当たり（C 実装、SIMD 無しでも Go の約 3 倍速。実測 2.7〜3.5 倍） | **差し替え候補として温存**。MVP では使わない |
| **純 Go 総当たり** | 通常テーブルの BLOB を読み、Go で内積 | — | 当然可。**modernc の更新すら不要** | 総当たり | **MVP 採用** |
| libSQL / Turso の native vector | SQLite フォーク | — | 不可（フォーク） | DiskANN | 不採用 |
| sqlite-vss | FAISS ベース | 非推奨（sqlite-vec の前身） | 不可 | ANN | 不採用 |
| 純 Go HNSW ライブラリ（coder/hnsw 等）、chromem-go | 外部 Go 依存 | 活発 | 可 | ANN / 総当たり | 10 万件未満では不要。依存を増やす価値がない |

### 6.1 PoC ベンチ（抜粋、詳細は [poc/README.md](./poc/README.md)）

新規プロセスが DB を開いて top-50 を返すまでの時間（`bkfz --list` の実行形態を模擬）:

| 件数 × 次元 | 純 Go float32 | 純 Go int8 | sqlite-vec `vec0` | DB 増分（float32 + vec0 の二重格納） |
|---|---|---|---|---|
| 15,000 × 768 | 70ms | 45ms | 25ms | 109MB |
| 15,000 × 256 | 35ms | 23ms | 10ms | 37MB |
| 50,000 × 768 | 211ms | 136ms | 78ms | 361MB |
| 100,000 × 256 | 192ms | 134ms | 58ms | 242MB |

- 現実的な規模（1.5〜2 万ベクトル、768 次元）では **純 Go でも 70ms 前後**（256 次元なら 35ms）。ワンショット用途では embedding 側（27〜65ms）と同程度で、ボトルネックにならない。
- 5 万件 × 768 次元を超えるあたりから sqlite-vec への差し替え（または int8 化）が効いてくる。interface（§4.4）があれば後から入れ替えられる。
- 測定環境は Apple M2 Max だが Go ツールチェーンが darwin/amd64（Rosetta）だった点に注意。ネイティブ arm64 ではさらに速いはず。

### 6.2 Vec1 を採用した場合のビルド・配布上の影響（参考）

- C 単一ファイル（`vec1.c`）のロード可能拡張。AVX2 でビルドしたバイナリは非対応 CPU で動かないため `vec1multi.so` のような複数バリアントが必要 → **CGO 化・動的ロード・プラットフォーム別配布**のどれかが必須になり、goreleaser の現在の単純な構成（`CGO_ENABLED=0`、5 ターゲット）を壊す。
- 純 Go で使う唯一の経路（ncruces の wasm ビルド）はドライバ変更を伴う。FTS5 ＋ `BIGRAM()` の移植は可能だが、本件のためだけに行う変更としては大きすぎる。
- IVFADC は「学習 → rebuild」ライフサイクルを持つ。1〜2 万件規模では Vec1 自身が NN（exact）モードを推奨しており、ANN の恩恵がない。

### 6.3 sqlite-vec を採用した場合の利点・欠点（参考）

- 利点: import 1 行、SQL で KNN（`WHERE embedding MATCH ? AND k = ?`）、metadata 列で `project_key` 等の絞り込みが SQL 側で可能、int8 / bit 量子化が組み込み、Go 実装より約 3 倍速。
- 欠点: pre-v1（`DELETE` の挙動変更などの前例あり）、modernc を v1.60.1 以上に上げる必要、`vec0` の shadow table 群でスキーマが複雑化しマイグレーションが面倒、グローバル auto_extension。
- 結論: 「必要になったら入れる」が妥当。今は不要。

## 7. 既存ユーザーへの影響をゼロに保つ設計

| 観点 | 設計 |
|---|---|
| バイナリ | 新規依存なし（HTTP クライアントは標準ライブラリ）。サイズ・起動時間は不変 |
| DB | `embedding:` 未設定なら新テーブルを作らない。既存 index.db はバイト単位で無変更 |
| 検索経路 | FTS の検索関数（`SearchIssues` / `SearchDocuments` / `search`）には触れない。`runList` は先頭の `--semantic` を剥がす分岐だけ増え、無ければ従来と同じ関数を呼ぶ |
| TUI | ctrl-s / alt-s のキーバインドだけ増える。未設定時はヘッダに案内を出すのみ（bkfz を呼ばない） |
| sync | 未設定なら embed ステージは呼ばれない。設定済みでも索引未構築（`embeddings` 不在）なら呼ばれず、`bkfz embed` の案内を 1 行出すだけ。`--no-embed` で抑止可。embed ステージの失敗は**警告のみで exit 0**（issues / documents の同期結果は保持） |
| 障害時 | embedding サーバに繋がらない場合、`bkfz --semantic` は stderr にエラー（接続先と設定手順）、ctrl-s は擬似行で同じ内容を表示（§3.1）。FTS は影響を受けない |

### 7.1 インデックスサイズ

| 構成 | ベクトル数（この index） | 追加サイズ |
|---|---|---|
| issue 1 本 ＋ doc 1,000 字 chunk（最大 8）, **768d float32（既定）** | 10,559 + 6,473 ≒ 17,000 | 約 52MB（72MB → 124MB） |
| 同上, 256d float32（`dimensions: 256`、品質は落ちる） | 17,000 | 約 17MB |
| 同上, 768d int8（将来） | 17,000 | 約 13MB |

### 7.2 同期時間（初回）

| 方式 | 見積り |
|---|---|
| Ollama embeddinggemma（M2 Max） | 約 10 件/s → 17,000 件で **約 30 分**（1 回きり。バッチごとにコミットするので中断・再開可能） |
| 同上、Intel Mac / GPU なし Linux | 2〜5 倍程度遅い可能性（未計測） |
| OpenAI text-embedding-3-small | 数分、約 $0.13 |

初回の重さは opt-in ユーザーだけが払い、`bkfz sync` には載せない（初回は `bkfz embed` の明示実行のみ）。`bkfz embed` はバックグラウンド実行を想定し、進捗は既存 `progressPrinter` に `StageEmbedded` を足して `[embedded: N/total ETA]` と表示する。増分は変更行のみなので通常は数秒。

### 7.3 メモリ・検索速度

- `--semantic` プロセスが読むベクトルは 52MB（768d）。読み込み＋内積で約 70ms の見込み（15,000 × 768 の実測 70ms）。
- クエリ embedding 27〜65ms（ローカル、モデルがウォーム状態）。合計 100〜140ms が目安。
- **embedding サーバ側の常駐メモリ**は bkfz の外で発生する: Ollama は embeddinggemma を既定 5 分（`keep_alive`）保持し、その間 **673MB**（`ollama ps` 実測）を使う。アイドル後は解放され、次回のクエリでロード時間（数百 ms〜数秒）が乗る。意味検索を設定しないユーザーには一切発生しない。

## 8. 対象テキストと chunking の判断

| 対象 | 入力 | 根拠 |
|---|---|---|
| issue | `{document_prefix}title: {summary} | text: {description 先頭 1,200 字}` → 1 ベクトル | description 平均 636 字。1,200 字超は 11%（1,173 件）。課題は冒頭に要旨があることが多く、chunk 化のコスト（ベクトル数 ×2〜3）に見合わない |
| document | `{document_prefix}title: {title} | text: {chunk}`、1,000 字 chunk、100 字 overlap、**最大 8 chunk**、スコアは chunk の最大値 | 1,000 字超は 1,291 件、平均 2.3 chunk。上限 8 で 6,473 本（無制限なら 8,953 本）。上限を超える 209 件は末尾が落ちるが、手順書・設計書の要旨は前半に偏る |
| 含めない | status / assignee / due / project_key / 日付 | 意味ではなく属性。絞り込みは FTS 側・将来の metadata フィルタで |
| 前処理 | 連続する空白・空行の正規化のみ（Backlog 記法の除去は MVP では行わない） | 過度な整形をしない。効果は S10 で測ってから |
| プロンプト | embeddinggemma の推奨形式（query: `task: search result | query: …`、document: `title: … | text: …`）を既定に内蔵。`query_prefix` / `document_prefix` で他モデル用に上書き可 | モデルカードの推奨。PoC の実データ検証は区切りが改行（`title: …\ntext: …`）だった点だけ異なる（本設計では公式の ` | ` 区切りを採用） |

chunk 境界は段落（空行）優先、無ければ文字数で切る。将来、comments を同期した場合は issue の chunk 1.. として同じ仕組みに乗せられる。

## 9. embedding の更新判定と増分同期の統合

```
bkfz embed [--rebuild]                 初回構築・再構築・手動の差分更新（失敗は exit 1）
bkfz sync [--no-embed]
  ├─ issues    （従来の watermark 増分）
  ├─ documents （従来）
  └─ embed     （embedding 有効 かつ embeddings テーブルが既に存在する時のみ。失敗は警告で exit 0）

embed ステージ:
  1. embedding_meta と設定の model/dimensions を照合（不一致なら --rebuild を要求して停止）
  2. issues / documents から対象テキストを生成し sha256 を計算
  3. embeddings.content_hash と比較 → 未登録 or 変更分だけ列挙（chunk 数が変わった場合も対象）
  4. 32 件ずつ Embedder.Embed → 正規化 → Upsert（レコード単位で chunk 集合を置換。バッチごとにコミット）
  5. issues / documents に存在しない ref_id の embeddings を削除
  6. embedding_meta を記録（初回は応答ベクトルの長さを dimensions として保存）
```

- **初回は `sync` に載せない**。`embedding:` を書いた直後の `bkfz sync` は「`bkfz embed` を実行してください」と 1 行出すだけ。
- **watermark を増やさない**。hash 差分なので `--refetch`（全件再取得）しても本文が変わっていなければ再 embedding は起きない。
- 中断は安全（バッチ単位コミット）。`bkfz embed` で続きから再開。
- モデル変更時は `bkfz embed --rebuild`（全削除 → 全生成）。
- MVP では `-p PROJ` を付けない（`embeddings` に project_key が無く、孤児削除と衝突するため）。

## 10. MVP として実装する範囲

1. `embedding:` 設定と検証、未設定時の完全無効化
2. OpenAI 互換 embedding クライアント（バッチ、`dimensions`、リトライ、タイムアウト、localhost 以外への送信警告）
3. `embeddings` / `embedding_meta` スキーマ（遅延作成）、純 Go `VectorStore`（集約済み top-k）、chunker、hash
4. `bkfz embed`（`--rebuild`、進捗表示、再開可能）と `bkfz sync` 末尾の差分ステージ（索引構築済みの時のみ、`--no-embed`、失敗は警告）
5. `bkfz --semantic <query>` / `bkfz --list --semantic {q}`（スコア順、issue/doc 混在、既存の行フォーマットを流用、擬似行によるエラー表示、索引・meta の整合チェック）
6. TUI: ctrl-s / alt-s ワンショット ＋ prompt 切替、未設定時の案内、`error` 行の Enter 無視
7. README（JA/EN）: セットアップ（Ollama）、プライバシー注記、制限

## 11. 今は実装しない範囲

- `--hybrid`（RRF）と、逐次入力での意味検索モード（debounce 内蔵）→ Phase 2。S10 の評価セットで semantic / hybrid を比較してから
- クエリ embedding のキャッシュ（`query_cache`）→ Phase 2
- `bkfz embed -p PROJ`
- sqlite-vec / Vec1 backend → 5 万ベクトル超、または SQL 側フィルタが必要になった時
- bkfz へのモデル推論内蔵（ONNX / Model2Vec）
- comments の embedding（comments 自体が未同期）
- 添付ファイル本文
- `bkfz init` での embedding 対話設定（まずは YAML 手書き）
- 既存検索結果の並び順変更（`updated_at` マージ）には手を入れない

## 12. リスク・未決事項

| # | リスク / 未決 | 影響 | 対応案 |
|---|---|---|---|
| R1 | 日本語品質がモデル依存（embeddinggemma は実データで良好だったが評価は 6 クエリ） | 期待外れなら機能の価値が落ちる | `scripts/demo-seed` の架空データに小さな評価セット（クエリ → 期待キー）を同梱し、モデル切替時に比較できるようにする |
| R2 | Ollama の導入負荷（追加インストール＋モデル 600MB） | opt-in の敷居 | README に 3 行の手順。未起動時のエラーメッセージに導入手順を含める |
| R3 | 初回 embedding 30 分（ローカル） | 「初回 sync が重い」印象 | 初回は `sync` に載せず `bkfz embed` の明示実行のみ（§9）。バックグラウンド推奨と ETA 表示。文字数上限で調整可（`dimensions` は時間にはほぼ効かない） |
| R4 | モデル／次元の変更で既存ベクトルが無効 | 検索結果が壊れる | `embedding_meta` で検出し `--rebuild` を要求（embed ステージと検索の両方で照合。黙って混在させない） |
| R5 | ctrl-s が端末の XOFF（flow control）に取られる環境 | キーが効かない | fzf は raw モードで動作するため通常は問題ない。予備として alt-s も同じ動作に割り当てる（設定項目は増やさない） |
| R6 | 長大文書（最大 18 万字）の末尾が chunk 上限で落ちる | 稀に見つからない | 上限 8 は設定値にする。必要なら引き上げ |
| R7 | Backlog 側で削除された記録が残る（既存制限） | 古い結果が混じる | 既存制限と同じ扱い。rebuild 系で同期 |
| R8 | クラウド API 利用時の情報漏えい | 組織ポリシー違反 | 既定ローカル、localhost 以外は警告、README に送信内容を明記 |
| R9 | fzf の Windows での reload 子プロセス終了（0.71 で修正） | Phase 2 の逐次モードのみ | Phase 2 で最小バージョンを見直す |
| R10 | embeddinggemma のライセンス（Gemma Terms） | 再配布に制約 | bkfz はモデルを同梱・再配布しない（ユーザーが `ollama pull`）。README に明記 |
| Q1 | ctrl-s の既定はワンショット「semantic」か「hybrid」か | UX | MVP は semantic。S10 の評価セットで両者を比較して既定を決める（ワンショットなら hybrid の追加コストは FTS 1 回分で、コストは判断材料にならない） |
| Q4 | chunk 集約での品質（検証は chunk なし） | 品質 | S10 で測る。256 次元は目視再実行で劣化を確認し既定から外した（poc/README §4.1） |
| Q2 | issue の description 上限 1,200 字は妥当か | 品質 vs コスト | 設定値にし、既定は 1,200 |
| Q3 | 結果の表示に類似度を出すか | UX | 出さない（既存の行フォーマット維持）。`--semantic` の非対話出力にだけ `--score` オプションを検討 |

## 13. 判断の記録（根拠つき）

| # | 判断 | 根拠（確認した事実） |
|---|---|---|
| D1 | 既存 FTS を置き換えない。opt-in | 依頼の前提。未設定ユーザーへのコストを 0 にできる設計が成立する（§7） |
| D2 | ベクトルは純 Go 総当たり | 実測 35〜70ms（§6.1）。新規依存 0。sqlite-vec は modernc v1.60.1 同梱で後から 1 行で足せる |
| D3 | Vec1 不採用 | 未リリース（2026-04 フォーラム）。Go からは ncruces 経由のみでドライバ交換が必要。1〜2 万件では Vec1 自身が exact モードを推奨 |
| D4 | embedding は OpenAI 互換 HTTP 1 本。既定 Ollama | Ollama `/v1/embeddings` でバッチと `dimensions` の動作を確認。純 Go 推論の現実的な選択肢がない |
| D5 | MVP UX = `--semantic` ＋ ctrl-s ワンショット | fzf 0.40 の範囲で実装可。逐次 embedding を避けられる。fzf が旧 reload を terminate することを `src/core.go` で確認したので Phase 2 の debounce 方式も成立する |
| D6 | issue 1 ベクトル／doc chunk 最大 8 | 本文長の分布（§1.2, §8） |
| D7 | 既定はモデルのネイティブ次元（`dimensions` 省略）。256 は任意指定 | 256 次元への切り詰めで 6 クエリ中の正解ヒットが 5 → 4 に落ち、1 件が圏外になった（poc §4.1）。768 次元でも索引 52MB・総当たり 70ms で許容範囲 |
| D8 | hash 差分で更新判定 | watermark に影響させない。`--refetch` でも無駄な再生成が起きない |
| D9 | 抽象化は interface 1 つ | 差し替え先（sqlite-vec / Vec1）の API 形が総当たり・学習型と異なるため `Rebuild` を含めた最小 interface だけ先に切る |
| D10 | 初回構築は `bkfz embed` のみ。`sync` は差分更新だけで失敗は警告 | 「初回 sync が極端に重くならない」を満たすため。Ollama 停止中でも `sync` が失敗しない |
| D11 | fzf 内のエラーは擬似行で表示 | reload の stderr は画面を壊す（既存 `runAction` と同じ理由）。`transform` による分岐は fzf 0.45 以上を要求するため使わない |
| D12 | `query_cache` と `embed -p` は MVP から除外 | 読み取り専用だった `--list` に書き込みを持ち込まない。`-p` は孤児削除と衝突する |

## 14. レビュー反映の記録（2026-10-13）

別エージェントによるレビュー 23 件（blocking 2、significant 9、minor 12）への対応。

| 指摘 | 対応 |
|---|---|
| ctrl-s 時のエラーが fzf 内で見えない（blocking） | 擬似行方式を採用（§3.1、D11） |
| `sync` 末尾の初回 embed が R3 と矛盾（blocking） | 初回は `bkfz embed` のみ。`sync` は索引構築済み時の差分更新、失敗は警告（§9、D10） |
| 品質検証（768d・chunk なし）と推奨構成（256d・chunk）の不一致 | 256d で再実行したところ劣化が確認されたため、**既定を 768d（ネイティブ次元）に変更**（D7、poc/README §4.1）。chunk の影響は Q4 として S10 へ |
| `runList` のフラグ解釈、空クエリ時の挙動 | §3.1 に仕様化。§7 の表現を修正 |
| `change-header:` の順序 | §3.1 に「末尾固定」を明記 |
| 検索側の schema / meta 整合チェック、`dimensions` 省略時の記録 | §3.1、§4.3 |
| chunk 集約と `Search(k)` の意味 | §4.3、§4.4（集約後の top-k） |
| `query_cache` の増加・書き込み競合 | MVP から除外（§3.2-C、D12） |
| hybrid 見送りの根拠が薄い | 理由を「評価セット未整備」に書き換え、Q1 を S10 後の判断に変更 |
| `embed -p` と孤児削除の衝突 | MVP から除外（§9、§11） |
| `bkfz --semantic` 引数なしの挙動 | usage エラーに統一（§3 表、§3.1） |
| クエリ合計時間の数値不整合 | 70〜100ms に統一（§0、§7.3） |
| 「2.8 倍」 | 「約 3 倍（2.7〜3.5）」に修正 |
| スループット計測が `/api/embed` 経由 | poc/README §3 に注記 |
| プロンプト区切り（改行 vs ` \| `） | 設計は公式の ` \| `。PoC は改行だったと §8 に明記 |
| 文書長の統計を chunk 境界に合わせる | §1.2、§8 を 1,000 字境界の数値に差し替え |
| PoC が gofmt チェックの対象になる | poc/README に注記 |
| 進捗表示の文言 | `StageEmbedded` / `[embedded: N/total]`（§4.1、§7.2、S6） |
| `Upsert` の chunk 減少時 | 集合を丸ごと置換と明記（§4.4） |
| 設定キーの一覧が文書間で異なる | §4.2 を S2 と同じキー集合に統一。代替キーは alt-s を固定で追加 |
| Ollama 常駐メモリ | §7.3 に追記 |
| 評価軸 6 項目との対応表 | §0 に追加 |
| 「束縛」 | 「キーバインド」に置換 |

## 参考（一次情報）

- Vec1: https://sqlite.org/vec1/doc/trunk/doc/vec1.md / 紹介: https://sqlite.org/vec1/doc/trunk/doc/vec1intro.md / フォーラム（2026-02 発表, 2026-03〜04 更新）: https://sqlite.org/forum/info/ceba048877c35c8e5a27e507d900a8f8727c4e546ad7f4eb74b52cea42a36db7 , https://sqlite.org/forum/info/82f01e4bb640bab0b2f688270488f1203e4b54bdd6f8d47764345f2d24a37287
- ncruces/go-sqlite3 `ext/vec1`: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/vec1
- sqlite-vec: https://github.com/asg017/sqlite-vec / Go bindings: https://github.com/asg017/sqlite-vec-go-bindings / KNN: https://alexgarcia.xyz/sqlite-vec/features/knn.html
- modernc.org/sqlite/vec（同梱 v0.1.9、v1.60.1 以降）: https://pkg.go.dev/modernc.org/sqlite/vec
- fzf: `src/core.go`（EvtSearchNew で `reader.terminate()`）、ADVANCED.md（`sleep 0.1` による debounce、`transform` 0.45.0）、CHANGELOG（`change-header` / `transform-header` 0.40.0）
- Ollama API `/api/embed`（`input` 配列、`dimensions`）: https://github.com/ollama/ollama/blob/main/docs/api.md
- EmbeddingGemma: https://ai.google.dev/gemma/docs/embeddinggemma / Model2Vec: https://github.com/MinishLab/model2vec
- OpenAI embeddings 料金: https://developers.openai.com/api/docs/models/text-embedding-3-small / Voyage: https://docs.voyageai.com/docs/pricing

## 15. 実装時の設計からの差分（2026-10-13）

MVP（S2〜S9）を実装した際、設計から変えた点・補った点。

| 項目 | 設計 | 実装 | 理由 |
|---|---|---|---|
| `VectorStore` interface | interface を切る | `index.DB` のメソッド群（`UpsertEmbeddings` / `SearchVectors` / `EmbeddingHashes` / `DeleteOrphanEmbeddings` / `ClearEmbeddings` / meta）として実装し、interface 型は定義していない | 実装が 1 つの段階で interface を置くと空の抽象になる。差し替え時（S13）にメソッド集合をそのまま interface 化すればよい |
| 既定次元 | 256 → 768（レビュー後） | `dimensions` 省略 = モデルのネイティブ次元。meta には応答ベクトル長を記録 | poc §4.1 |
| エラー表示 | 擬似行 | 擬似行 `error\t-\t-\t-\t✗ <msg>\t-`。端末向けの複数行ヒント（`ollama serve` 等）は `hintedError` に分離し、擬似行では `(see README)` に置き換える | 一覧の 1 行に複数行ヒントを畳むと読めない |
| chunk の境界 | 段落優先 | 段落（空行）を詰めて chunk を作り、1 段落が `chunk_chars` を超える場合のみ overlap 100 字の固定長窓で切る | 実装の単純さ。空本文の文書はタイトルだけの 1 chunk を持つ（タイトルで探せるように） |
| 空クエリ | 最近更新順 | `--list --semantic ""` は embedding を呼ばず FTS の空クエリと同じ最近更新順を返す | ctrl-s を空のまま押した時に HTTP を飛ばさない |
| `sync` の embed ステージ | 索引構築済みの時のみ | `embeddings` テーブルの有無で判定。未構築なら `run bkfz embed` の 1 行通知のみ。失敗は警告で exit 0（`context.Canceled` は除く） | 設計通り。Ctrl-C は従来通り 130 で終了させる |
| 進捗表示 | `StageEmbedded` | `syncer.StageEmbedded` を追加し、`progressPrinter` が `[embedded: N/total ETA]` / `embeddings ok (N embedded)` と表示 | |
| 非ローカル送信の警告 | `bkfz embed` 時 | `embed` と `sync` の embed ステージの両方で、実行前に `note: sending issue and document text to <url>` を表示 | |

検証: 単体テスト（`go test -race ./...`）に加え、隔離した XDG ディレクトリにコピーした実 index（300 issues ＋ 40 documents → 465 chunks）で `bkfz embed`（13 秒）→ 再実行で 0 件 → `--semantic` / `--list --semantic` / 空クエリ / サーバ停止時の擬似行 / モデル変更時の `--rebuild` 案内 / 非ローカル URL の警告を手動確認した。

### 15.1 実装コードのレビュー（2026-10-13、`/code-review high --fix`）

10 件の指摘のうち 9 件を反映、1 件を見送り。

| 指摘 | 対応 |
|---|---|
| サーバが `dimensions` を無視した場合に `--rebuild` を案内し続ける | `syncer.ErrDimensionsIgnored` を追加し、「`dimensions` を 0 にしてから `--rebuild`」と案内 |
| `sync` の embed ステージ失敗時の案内が一律 | mismatch / dimensions 無視 / その他で案内を分岐。`HasEmbeddingSchema` の失敗も警告のみに |
| embed の ETA 開始時刻が 1 バッチ遅い | `StageTotal` で時計を開始 |
| ctrl-s でサーバ停止時にリトライで約 6 秒待つ | `Client.Retries < 0` を「リトライしない」と定義し、クエリ経路で使用（実測 約 1 秒で擬似行表示） |
| model / dimensions の互換判定が 2 か所に重複 | `index.EmbeddingMeta.Compatible()` に集約 |
| `recentLines` が `search()` と重複 | `searchLines()` に統合 |
| `progressPrinter` のデッドコード | 削除 |
| エラー本文の切り詰めがルーン境界を壊す | `[]rune` で切り詰め |
| hash に `dimensions` を含めると「0 → ネイティブ値の明示」で全件再埋め込みになる | hash を `sha256(model \| text)` に変更。次元は `embedding_meta` で照合（§4.3 を更新） |
| `DocumentsByIDs` と `scanDocuments` の重複 | **見送り**。`scanDocuments` が rowid を返さず、既存検索パスの改修が必要なため S13 以降で扱う |
