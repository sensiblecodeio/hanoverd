package main

import (
	"log"
	"os"
	// "sort"

	"github.com/scraperwiki/hanoverd/builder/git"
)

func main() {
	err := git.GitSetMTimes(".", os.Args[1], "HEAD")

	if err != nil {
		log.Fatal(err)
	}
	// times, err := git.GitCommitTimes(".", "HEAD")

	// files := []string{}
	// for file := range times {
	// 	files = append(files, file)
	// }
	// sort.Strings(files)

	// for _, file := range files {
	// 	log.Printf("%v: %v", file, times[file])
	// }

}
