# ADR-0001: 意味検索（semantic search）を opt-in の追加機能として導入する

- 日付: 2026-10-13
- ステータス: 提案（Proposed）
- 関連: [調査レポート](./README.md), [Issue 分割案](./issues.md)

## 文脈

backlog-fzf は Backlog の課題・文書をローカル SQLite（FTS5 + bigram）にミラーし、fzf で逐次検索する CLI である。pure-Go・CGO なし・単一バイナリ・ローカル完結を特徴とする。

現行の検索はクエリの各語が部分文字列として存在することを要求するため、「過去に似た課題があった気がするが件名やキーワードを思い出せない」「言い換え・表記ゆれ・ツール名を忘れた」といったケースでは 0 件になる。実データでの検証では、言い換えクエリ 6 件すべてで FTS は 0 件、埋め込みベースの検索は 5 件で意図した課題を 1〜2 位に返した。

## 決定

1. **既存の FTS5 検索はそのまま維持し、意味検索を opt-in の追加機能として導入する。** config の `embedding:` セクションが無ければ、バイナリ・DB・検索経路・TUI のいずれも従来と同一に動作する。
2. **UX（MVP）**: `bkfz --semantic <query>`（非対話、引数なしは usage エラー）と、TUI の **ワンショットキー（ctrl-s、予備に alt-s）** で現在のクエリを意味検索し直す方式を採用する。逐次入力中は embedding を生成しない。fzf 内でのエラーは stdout の擬似行（kind `error`）で表示し、`--list --semantic` は常に exit 0 とする。Hybrid（RRF）と逐次意味検索モードは Phase 2 とする。
3. **ベクトル格納・検索**: 通常テーブルの BLOB（float32、L2 正規化済み）を Go で総当たり（内積）し、(kind, ref_id) ごとの最大スコアに集約した上位 k を返す。SQLite 拡張は使わない。`VectorStore` interface（Upsert / Delete / Search / Hashes / Rebuild）を切り、将来 sqlite-vec（`modernc.org/sqlite/vec`、v1.60.1 以降に同梱）や Vec1 へ差し替え可能にする。クエリ embedding のキャッシュは MVP に含めない（読み取り専用の `--list` に書き込みを持ち込まない）。
4. **embedding 生成**: OpenAI 互換 HTTP（`/v1/embeddings`）クライアント 1 本のみ実装する。既定の接続先はローカル Ollama（`http://localhost:11434/v1`、モデル `embeddinggemma`）。次元は既定で切り詰めない（ネイティブ 768）。256 次元への切り詰めは目視評価で劣化したため、索引サイズを優先する場合の任意設定とする。クラウド API は明示設定が必要で、localhost 以外へ送信する際は警告を表示する。bkfz にモデル推論は内蔵しない。
5. **対象テキスト**: issue は `summary` + `description` 先頭 1,200 字で 1 ベクトル。document は `title` + 本文 1,000 字 chunk（overlap 100 字、最大 8 chunk）で、スコアは chunk の最大値。属性（status / assignee / 日付）は含めない。
6. **更新判定**: 埋め込み対象テキストの sha256 を `embeddings.content_hash` に保存し、差分のみ再生成する。**初回構築は `bkfz embed` の明示実行のみ**。`bkfz sync` は索引構築済みの場合に限り末尾で差分更新し、embedding サーバの失敗は警告のみで exit 0 とする（issues / documents の同期結果は保持）。`bkfz embed --rebuild` で全再生成。`embedding_meta` の model / dimensions は embed ステージと検索の両方で照合し、不一致は `--rebuild` を促すエラーにする。既存の watermark 機構には触れない。

## 検討した代替案

| 代替案 | 却下理由 |
|---|---|
| 既存検索を意味検索に置き換える | 依頼の前提で不採用。FTS の即応性・確実性（キー・固有名詞）は意味検索で代替できない |
| Vec1（sqlite.org 公式）を採用 | 2026-04 時点で未リリース（API は "proposed"）。Go からは ncruces/go-sqlite3 経由のみでドライバ総入れ替えが必要。C 拡張としての配布は CGO 化・CPU 別ビルドを招く。1〜2 万件規模では Vec1 自身が exact モードを推奨し ANN の利点がない |
| sqlite-vec（modernc 同梱）を最初から採用 | 技術的には 1 行で可能（+0.3MB、クロスビルド確認済み）だが、pre-v1 で破壊的変更の前例があり、shadow table でスキーマが複雑化する。実測で純 Go 総当たりが 35〜70ms に収まるため、現時点では不要 |
| 逐次入力で毎回 embedding（debounce 内蔵） | fzf が旧 reload を terminate する挙動は確認済みで成立するが、MVP では FTS の即応性を一切損なわない方式を優先。Phase 2 で検討 |
| 最初から Hybrid（RRF） | ワンショット前提なら追加コストは FTS 1 回分で小さい。見送る理由は、semantic 単独との優劣を測る評価セット（S10）が無く、融合パラメータを根拠なく決めることになる点。S10 の後で ctrl-s の既定を決める |
| 初回 embedding を `bkfz sync` に載せる | `embedding:` を書いた直後の `sync` に約 30 分が乗り「初回 sync が極端に重くならない」という評価軸に反する。Ollama 停止中に `sync` 全体が失敗する問題もある |
| bkfz にモデル推論を内蔵（ONNX Runtime / gomlx / Model2Vec） | ONNX は共有ライブラリまたは CGO が必要。純 Go 推論は依存が巨大で遅い。Model2Vec（静的埋め込み）は純 Go で実装可能だが Go 実装が存在せず tokenizer 移植が必要で、品質も Transformer 系に劣る。将来案として記録 |
| クラウド embedding API を既定にする | 社内情報の外部送信を既定にできない |

## 結果

- 良い点: 未設定ユーザーへの影響ゼロ。新規 Go 依存ゼロ。配布構成（goreleaser、Homebrew cask）に変更なし。意味検索は「FTS で出なかった時に ctrl-s」という 1 動作で使える。
- 受け入れるコスト: opt-in ユーザーは Ollama（または他の embedding サーバ）の導入と、初回 embedding（この端末の実 index 約 17,000 ベクトルで約 30 分）を払う。index.db は約 52MB 増える（768 次元。256 次元なら 17MB）。
- 将来の差し替え: `VectorStore` の実装追加で sqlite-vec / Vec1 に移行可能。`Embedder` の実装追加で Ollama ネイティブ API や Voyage 等に対応可能。

## 検証に用いた事実

- 純 Go 総当たり vs sqlite-vec `vec0` のベンチ（新規プロセス、top-50）: 15,000 × 256d で 35ms / 10ms、15,000 × 768d で 70ms / 25ms、100,000 × 256d で 192ms / 58ms。
- Ollama embeddinggemma（Apple M2 Max）: 索引 9.7 件/s（`/api/embed` 経由、1 件平均 390 トークン、batch 32）、クエリ 27〜65ms。`dimensions: 256` と `/v1/embeddings` のバッチ入力の動作を確認。品質の目視確認（6 クエリ）は 768 次元・chunk なしで実施。256 次元では正解ヒットが 5/6 → 4/6 に劣化。chunk 集約の品質は未測定（S10 で検証）。
- fzf `src/core.go`: `EvtSearchNew` で reading 中なら `reader.terminate()` してから次の command を restart する。
- `modernc.org/sqlite/vec` は v1.60.1 以降に存在し、v1.60.0 以前には無い。バイナリ増分 0.3MB、linux/darwin/windows × amd64/arm64 のクロスビルド成功。
