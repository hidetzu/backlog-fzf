// vecbench: compares (a) pure-Go brute-force cosine top-k over float32 BLOBs
// stored in a plain SQLite table, (b) sqlite-vec vec0 KNN (bundled in
// modernc.org/sqlite/vec), and (c) pure-Go brute force over int8-quantized
// vectors. Each "query" run opens the DB fresh to mimic `bkfz --list {q}`
// being spawned per keystroke.
package main

import (
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func main() {
	mode := flag.String("mode", "gen", "gen | go | vec0 | int8")
	n := flag.Int("n", 10000, "vectors")
	d := flag.Int("d", 256, "dims")
	k := flag.Int("k", 50, "top-k")
	path := flag.String("db", "bench.db", "db path")
	flag.Parse()
	switch *mode {
	case "gen":
		gen(*path, *n, *d)
	case "go":
		queryGo(*path, *d, *k)
	case "int8":
		queryInt8(*path, *d, *k)
	case "vec0":
		queryVec0(*path, *d, *k)
	}
}

func randUnit(r *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	var s float64
	for i := range v {
		x := r.NormFloat64()
		v[i] = float32(x)
		s += x * x
	}
	s = math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
	return v
}

func f32blob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func i8blob(v []float32) []byte {
	b := make([]byte, len(v))
	for i, x := range v {
		b[i] = byte(int8(math.Round(float64(x) * 127 / 0.3))) // crude scale for unit vectors
	}
	return b
}

func gen(path string, n, d int) {
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	must(err)
	defer db.Close()
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE emb(id INTEGER PRIMARY KEY, vec BLOB NOT NULL, vec8 BLOB NOT NULL);
CREATE VIRTUAL TABLE vemb USING vec0(embedding float[` + fmt.Sprint(d) + `] distance_metric=cosine);`)
	must(err)
	r := rand.New(rand.NewSource(1))
	tx, err := db.Begin()
	must(err)
	st, err := tx.Prepare(`INSERT INTO emb(id, vec, vec8) VALUES(?,?,?)`)
	must(err)
	sv, err := tx.Prepare(`INSERT INTO vemb(rowid, embedding) VALUES(?,?)`)
	must(err)
	start := time.Now()
	for i := 1; i <= n; i++ {
		v := randUnit(r, d)
		b := f32blob(v)
		_, err = st.Exec(i, b, i8blob(v))
		must(err)
		_, err = sv.Exec(i, b)
		must(err)
	}
	must(tx.Commit())
	fi, _ := os.Stat(path)
	fmt.Printf("gen n=%d d=%d in %s, db=%.1fMB\n", n, d, time.Since(start).Round(time.Millisecond), float64(fi.Size())/1e6)
}

type hit struct {
	id    int64
	score float32
}

func topK(hits []hit, k int) []hit {
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func queryGo(path string, d, k int) {
	t0 := time.Now()
	db, err := sql.Open("sqlite", path)
	must(err)
	defer db.Close()
	q := randUnit(rand.New(rand.NewSource(99)), d)
	rows, err := db.Query(`SELECT id, vec FROM emb`)
	must(err)
	var hits []hit
	buf := make([]float32, d)
	t1 := time.Now()
	for rows.Next() {
		var id int64
		var b []byte
		must(rows.Scan(&id, &b))
		for i := range buf {
			buf[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
		var s float32
		for i := range buf {
			s += buf[i] * q[i]
		}
		hits = append(hits, hit{id, s})
	}
	must(rows.Err())
	t2 := time.Now()
	top := topK(hits, k)
	fmt.Printf("go    n=%d d=%d open=%s scan+dot=%s sort=%s total=%s top1=%d\n", len(hits), d,
		t1.Sub(t0).Round(time.Millisecond), t2.Sub(t1).Round(time.Millisecond), time.Since(t2).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond), top[0].id)
}

func queryInt8(path string, d, k int) {
	t0 := time.Now()
	db, err := sql.Open("sqlite", path)
	must(err)
	defer db.Close()
	qf := randUnit(rand.New(rand.NewSource(99)), d)
	q := make([]int32, d)
	for i, x := range qf {
		q[i] = int32(int8(math.Round(float64(x) * 127 / 0.3)))
	}
	rows, err := db.Query(`SELECT id, vec8 FROM emb`)
	must(err)
	var hits []hit
	t1 := time.Now()
	for rows.Next() {
		var id int64
		var b []byte
		must(rows.Scan(&id, &b))
		var s int32
		for i := range b {
			s += int32(int8(b[i])) * q[i]
		}
		hits = append(hits, hit{id, float32(s)})
	}
	must(rows.Err())
	t2 := time.Now()
	top := topK(hits, k)
	fmt.Printf("int8  n=%d d=%d open=%s scan+dot=%s sort=%s total=%s top1=%d\n", len(hits), d,
		t1.Sub(t0).Round(time.Millisecond), t2.Sub(t1).Round(time.Millisecond), time.Since(t2).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond), top[0].id)
}

func queryVec0(path string, d, k int) {
	t0 := time.Now()
	db, err := sql.Open("sqlite", path)
	must(err)
	defer db.Close()
	q := randUnit(rand.New(rand.NewSource(99)), d)
	t1 := time.Now()
	rows, err := db.Query(`SELECT rowid, distance FROM vemb WHERE embedding MATCH ? AND k = ?`, f32blob(q), k)
	must(err)
	var hits []hit
	for rows.Next() {
		var id int64
		var dist float64
		must(rows.Scan(&id, &dist))
		hits = append(hits, hit{id, float32(dist)})
	}
	must(rows.Err())
	fmt.Printf("vec0  d=%d open=%s knn=%s total=%s top1=%d\n", d,
		t1.Sub(t0).Round(time.Millisecond), time.Since(t1).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond), hits[0].id)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
