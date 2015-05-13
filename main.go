// Copyright 2014 The Hanoverd Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/nat"
	"github.com/fsouza/go-dockerclient"
	"github.com/pwaller/barrier"
	"github.com/scraperwiki/hookbot/listen"
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

type Options struct {
	env, publish  []string
	source        ContainerSource
	containerArgs []string
	ports         nat.PortSet
	portBindings  nat.PortMap
	statusURI     string
}

type UpdateEvent struct {
	Source        ContainerSource
	OutputStream  io.Writer
	BuildComplete chan<- struct{}
}

// Determine if stdin is connected without blocking
func IsStdinReadable() bool {
	unix.SetNonblock(int(os.Stdin.Fd()), true)
	_, err := os.Stdin.Read([]byte{0})
	unix.SetNonblock(int(os.Stdin.Fd()), false)
	return err != io.EOF
}

func main() {
	app := cli.NewApp()

	app.Name = "hanoverd"
	app.Usage = "handover docker containers"

	app.Flags = []cli.Flag{
		cli.StringSliceFlag{
			Name:  "env, e",
			Usage: "environment variables to pass (reads from env if = omitted)",
			Value: &cli.StringSlice{},
		},
		cli.StringSliceFlag{
			Name:  "publish, p",
			Usage: "ports to publish (same syntax as docker)",
			Value: &cli.StringSlice{},
		},
		cli.StringFlag{
			Name:  "status-uri",
			Usage: "specify URI which returns 200 OK when functioning correctly",
			Value: "/",
		},
		cli.StringFlag{
			Name:  "hookbot",
			Usage: "url of hookbot websocket endpoint to monitor for updates",
		},
	}

	app.Action = ActionRun

	app.RunAndExitOnError()
}

// Returns true if $HOME/.ssh exists, false otherwise
func HaveDotSSH() bool {
	_, err := os.Stat(os.ExpandEnv("${HOME}/.ssha"))
	return err == nil
}

func ActionRun(c *cli.Context) {
	var err error

	options := Options{}
	options.env = makeEnv(c.StringSlice("env"))
	options.statusURI = c.String("status-uri")

	if c.GlobalIsSet("hookbot") {

		hookbotRe := regexp.MustCompile("/sub/github.com/repo/([^/]+)/([^/]+)/push/branch/([^/]+)")

		hookbotURLStr := c.GlobalString("hookbot")
		hookbotURL, err := url.Parse(hookbotURLStr)
		if err != nil {
			log.Fatalf("Hookbot URL %q does not parse: %v", hookbotURLStr, err)
		}

		if !hookbotRe.MatchString(hookbotURL.Path) {
			log.Fatalf("Hookbot URL path %q does not match %q", hookbotURL.Path, hookbotRe.String())
		}

		groups := hookbotRe.FindStringSubmatch(hookbotURL.Path)
		user, repo, branch := groups[1], groups[2], groups[3]

		format := "https://github.com/%s/%s"
		if HaveDotSSH() {
			format = "git@github.com:%s/%s"
		}

		s := ContainerSource{
			Type:      GithubRepository,
			githubURL: fmt.Sprintf(format, user, repo),
			githubRef: branch,
		}
		options.source = s

		options.containerArgs = c.Args()

		log.Printf("Hookbot monitoring %v@%v via %v", s.githubURL, s.githubRef, hookbotURL.Host)

	} else if len(c.Args()) == 0 {
		options.source.Type = BuildCwd

	} else {
		first := c.Args().First()
		args := c.Args()[1:]

		if first == "@" {
			// If the first arg is "@", then use the Cwd
			options.source.Type = BuildCwd
		} else if first == "daemon" {
			RunDaemon()
		} else {
			options.source.Type = DockerPull
			options.source.dockerImageName = first
		}
		options.containerArgs = args
	}

	if err := CheckIPTables(); err != nil {
		log.Fatal("Unable to run `iptables -L`, see README (", err, ")")
	}

	options.ports, options.portBindings, err = nat.ParsePortSpecs(c.StringSlice("publish"))
	if err != nil {
		log.Fatalln("--publish:", err)
	}

	log.Println("Hanoverd")

	var wg sync.WaitGroup
	defer wg.Wait()

	// Fired when we're signalled to exit
	var dying barrier.Barrier
	defer dying.Fall()

	if IsStdinReadable() {
		log.Println("Press CTRL-D to exit")
		go func() {
			defer dying.Fall()
			defer log.Println("Stdin closed, exiting...")

			// Await Stdin closure, don't care about errors
			_, _ = io.Copy(ioutil.Discard, os.Stdin)
		}()
	}

	events := make(chan UpdateEvent, 1)
	originalEvent := UpdateEvent{Source: options.source}
	events <- originalEvent

	if c.GlobalIsSet("hookbot") {
		go MonitorHookbot(c.GlobalString("hookbot"), events)
	}

	// SIGHUP handler
	go func() {
		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGHUP)
		for _ = range sig {
			// For now, SIGHUP always means build the working dir.
			events <- originalEvent
		}
	}()

	// SIGTERM handler
	go func() {
		defer dying.Fall()
		defer log.Println("Received SIGTERM")
		sig := make(chan os.Signal)
		signal.Notify(sig, unix.SIGTERM)
		<-sig
	}()

	go loop(&wg, &dying, options, events)
	go httpInterface(events)

	<-dying.Barrier()
}

func MonitorHookbot(target string, notify chan<- UpdateEvent) {
	finish := make(chan struct{})
	header := http.Header{}

	events, errs := listen.RetryingWatch(target, header, finish)

	log.Println("Monitoring hookbot")

	for ev := range events {

		data := map[string]string{}
		err := json.Unmarshal(ev, &data)
		if err != nil {
			log.Printf("Failed to parse message: %v", err)
			continue
		}

		done := make(chan struct{})

		outBound := &UpdateEvent{}
		outBound.BuildComplete = done
		outBound.Source.Type = GithubRepository
		outBound.Source.githubURL = "github.com/scraperwiki/hookbot"
		outBound.Source.githubRef = data["SHA"]

		notify <- *outBound

		<-done
		log.Printf("--- Build completed %v ---", outBound.Source.githubRef)
	}

	for err := range errs {
		log.Printf("Error in MonitorHookbot: %v", err)
	}
}

// Make an env []string from a list of options specified on the cmdline.
func makeEnv(opts []string) []string {
	var env []string
	for _, envVar := range opts {
		if strings.Contains(envVar, "=") {
			env = append(env, envVar)
		} else {
			env = append(env, fmt.Sprint(envVar, "=", os.Getenv(envVar)))
		}
	}
	return env
}

func httpInterface(events chan<- UpdateEvent) {
	http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {

		buildComplete := make(chan struct{})
		event := UpdateEvent{
			OutputStream:  NewFlushWriter(w),
			BuildComplete: buildComplete,
		}

		switch r.Method {
		case "POST":
			var (
				reader io.Reader
				err    error
			)

			switch r.Header.Get("Content-Type") {
			default:
				const msg = "Unrecognized Content-Type. Should be application/zip or application/x-tar (or x-bzip2 or gzip)"
				http.Error(w, msg, http.StatusBadRequest)
				return
			case "application/zip":
				reader, err = zip2tar(r.Body)
				if err != nil {
					const msg = "Problem whilst reading zip input"
					http.Error(w, msg, http.StatusBadRequest)
					return
				}

			case "application/x-bzip2", "application/x-tar":
				reader = r.Body
			}

			event.Source = ContainerSource{
				Type:                BuildTarballContent,
				buildTarballContent: reader,
			}
		default:
			fmt.Fprintln(w, "Signal build $PWD")
		}

		events <- event
		// Only wait on buildComplete when we know we got as far as requesting
		// the build.
		<-buildComplete
	})
	http.ListenAndServe("localhost:9123", nil)
}

func dockerConnect() (*docker.Client, error) {
	docker_host := os.Getenv("DOCKER_HOST")
	if docker_host == "" {
		docker_host = "unix:///run/docker.sock"
	}

	docker_tls_verify := os.Getenv("DOCKER_TLS_VERIFY") != ""

	var (
		client *docker.Client
		err    error
	)
	if docker_tls_verify {
		docker_cert_path := os.Getenv("DOCKER_CERT_PATH")
		docker_cert := docker_cert_path + "/cert.pem"
		docker_key := docker_cert_path + "/key.pem"
		// TODO there's no environment variable option in docker client for
		// this, it's called -tlscacert in its command line. We'll leave it
		// as the default (no CA, just trust) which boot2docker uses.
		docker_ca := ""
		client, err = docker.NewTLSClient(docker_host, docker_cert, docker_key, docker_ca)
	} else {
		client, err = docker.NewClient(docker_host)
	}
	return client, err
}

// Main loop managing the lifecycle of all containers.
func loop(wg *sync.WaitGroup, dying *barrier.Barrier, options Options, events chan UpdateEvent) {
	client, err := dockerConnect()
	if err != nil {
		dying.Fall()
		log.Println("Connecting to Docker failed:", err)
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

	lastEvent := <-events

	for {

		c := NewContainer(client, getName(), wg)
		c.Args = options.containerArgs
		c.Env = options.env
		c.StatusURI = options.statusURI

		// Global exit should cause container exit
		dying.Forward(&c.Closing)

		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()

			go func() {
				for err := range c.Errors {
					log.Println("BUG: Async container error:", err)
					// TODO(pwaller): If this case is hit we might not want to
					// tear the container down really.
					c.Failed.Fall()
				}
			}()

			status, err := c.Run(lastEvent)
			if err != nil {
				switch err := err.(type) {
				case *docker.Error:
					// (name) Conflict
					if err.Status == 409 {
						log.Printf("Container with name %q exists, aborting...", c.Name)
						c.Failed.Fall()
						return
					}
				}
				log.Println("Container run failed:", strings.TrimSpace(err.Error()))
				c.Failed.Fall()
				return
			}
			log.Println("container", c.Name, "quit, exit status:", status)
		}(c)

		go func(c *Container) {

			log.Printf("Awaiting container fate: %q %q", c.Name, c.StatusURI)
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
				return

			case <-c.Ready.Barrier():
			}

			log.Println("Container going live:", c.Name)

			liveMutex.Lock()
			defer liveMutex.Unlock()
			previousLive := live

			// Block main exit until the firewall rule has been placed
			// (and removed)
			wg.Add(1)
			defer wg.Done()

			// get the public port for an internal one
			getMappedPort := func(p int) (int, bool) {
				for _, m := range c.container.NetworkSettings.PortMappingAPI() {
					if int(m.PrivatePort) == p {
						return int(m.PublicPort), true
					}
				}
				return -1, false
			}

			removal := []func(){}

			defer func() {
				// Block main exit until firewall rule has been removed
				wg.Add(1)
				go func() {
					defer wg.Done()

					<-c.Closing.Barrier()
					for _, remove := range removal {
						remove()
					}
				}()
			}()

			for internalPort, bindings := range options.portBindings {
				if mappedPort, ok := getMappedPort(internalPort.Int()); ok {
					for _, binding := range bindings {
						var public int
						_, err := fmt.Sscan(binding.HostPort, &public)
						if err != nil {
							// If no public port specified, use same port as internal port
							public = internalPort.Int()
						}

						ipAddress := c.container.NetworkSettings.IPAddress
						remove, err := ConfigureRedirect(public, mappedPort, ipAddress)
						if err != nil {
							// Firewall rule didn't get applied.
							c.err(fmt.Errorf("Firewall rule application failed: %q (public: %v, private: %v)", err, public, internalPort))
							return
						}

						removal = append(removal, remove)
					}
				} else {
					c.err(fmt.Errorf("Docker image not exposing port %v!", internalPort))
					return
				}
			}

			live = c
			if previousLive != nil {
				previousLive.Closing.Fall()
			}

		}(c)

		lastEvent = <-events

		c.Superceded.Fall()

		log.Println("Signalled!")
	}
}
