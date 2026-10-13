// embbench: embeds real local issues/documents via Ollama /api/embed and
// stores vectors in a side DB; "query" mode embeds a query and prints the
// top-k by cosine (brute force in Go). Measures throughput and latency.
package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type embedReq struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Truncate   bool     `json:"truncate"`
	Dimensions int      `json:"dimensions,omitempty"`
	KeepAlive  string   `json:"keep_alive,omitempty"`
}
type embedResp struct {
	Embeddings      [][]float32 `json:"embeddings"`
	TotalDuration   int64       `json:"total_duration"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

func embed(model string, texts []string, dims int) ([][]float32, int, error) {
	body, _ := json.Marshal(embedReq{Model: model, Input: texts, Truncate: true, Dimensions: dims, KeepAlive: "10m"})
	resp, err := http.Post("http://localhost:11434/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var er embedResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, 0, err
	}
	if len(er.Embeddings) != len(texts) {
		return nil, 0, fmt.Errorf("got %d embeddings for %d inputs (status %s)", len(er.Embeddings), len(texts), resp.Status)
	}
	return er.Embeddings, er.PromptEvalCount, nil
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func f32blob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	if s == 0 {
		return
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
}

func main() {
	mode := flag.String("mode", "index", "index | query")
	srcPath := flag.String("src", "index_copy.db", "path to a bkfz index.db (read only)")
	model := flag.String("model", "embeddinggemma", "ollama model")
	dims := flag.Int("dims", 0, "requested dims (0 = model default)")
	limit := flag.Int("limit", 200, "issues to embed")
	docLimit := flag.Int("docs", 0, "documents to embed")
	batch := flag.Int("batch", 32, "batch size")
	maxChars := flag.Int("maxchars", 1500, "max runes of description")
	k := flag.Int("k", 10, "top-k")
	flag.Parse()
	src, err := sql.Open("sqlite", *srcPath)
	must(err)
	defer src.Close()
	out, err := sql.Open("sqlite", "emb.db")
	must(err)
	defer out.Close()
	_, err = out.Exec(`CREATE TABLE IF NOT EXISTS emb(kind TEXT, key TEXT, title TEXT, model TEXT, vec BLOB, PRIMARY KEY(kind,key,model))`)
	must(err)

	switch *mode {
	case "index":
		type item struct{ kind, key, title, text string }
		var items []item
		rows, err := src.Query(`SELECT issue_key, summary, IFNULL(description,'') FROM issues ORDER BY updated_at DESC LIMIT ?`, *limit)
		must(err)
		for rows.Next() {
			var key, sum, desc string
			must(rows.Scan(&key, &sum, &desc))
			items = append(items, item{"issue", key, sum, "title: " + sum + "\ntext: " + truncRunes(desc, *maxChars)})
		}
		rows.Close()
		if *docLimit > 0 {
			rows, err = src.Query(`SELECT backlog_id, title, IFNULL(body,'') FROM documents ORDER BY updated_at DESC LIMIT ?`, *docLimit)
			must(err)
			for rows.Next() {
				var key, title, body string
				must(rows.Scan(&key, &title, &body))
				items = append(items, item{"doc", key, title, "title: " + title + "\ntext: " + truncRunes(body, *maxChars)})
			}
			rows.Close()
		}
		start := time.Now()
		var tokens int
		for i := 0; i < len(items); i += *batch {
			j := min(i+*batch, len(items))
			texts := make([]string, 0, j-i)
			for _, it := range items[i:j] {
				texts = append(texts, it.text)
			}
			vecs, n, err := embed(*model, texts, *dims)
			must(err)
			tokens += n
			tx, err := out.Begin()
			must(err)
			for x, v := range vecs {
				normalize(v)
				_, err = tx.Exec(`INSERT OR REPLACE INTO emb(kind,key,title,model,vec) VALUES(?,?,?,?,?)`, items[i+x].kind, items[i+x].key, items[i+x].title, *model, f32blob(v))
				must(err)
			}
			must(tx.Commit())
			if (i / *batch)%10 == 0 {
				el := time.Since(start)
				fmt.Fprintf(os.Stderr, "\r%d/%d  %.1f items/s", j, len(items), float64(j)/el.Seconds())
			}
		}
		el := time.Since(start)
		var dim int
		_ = out.QueryRow(`SELECT length(vec)/4 FROM emb LIMIT 1`).Scan(&dim)
		fmt.Printf("\nindexed %d items in %s (%.1f items/s, %d tokens, %.0f tok/s, dims=%d)\n", len(items), el.Round(time.Second), float64(len(items))/el.Seconds(), tokens, float64(tokens)/el.Seconds(), dim)
	case "query":
		q := strings.Join(flag.Args(), " ")
		t0 := time.Now()
		qv, _, err := embed(*model, []string{"task: search result | query: " + q}, *dims)
		must(err)
		normalize(qv[0])
		tEmbed := time.Since(t0)
		rows, err := out.Query(`SELECT kind, key, title, vec FROM emb WHERE model = ?`, *model)
		must(err)
		type hit struct {
			kind, key, title string
			score            float32
		}
		var hits []hit
		for rows.Next() {
			var kind, key, title string
			var b []byte
			must(rows.Scan(&kind, &key, &title, &b))
			var s float32
			for i := range qv[0] {
				s += qv[0][i] * math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
			}
			hits = append(hits, hit{kind, key, title, s})
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		fmt.Printf("query=%q embed=%s total=%s n=%d\n", q, tEmbed.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond), len(hits))
		for _, h := range hits[:min(*k, len(hits))] {
			fmt.Printf("  %.3f %-5s %-12s %s\n", h.score, h.kind, h.key, truncRunes(h.title, 60))
		}
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
