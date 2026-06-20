# Benchmark suite: k3c vs OrbStack / Rancher Desktop / k3d-on-OrbStack.
# Override any variable, e.g.:  make full ENGINES=k3c,orb ITER=5 POWER=false
BIN        := k3cbench
ENGINES    ?= k3c,orb,k3d,rd
BENCHMARKS ?= empty,resume,pull,helm
VARIANTS   ?= cold,warm
ITER       ?= 3
POWER      ?= true
STORE      ?= results/store.jsonl
EXTRA      ?=

.PHONY: help build full k8s quick summary report open clean

help: ## list targets
	@grep -E '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | sort | column -t -s "$$(printf '\t')"

build: ## build the bench binary
	go build -o $(BIN) .

full: build ## run the FULL bench: every engine and benchmark (results append)
	@echo "Full bench: $(ENGINES) x $(BENCHMARKS), $(ITER) rounds. Cycles OrbStack/RD/k3c daemons."
	./$(BIN) -engines $(ENGINES) -benchmarks $(BENCHMARKS) -variants $(VARIANTS) \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

k8s: build ## full bench but k8s engines only (k3c, orb, k3d)
	./$(BIN) -engines k3c,orb,k3d -benchmarks $(BENCHMARKS) -variants $(VARIANTS) \
	  -iterations $(ITER) -power=$(POWER) -store $(STORE) $(EXTRA)

quick: build ## fast sanity run: k3c vs orb, empty cold, no power
	./$(BIN) -engines k3c,orb -benchmarks empty -variants cold -iterations 1 -power=false -store $(STORE)

summary: build ## print the comparison table from the store
	./$(BIN) -summary -store $(STORE)

report: build ## (re)generate results/report.html from the store
	./$(BIN) -report -store $(STORE)

open: report ## generate and open the HTML report
	open results/report.html

clean: ## remove the built binary and all results
	rm -f $(BIN)
	rm -rf results
