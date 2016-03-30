package tests

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	// TCP port chosen by rolling a dice, used for listening.
	testPort = "50820"
)

// simpleGetHTTP returns the body of a HTTP GET response from `url`, or an
// empty string on error.
func simpleGetHTTP(url string) string {
	r, err := http.Get(url)
	if r != nil && r.Body != nil {
		defer r.Body.Close()
	}
	if err != nil {
		return ""
	}
	bs, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	return string(bs)
}

// TestCWDBuildsAdvance attempts to run hanoverd in CWD-build mode in the
// examples directory. It checks that the hostname advances.
func TestCWDBuildsAdvance(t *testing.T) {

	examplePath, err := filepath.Abs(filepath.Join("..", "example"))
	if err != nil {
		t.Fatalf("Unable to determine example path: %v", err)
	}

	// Spawn hanoverd in the example directory

	cmd := exec.Command("hanoverd", "--publish", testPort+":8000")
	cmd.Dir = examplePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		t.Fatalf("hanoverd failed to start, is it installed?: %v", err)
	}

	// Amount of time we're willing to wait for a state transition.
	const timeout = 30 * time.Second
	deadline := time.Now().Add(timeout)

	current := ""
	wanted := "Hello from hanoverd_0\r\n"
	next := map[string]string{
		"Hello from hanoverd_0\r\n": "Hello from hanoverd_1\r\n",
		"Hello from hanoverd_1\r\n": "Hello from hanoverd_2\r\n",
		// When 2 is reached,
		"Hello from hanoverd_2\r\n": "",
	}

	// Rapidly loop doing HTTP requests.
loop:
	for {
		if time.Now().After(deadline) {
			t.Errorf("Took longer than deadline to get a valid response")
			break
		}

		response := simpleGetHTTP("http://localhost:" + testPort)

		switch response {
		default:
			t.Logf("Unexpected response, current = %q wanted = %q, got = %q",
				current, wanted, response)
		case current:
		case wanted:
			deadline = time.Now().Add(timeout)

			current = response
			wanted = next[response]

			// Default: tell hanoverd to reload
			sig := syscall.SIGHUP
			if response == "Hello from hanoverd_2\r\n" {
				// Final: send SIGTERM
				sig = syscall.SIGTERM
			}

			err := cmd.Process.Signal(sig)
			if err != nil {
				t.Errorf("Failed to signal hanoverd: %v", err)
				break loop
			}

			if sig == syscall.SIGTERM {
				break loop
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	err = cmd.Wait()
	if err != nil {
		t.Fatalf("waiting on hanoverd: %v", err)
	}

	log.Printf("Success!")
}
