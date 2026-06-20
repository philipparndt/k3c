package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Record is one measurement. The store is append-only JSONL: a run appends
// records, and every consumer aggregates the whole store — so adding an engine
// later or running more rounds is just more appends (incremental by design).
type Record struct {
	Ts        string  `json:"ts"`
	Run       string  `json:"run"`
	Engine    string  `json:"engine"`
	Benchmark string  `json:"benchmark"`
	Variant   string  `json:"variant"`
	Iteration int     `json:"iteration"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
}

// Store is an append-only JSONL file of Records.
type Store struct{ path string }

func openStore(path string, fresh bool) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fresh {
		_ = os.Remove(path)
	}
	return &Store{path: path}, nil
}

func (s *Store) append(r Record) error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(r)
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) load() ([]Record, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs, sc.Err()
}

// Agg is the mean of a metric for one engine across every record in the store.
type Agg struct {
	Benchmark string  `json:"benchmark"`
	Variant   string  `json:"variant"`
	Metric    string  `json:"metric"`
	Engine    string  `json:"engine"`
	Unit      string  `json:"unit"`
	Mean      float64 `json:"mean"`
	N         int     `json:"n"`
}

func aggregate(recs []Record) []Agg {
	type key struct{ b, v, m, e string }
	sums := map[key]float64{}
	counts := map[key]int{}
	units := map[key]string{}
	for _, r := range recs {
		k := key{r.Benchmark, r.Variant, r.Metric, r.Engine}
		sums[k] += r.Value
		counts[k]++
		units[k] = r.Unit
	}
	out := make([]Agg, 0, len(sums))
	for k, sum := range sums {
		out = append(out, Agg{k.b, k.v, k.m, k.e, units[k], sum / float64(counts[k]), counts[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Benchmark != b.Benchmark {
			return a.Benchmark < b.Benchmark
		}
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		return a.Engine < b.Engine
	})
	return out
}

func nowTs() string { return time.Now().UTC().Format(time.RFC3339) }
