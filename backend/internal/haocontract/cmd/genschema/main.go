package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	source := filepath.Join("..", "..", "..", "contracts", "hao", "v1", "config.schema.json")
	data, err := os.ReadFile(source)
	if err != nil {
		panic(err)
	}
	output := "// Code generated from contracts/hao/v1/config.schema.json; DO NOT EDIT.\n\npackage haocontract\n\nconst configSchemaJSON = " + strconv.Quote(string(data)) + "\n"
	// #nosec G306 -- generated Go source is repository-readable, not secret state.
	if err := os.WriteFile("config_schema_gen.go", []byte(output), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("generated config_schema_gen.go")
}
