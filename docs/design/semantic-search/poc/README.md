# PoC / ベンチマーク結果

調査（2026-10-13）で使った 2 つの小さな PoC と、その結果。
このディレクトリは独立した Go モジュール（`go.mod` あり）なので、リポジトリ本体の `go build ./...` / golangci-lint / `go mod tidy` には影響しない。ただし CI の `gofmt -l .` はモジュール境界を無視して走査するため、このディレクトリも gofmt チェックの対象になる（現状クリーン）。

- `vecbench/`: 純 Go 総当たり（float32 / int8）と sqlite-vec `vec0`（`modernc.org/sqlite/vec`）の検索時間・DB サイズ比較
- `embbench/`: ローカル Ollama で実 index を埋め込み、スループットと言い換えクエリの品質を見る

## 環境

| 項目 | 値 |
|---|---|
| マシン | Apple M2 Max, 34GB |
| Go | 1.26.2 **darwin/amd64（Rosetta）**。ネイティブ arm64 ではより速いはず |
| modernc.org/sqlite | v1.60.1（`vec` 同梱 v0.1.9。bkfz 本体は v1.50.0） |
| Ollama | ローカル、`embeddinggemma`（300M, 768 次元, Matryoshka） |
| fzf | 0.72.0 |

## 1. バイナリサイズとクロスビルド（modernc.org/sqlite/vec を import した場合）

| | サイズ（`-s -w -trimpath`, CGO_ENABLED=0） |
|---|---|
| `modernc.org/sqlite` のみ | 6,521,552 B |
| ＋ `modernc.org/sqlite/vec` | 6,825,248 B（**+0.3MB**） |

クロスビルド: linux/amd64, linux/arm64, darwin/arm64, windows/amd64 すべて成功。
`vec` パッケージは **v1.60.1 で追加**（v1.51.0〜v1.60.0 には存在しない）。

## 2. 検索時間（新規プロセスが DB を開いて top-50 を返すまで）

`vecbench -mode gen` でランダム単位ベクトルを `emb(vec BLOB, vec8 BLOB)` と `vec0` 仮想テーブルの両方に格納し、各 mode を 3 回ずつ別プロセスで実行（中央値）。

| 件数 × 次元 | go float32 | go int8 | vec0 (cosine) | DB サイズ（二重格納） |
|---|---|---|---|---|
| 15,000 × 768 | 70ms（scan+dot 65） | 45ms | 25ms | 109MB |
| 15,000 × 256 | 35ms（scan+dot 30） | 23ms | 10ms | 37MB |
| 50,000 × 768 | 211ms | 136ms | 78ms | 361MB |
| 100,000 × 256 | 192ms | 134ms | 58ms | 242MB |

- 3 方式とも top-1 が一致（正しさの確認）。
- 純 Go の時間はほぼ「BLOB の読み出し＋内積」。sort は 2〜25ms。
- 実運用規模（約 17,000 × 768）では純 Go で 70〜80ms 程度、256 次元なら 35〜40ms 程度と見込める。

再現:

```sh
cd docs/design/semantic-search/poc
go build -o vecbench ./vecbench
./vecbench -mode gen -n 15000 -d 256 -db b.db
./vecbench -mode go   -d 256 -db b.db
./vecbench -mode int8 -d 256 -db b.db
./vecbench -mode vec0 -d 256 -db b.db
```

## 3. embedding 生成（Ollama embeddinggemma）

`embbench -mode index` で、ローカル index.db のコピーから直近の issues / documents を取り出し、`title: …\ntext: …` 形式（改行区切り。本設計が採用する公式の ` | ` 区切りとは異なる）で Ollama ネイティブの `/api/embed` にバッチ投入。設計で採用する `/v1/embeddings`（OpenAI 互換）は動作確認のみで、スループットは未計測（同一エンジンなので差は小さい見込み）。

| 指標 | 値 |
|---|---|
| スループット | **9.7 件/s**（batch 32、本文先頭 1,500 字、1 件平均 約 390 トークン、約 3,800 tok/s） |
| 256 件 | 26 秒 |
| 2,500 issues + 300 documents（本文先頭 1,200 字） | 約 4.8 分 |
| クエリ 1 件（ウォーム状態） | 27ms（curl 実測）〜65ms（Go から、正規化含む） |
| `dimensions: 256` | 動作（768 → 256）。`/v1/embeddings`（OpenAI 互換）でもバッチと `dimensions` が動作 |

この端末の実 index（10,559 issues ＋ documents を 1,000 字 chunk・上限 8 で 6,473 本）に換算すると約 17,000 件 → **約 30 分**（初回のみ）。

## 4. 品質の目視確認（実データ、ローカル完結）

直近 2,500 issues ＋ 300 documents（**768 次元、本文先頭 1,200 字、chunk なし**）に対し、件名の語を含まない言い換えクエリを 6 件投げ、現行 `bkfz <query>`（FTS）と比較した。社内データのため具体的な文字列は記載しない。推奨構成（256 次元・chunk 集約）での品質は未測定で、256 次元の再実行結果は §4.1 に示す。

| クエリの種類 | FTS ヒット | 意味検索（top-5） |
|---|---|---|
| 障害の症状を言い換え（「〜後に〜できなくなった」） | 0 | 意図した課題が 2 位。1 位・3〜5 位も同じ移行作業の関連課題 |
| 性能問題の言い換え（「遅いので速くしたい」） | 0 | 「速度チューニング」「パフォーマンス改善」系が上位 5 件を占める |
| ツール固有名を一般語で表現（固有名は綴りゆれあり） | 0 | 意図した課題が 1 位 |
| 環境構築を別の言葉で（「仮想マシンにする」） | 0 | 意図した課題が 1 位。2〜4 位に関連する手順書（document）が並ぶ |
| 制度変更対応の言い換え | 0 | 同カテゴリの課題群が上位（意図したものと同種） |
| 定型課題シリーズを一般語で | 0 | 同シリーズが上位 5 件を独占 |

所感:
- 「キーワードを思い出せない」ケースで明確に効く。issue と document が同じスコア空間で自然に混ざる。
- 類似度は 0.43〜0.58 程度に分布。しきい値での足切りは不要（上位 k 件で十分）。
- 評価は 6 クエリの目視であり、定量評価は Issue S10（架空データの評価セット）で行う。

### 4.1 256 次元（Matryoshka 切り詰め）での再実行

同じ 2,800 件を `dimensions: 256` で埋め込み直し、同じ 6 クエリを投げた（レビュー指摘への対応）。

| クエリの種類 | 768 次元 | 256 次元 |
|---|---|---|
| 障害の症状を言い換え | 意図した課題が 2 位 | **3 位**に後退（1〜2 位は同じ移行作業の別課題） |
| 性能問題の言い換え | 上位 5 件すべて性能系 | 上位 5 件中 4 件が性能系 |
| ツール固有名を一般語で | 意図した課題が 1 位 | **圏外**（top-5 はテスト関連の別課題） |
| 環境構築を別の言葉で | 1 位、2〜4 位に手順書 | 1 位、3〜4 位に手順書（同等） |
| 制度変更対応の言い換え | 同カテゴリ群 | 同カテゴリ群（5 件中 3 件。やや薄い） |
| 定型課題シリーズ | 上位独占 | 上位独占（同等） |

- 意図した課題が top-5 に入った数: 768 次元 5/6 → 256 次元 **4/6**。固有名詞の言い換えのような難しいクエリで落ちる。
- クエリ embedding は 35〜66ms で 768 次元（48〜65ms）と大差なし。総当たりは 2,800 件では差が見えない。
- **判断**: 既定は切り詰めない（モデルのネイティブ次元 = embeddinggemma なら 768）。256 次元は索引サイズ（52MB → 17MB）を優先したい場合の選択肢として README に記載する。
- Ollama の常駐メモリ（`ollama ps`）: embeddinggemma で **673MB**（100% GPU、`keep_alive` 既定 5 分）。

再現（`BACKLOG` の実 index を使う。外部送信なし）:

```sh
cd docs/design/semantic-search/poc
ollama pull embeddinggemma
go build -o embbench ./embbench
./embbench -mode index -src ~/.local/share/bkfz/index.db -limit 2500 -docs 300
./embbench -mode query "言い換えたクエリ"
```

## 5. fzf の reload 挙動（一次ソース確認）

- `src/core.go` の `EvtSearchNew`: 読み込み中（`reading`）に新しい command が来ると `reader.terminate()` を呼び、`EvtReadFin` 後に次の command を `restart` する。
  → bkfz 内で sleep してから embedding を呼ぶ debounce が成立する（Phase 2 の逐次モード用）。
- `ADVANCED.md`: `sleep 0.1` を reload コマンド先頭に置く debounce を公式に案内。
- `CHANGELOG`: `change-header` / `transform-header` は 0.40.0、`transform` は 0.45.0、Windows の子プロセス終了修正は 0.71.0。
