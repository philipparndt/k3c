package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// gReadyTimeout is referenced by engines (e.g. k3d --wait) and benchmarks.
var gReadyTimeout = 300 * time.Second

func csv(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	var (
		engines     = flag.String("engines", "k3c,orb", "comma list: k3c,orb,rd,k3d")
		benchmarks  = flag.String("benchmarks", "empty,resume,helm", "comma list: empty,resume,helm,pull (pull is opt-in; Docker Hub rate-limits)")
		variants    = flag.String("variants", "cold,warm", "cold,warm filter for empty/pull")
		iterations  = flag.Int("iterations", 1, "rounds to append this run (results accumulate)")
		power       = flag.Bool("power", true, "sample per-engine energy impact (sudo-free)")
		powerWindow = flag.Int("power-window", 120, "steady-state power window seconds (helm)")
		readySecs   = flag.Int("ready-timeout", 300, "seconds to wait for readiness")
		storePath   = flag.String("store", "results/store.jsonl", "append-only results store")
		fresh       = flag.Bool("fresh", false, "truncate the store before running")
		reportOnly  = flag.Bool("report", false, "regenerate report.html from the store and exit")
		summaryOnly = flag.Bool("summary", false, "print the summary from the store and exit")
	)
	flag.Parse()

	gReadyTimeout = time.Duration(*readySecs) * time.Second
	store, err := openStore(*storePath, *fresh)
	if err != nil {
		die("store: %v", err)
	}
	reportPath := filepath.Join(filepath.Dir(*storePath), "report.html")

	if *reportOnly {
		recs, _ := store.load()
		if err := writeReport(reportPath, recs); err != nil {
			die("report: %v", err)
		}
		fmt.Println(reportPath)
		return
	}
	if *summaryOnly {
		recs, _ := store.load()
		printSummary(recs)
		return
	}

	variantSet := map[string]bool{}
	for _, v := range csv(*variants) {
		variantSet[v] = true
	}
	env := &Env{
		Variants:     variantSet,
		Power:        *power,
		PowerWindow:  time.Duration(*powerWindow) * time.Second,
		ReadyTimeout: gReadyTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runID := time.Now().Format("20060102-150405")
	logf("run %s → store %s", runID, *storePath)

	for _, en := range csv(*engines) {
		eng, err := newEngine(en)
		if err != nil {
			die("%v", err)
		}
		// Host :443 is exclusive — quiesce the other engines first.
		for _, other := range allEngines {
			if other == en || (en == "orbstack" && other == "orb") {
				continue
			}
			if oe, err := newEngine(other); err == nil {
				logf("quiescing '%s' (freeing host for '%s')…", other, en)
				_ = oe.StopAll(ctx)
			}
		}
		for _, bn := range csv(*benchmarks) {
			b, err := newBenchmark(bn)
			if err != nil {
				die("%v", err)
			}
			for i := 1; i <= *iterations; i++ {
				if ctx.Err() != nil {
					break
				}
				logf("=== %s / %s (iter %d/%d) ===", eng.Name(), bn, i, *iterations)
				iter := i
				emit := func(variant, metric string, value float64, unit string) {
					_ = store.append(Record{
						Ts: nowTs(), Run: runID, Engine: eng.Name(), Benchmark: b.Name(),
						Variant: variant, Iteration: iter, Metric: metric, Value: value, Unit: unit,
					})
				}
				if err := b.Run(ctx, env, eng, emit); err != nil {
					warnf("%s/%s iter %d: %v", eng.Name(), bn, i, err)
				}
			}
		}
	}

	recs, _ := store.load()
	fmt.Println()
	printSummary(recs)
	if err := writeReport(reportPath, recs); err != nil {
		warnf("report: %v", err)
	} else {
		okf("report: %s", reportPath)
	}
}

func printSummary(recs []Record) {
	aggs := aggregate(recs)
	cols := []string{"k3c", "orbstack", "rancher", "k3d"}
	type key struct{ b, v, m string }
	groups := map[key]map[string]Agg{}
	var order []key
	for _, a := range aggs {
		k := key{a.Benchmark, a.Variant, a.Metric}
		if groups[k] == nil {
			groups[k] = map[string]Agg{}
			order = append(order, k)
		}
		groups[k][a.Engine] = a
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].b != order[j].b {
			return order[i].b < order[j].b
		}
		if order[i].v != order[j].v {
			return order[i].v < order[j].v
		}
		return order[i].m < order[j].m
	})
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "BENCHMARK\tVARIANT\tMETRIC\tk3c\torbstack\trancher\tk3d\tUNIT")
	for _, k := range order {
		row := groups[k]
		unit := ""
		cells := make([]string, len(cols))
		for i, c := range cols {
			if a, ok := row[c]; ok {
				cells[i] = fmt.Sprintf("%.0f", a.Mean)
				unit = a.Unit
			} else {
				cells[i] = "-"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.b, k.v, k.m, strings.Join(cells, "\t"), unit)
	}
	w.Flush()
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "\033[31m[fail]\033[0m "+format+"\n", a...)
	os.Exit(1)
}
