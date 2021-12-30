//go:generate go run assets_generate.go

//go:build generate
// +build generate

package main

import (
	"log"
	"net/http"

	"github.com/shurcooL/vfsgen"
)

func main() {
	var assets http.FileSystem = http.Dir("data")

	err := vfsgen.Generate(assets, vfsgen.Options{
		PackageName:  "data",
		BuildTags:    "!generate",
		VariableName: "Assets",
	})
	if err != nil {
		log.Fatalln(err)
	}
}
