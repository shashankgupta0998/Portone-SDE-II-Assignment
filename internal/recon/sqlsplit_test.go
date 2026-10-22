package recon

import "testing"

func TestSplitStatements(t *testing.T) {
	got := splitStatements("SELECT 1;\n-- comment\nSELECT ';' AS x;\n  ;\n-- trailing comment\n")
	if len(got) != 2 || got[1] != "-- comment\nSELECT ';' AS x" {
		t.Fatalf("got %q", got)
	}
}

func TestContainsParam(t *testing.T) {
	if !containsParam("WHERE a = $2", "$2") || containsParam("WHERE a = $12", "$1") || !containsParam("$12 and $1", "$1") {
		t.Fatal("containsParam")
	}
}
