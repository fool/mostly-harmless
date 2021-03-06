// +build vfsgen

package main

import (
	"log"

	"github.com/FiloSottile/mostly-harmless/covfefe/cmd/webfefe/data"
	"github.com/shurcooL/vfsgen"
)

func main() {
	err := vfsgen.Generate(data.Templates, vfsgen.Options{
		PackageName:  "data",
		BuildTags:    "!dev,!generate",
		VariableName: "Templates",
	})
	if err != nil {
		log.Fatalln(err)
	}
}
