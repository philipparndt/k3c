# Benchmark suite: k3c vs OrbStack / Rancher Desktop / colima, and k3d on each.
# Override any variable, e.g.:  make full ENGINES=k3c,orb ITER=5 POWER=false
BIN        := k3cbench
# Engines: native providers (k3c, orb, rancher, colima) + k3d on each provider's
# docker. pull and edx are NOT in the default set (Docker Hub limits / very
# heavy) — run them explicitly via `make pull` / `make edx`.
ENGINES    ?= k3c,orb,rancher,colima,orb-k3d,rancher-k3d,colima-k3d
BENCHMARKS ?= empty,resume,helm
VARIANTS   ?= cold,warm
ITER       ?= 3
POWER      ?= true
STORE      ?= results/store.jsonl
EXTRA      ?=
# keep the Mac awake for the duration of a run (idle sleep would pause the
# VMs and ruin timings/energy); the display may still sleep
RUN        := caffeinate -i ./$(BIN)

.PHONY: help build full k8s quick pull edx summary report open clean

help: ## list targets
	@grep -E '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | sort | column -t -s "$$(printf '\t')"

build: ## build the bench binary
	go build -o $(BIN) .

full: build ## run the FULL bench: every engine and benchmark (results append)
	@echo "Full bench: $(ENGINES) x $(BENCHMARKS), $(ITER) rounds. Cycles OrbStack/RD/k3c daemons."
	$(RUN) -engines $(ENGINES) -benchmarks $(BENCHMARKS) -variants $(VARIANTS) \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

k8s: build ## full bench but k8s engines only (k3c, orb, k3d)
	$(RUN) -engines k3c,orb,k3d -benchmarks $(BENCHMARKS) -variants $(VARIANTS) \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

quick: build ## fast sanity run: k3c vs orb, empty cold, no power
	$(RUN) -engines k3c,orb -benchmarks empty -variants cold -iterations 1 -power=false -store $(STORE)

pull: build ## image-pull bench (opt-in; Docker Hub rate-limits — prefer the cache or BENCH_PULL_IMAGES)
	$(RUN) -engines $(ENGINES) -benchmarks pull -variants $(VARIANTS) \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

edx: build ## Open edX heavy build (docker providers only; very heavy, Docker Hub bound)
	$(RUN) -engines k3c,orb,rancher,colima -benchmarks edx \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

summary: build ## print the comparison table from the store
	./$(BIN) -summary -store $(STORE)

report: build ## (re)generate results/report.html from the store
	./$(BIN) -report -store $(STORE)

open: report ## generate and open the HTML report
	open results/report.html

clean: ## remove the built binary and all results
	rm -f $(BIN)
	rm -rf results
