# 意味検索 Issue 分割案

各 Issue は 1 PR で閉じられる粒度を狙う。依存順に並べている。番号は仮。

> 2026-10-13: S1〜S9 を実装済み（レビュー反映後。差分は README.md §15）。S10 も実装済み（README.md §15.2）。Phase 2 は未着手。

## Phase 1（MVP）

### S1. docs: 意味検索の設計ドキュメントと ADR を追加
- 内容: `docs/design/semantic-search/` 一式（本調査）。
- 受け入れ条件: レビューで ADR-0001 の方針に合意。
- 依存: なし。

### S2. feat(config): `embedding` セクションを追加
- 内容: `Config.Embedding *EmbeddingConfig`（`base_url`, `model`, `dimensions`, `api_key_env`, `query_prefix`, `document_prefix`, `max_issue_chars`, `chunk_chars`, `max_chunks`）。未設定（nil）= 無効。URL / 必須項目の検証。`IsLocal()`（localhost 判定）。
- 受け入れ条件: 既存 config のパースに影響なし（テスト追加）。無効値はわかりやすいエラー。
- 依存: S1。

### S3. feat(embed): OpenAI 互換 embedding クライアント
- 内容: `internal/embed` パッケージ。`Embedder` interface と `OpenAICompatible` 実装（`POST {base_url}/embeddings`、`input []string`、`dimensions` 任意、`Authorization: Bearer` 任意）。バッチ分割（既定 32）、タイムアウト、429/5xx リトライ、`context` 対応。
- 受け入れ条件: `httptest` による単体テスト（バッチ分割、リトライ、エラー整形、次元不一致検出）。実 Ollama なしで CI が通る。
- 依存: S2。

### S4. feat(index): embeddings スキーマと純 Go VectorStore
- 内容: `embeddings` / `embedding_meta` テーブル（`index.DB.EnsureEmbeddingSchema()` で遅延作成）。`VectorStore` interface と BLOB 実装（Upsert / Delete / Search(top-k, cosine) / Hashes / Rebuild）。`Upsert` は (kind, ref_id) の chunk 集合を丸ごと置換（余った chunk 番号は削除）。`Search` は全件走査中に (kind, ref_id) ごとの最大スコアを保持し、集約後の top-k を返す（`Hit.Chunk` は最大スコアの chunk 番号）。`embedding_meta` の get / set。
- 受け入れ条件: `:memory:` でのテスト。既存 `schemaSQL` は不変。embedding 無効時にテーブルが作られないことをテスト。同一 doc の複数 chunk が上位でも 1 行に畳まれ、k 件返ることをテスト。
- 依存: なし（S3 と並行可）。

### S5. feat(embed): 埋め込み対象テキスト生成・chunker・hash
- 内容: issue → `{document_prefix}title: {summary} | text: {description 先頭 max_issue_chars}` 1 本。document → 段落優先の `chunk_chars` 字 chunk（overlap 100、上限 `max_chunks`）、各 chunk に title を前置。前処理は空白・空行の正規化のみ。`sha256(model | text)`（prefix は text に含まれる。dimensions は meta で照合するため含めない）。
- 受け入れ条件: 境界（空本文、1 文字、超長文、改行のみ、上限超過）のテーブルテスト。
- 依存: なし。

### S6. feat(sync): `bkfz embed` と `sync` 末尾の差分ステージ
- 内容: `syncer.Embed(ctx, embedder, db, opts)`: meta 照合（不一致なら `ErrEmbeddingMismatch`、`--rebuild` を案内）、差分列挙（chunk 数の変化も対象）、バッチ embed → 正規化 → Upsert（バッチごとにコミット）、孤児削除、meta 記録（`dimensions` 省略時は応答ベクトル長）。`ProgressStage` に `StageEmbedded` を追加し、`progressPrinter` は `[embedded: N/total ETA]` と表示。`bkfz embed [--rebuild]`（失敗は exit 1）。`bkfz sync [--no-embed]` は **`embeddings` テーブルが存在する時だけ**差分ステージを実行し、失敗は警告で exit 0。テーブル不在なら「run `bkfz embed`」を 1 行表示。localhost 以外への送信警告。`-p PROJ` は付けない。
- 受け入れ条件: fake Embedder での統合テスト（差分のみ呼ばれる、途中失敗後の再開で残りだけ処理される、rebuild、meta 不一致）。中断時に watermark が影響を受けない。
- 依存: S2, S3, S4, S5。

### S7. feat(search): `bkfz --semantic <query>` / `--list --semantic {q}`
- 内容: `runList` の先頭で `--semantic` を剥がす（無ければ従来通り）。クエリ embedding（`query_prefix` 付与）→ `VectorStore.Search` → issues / documents を id で引く → **スコア順**に既存の行フォーマットで出力。空クエリは embedding せず最近更新順（FTS の空クエリと同じ）。検索前の整合チェック: 設定なし → 設定手順、`embeddings` 不在 → 「run `bkfz embed`」、meta の model/dimensions 不一致または応答ベクトル長の不一致 → 「run `bkfz embed --rebuild`」。`--list --semantic` は常に exit 0 で、失敗時は擬似行 `error\t-\t-\t-\t✗ <message>\t-` を stdout に出す。非対話の `bkfz --semantic` は stderr にエラーで exit 1、引数なしは usage エラー。
- 受け入れ条件: fake Embedder + `:memory:` でのテスト。issue と doc が同一スコア空間で混在する。擬似行のフォーマットと exit 0 をテスト。
- 依存: S4, S6。

### S8. feat(tui): ctrl-s ワンショット意味検索
- 内容: `RunOptions.SemanticCmd`。`ctrl-s` と `alt-s` の両方に `reload(... --list --semantic {q})+change-prompt(semantic> )+change-header:<key help>`。`change:` 側は `reload(...)+change-prompt(> )+change-header:<key help>` の順（**`change-header:` は末尾固定**）。無効時は `ctrl-s` / `alt-s` に `change-header(semantic search is not configured; see README)` のみ。ヘッダの key help に `ctrl-s: semantic` を追加。`runTUI` は kind `error` の行で Enter が押されても何もしない。`bkfz preview error -` は案内文を返す。
- 受け入れ条件: `buildFzfArgs` のテスト更新（順序を固定）。fzf 最小バージョン 0.40 据え置き。Windows の引用規則（既存 `shellQuote`）を踏襲。
- 依存: S7。

### S9. docs(readme): 意味検索のセットアップとプライバシー注記（JA/EN）
- 内容: Ollama の導入 3 行、config 例、`bkfz embed` の所要時間目安、送信されるデータの説明、クラウド API 利用時の注意、既知の制限（chunk 上限、comments 未対応）。
- 依存: S8。

### S10. test(eval): 架空データでの小さな評価セット
- 内容: 架空データを `scripts/demodata` に切り出し、`scripts/eval`（`make eval`）に「言い換えクエリ → 期待キー」22 件と Recall@1 / Recall@5 / MRR の計算を実装。`-min-recall5` で退行ゲート。計算ロジックは単体テスト、実行には embedding サーバが必要。
- 目的: モデル・次元・chunk 設定の変更時に退行を検出する。
- 依存: S7。

## Phase 2（MVP の利用状況を見て判断）

### S11. feat(search): `--hybrid`（RRF）
- FTS top-100 と vector top-100 を Reciprocal Rank Fusion で融合（パラメータは S10 の評価セットで決める）。S10 で semantic / hybrid を比較し、ctrl-s の既定を決める。

### S12. feat(tui): 逐次意味検索モード（`bkfz --semantic-tui` などの起動オプション）
- `--list --semantic --delay 300ms {q}` の debounce を bkfz 内に実装（fzf が旧 reload を terminate する前提）。Windows は fzf 0.71 以上を要求。

### S12b. feat(search): クエリ embedding のキャッシュ
- `sha256(model|dimensions|prefix|text) → vec`、上限（例 500 行、古い順に削除）、best-effort 書き込み、`--rebuild` で全削除、`busy_timeout` の設定。

### S13. feat(index): sqlite-vec backend
- `modernc.org/sqlite` を v1.60.1 以上に更新し `modernc.org/sqlite/vec` を import。`VectorStore` の `vec0` 実装。5 万ベクトル超または metadata フィルタが必要になった時。

### S14. feat(index): int8 量子化
- index サイズ 1/4、検索 1.5 倍速。品質影響を S10 で確認してから。

### S15. feat(sync): comments の同期と embedding（Roadmap の comments search と合流）

### S16. chore: Vec1 の再評価
- Vec1 正式リリース後。ncruces/go-sqlite3 への移行コストと合わせて判断。
