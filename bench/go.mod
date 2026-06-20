// Nested module: the k3c-vs-others benchmark harness. Kept separate from the
// root k3c module so `go build ./...` / CI for the product never builds it.
module k3cbench

go 1.26
