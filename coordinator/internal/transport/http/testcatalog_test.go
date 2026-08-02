package http_test

import (
	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// testCatalog loads the embedded workload catalog for http tests, the same
// catalog the server binary loads at startup.
func testCatalog() *workloads.Catalog {
	catalog, err := workloads.Load()
	if err != nil {
		panic(err)
	}
	return catalog
}
