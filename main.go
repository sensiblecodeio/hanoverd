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

	go loop(&wg, &dying)

	<-dying.Barrier()
}

func loop(wg *sync.WaitGroup, dying *barrier.Barrier) {
	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt)

	client, err := docker.NewClient("unix:///run/docker.sock")
	if err != nil {
		dying.Fall()
		log.Println(err)
		return
	}

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

	var liveMutex sync.Mutex
	var live *Container

	for {

		c := NewContainer(client, getName(), wg, dying)

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
				c.Failed.Fall()
				return
			}
			log.Println(c.Name, "exit:", status)
		}(c)

		go func(c *Container) {

			log.Println("Awaiting container fate:", c.Name)
			select {
			case <-c.Failed.Barrier():
				log.Println("Container failed before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Superceded.Barrier():
				log.Println("Container superceded before going live:", c.Name)
				c.Closing.Fall()
				return
			case <-c.Closing.Barrier():
				log.Println("Container closed before going live:", c.Name)
				log.Println("(This should never happen?)")
				return

			case <-c.Ready.Barrier():
			}

			log.Println("Container going live:", c.Name)

			liveMutex.Lock()
			defer liveMutex.Unlock()
			previousLive := live

			// Block main exit until the firewall rule has been removed
			wg.Add(1)

			target := c.container.NetworkSettings.PortMappingAPI()[0].PublicPort
			remove, err := ConfigureRedirect(5555, target)
			if err != nil {
				// Firewall rule didn't get applied.
				wg.Done()
				c.err(err)
				return
			}

			live = c
			if previousLive != nil {
				previousLive.Closing.Fall()
			}

			// Networking
			go func() {
				defer wg.Done()

				<-c.Closing.Barrier()
				remove()
			}()
		}(c)

		<-sig

		c.Superceded.Fall()

		log.Println("Signalled!")
	}
}
