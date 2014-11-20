// Copyright 2014 The Hanoverd Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"sync"
	"sync/atomic"

	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
)

// DockerErrorStatus returns the HTTP status code represented by `err` or Status
// OK if no error or 0 if err != nil and is not a docker error.
func DockerErrorStatus(err error) int {
	if err, ok := err.(*docker.Error); ok {
		return err.Status
	}
	if err == nil {
		return http.StatusOK
	}
	return 0
}

func main() {
	log.Println("Hanoverd")

	client, err := docker.NewClient("unix:///run/docker.sock")
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	go func() {
		defer dying.Fall()
		// Await Stdin closure, don't care about errors
		_, _ = io.Copy(ioutil.Discard, os.Stdin)
	}()

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	baseName := path.Base(wd)

	var i uint64
	getName := func() string {
		n := atomic.AddUint64(&i, 1)
		return fmt.Sprint(baseName, "_", n)
	}

	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, os.Interrupt)
		var previous *Container
		for {

			c := NewContainer(client, getName(), &wg, &dying)
			wg.Add(1)
			go func(c *Container) {
				defer wg.Done()

				go func() {
					for err := range c.Errors {
						log.Println("BUG: Async container error:", err)
					}
				}()

				status, err := c.Run()
				if err != nil {
					log.Println(err)
					dying.Fall()
					return
				}
				log.Println(c.Name, "exit:", status)
			}(c)

			redirected := make(chan struct{})

			// Configure port redirect
			go func() {
				<-c.Ready.Barrier()

				target := c.container.NetworkSettings.PortMappingAPI()[0].PublicPort
				remove, err := ConfigureRedirect(5555, target)
				if err != nil {
					c.err(err)
					return
				}

				// Networking
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-c.Closing.Barrier()
					remove()
				}()
				close(redirected)
			}()

			if previous != nil {
				// Previous container teardown
				go func(previous *Container) {
					// Await ready
					<-redirected

					// TODO(pwaller): "kill all previous"
					previous.Closing.Fall()
				}(previous)
			}

			<-sig
			log.Println("Signalled!")
			previous = c
		}
	}()

	<-dying.Barrier()
}
