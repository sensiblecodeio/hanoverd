package main

import (
	"log"

	"github.com/scraperwiki/hanoverd/builder/git"

	"github.com/codegangsta/cli"
)

func main() {
	app := cli.NewApp()

	app.Action = ActionMain

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "url, u",
			Usage: "URL to clone",
		},
		cli.StringFlag{
			Name:  "ref, r",
			Usage: "ref to checkout",
		},
		cli.StringFlag{
			Name:  "destination, d",
			Usage: "destination dir",
			Value: "./src",
		},
	}

	app.RunAndExitOnError()
}

func ActionMain(c *cli.Context) {

	if !c.GlobalIsSet("url") || !c.GlobalIsSet("ref") {
		log.Fatal("--url and --ref required")
		return
	}

	where, err := git.PrepBuildDirectory(
		c.GlobalString("destination"),
		c.GlobalString("url"),
		c.GlobalString("ref"))
	if err != nil {
		log.Fatalln("Error:", err)
	}
	log.Printf("Checked out %v at %v", where.Name, where.Dir)
}
