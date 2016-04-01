package iptables

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

var iptablesPath = "iptables"

func init() {
	err := CheckIPTables()
	if err != nil {
		log.Printf("Unable to find iptables, using fallback")
		wd, err := os.Getwd()
		if err != nil {
			return
		}
		iptablesPath = filepath.Join(wd, iptablesPath)
	}
}

// NOTEs from messing with iptables proxying:
// For external:
// iptables -A PREROUTING -t nat -p tcp -m tcp --dport 5555 -j REDIRECT --to-ports 49278
// For internal:
// iptables -A OUTPUT -t nat -p tcp -m tcp --dport 5555 -j REDIRECT --to-ports 49278
// To delete a rule, use -D rather than -A.

// CheckIPTables ensures that `iptables --list` runs without error.
func CheckIPTables() error {
	return execIPTables("--list")
}

// runIPTables invokes iptables with `args`.
// It appends --wait to the end, ensuring that we don't return before the
// command takes effect.
// iptables' stderr is connected to os.Stderr.
func execIPTables(args ...string) error {
	args = append(args, "--wait")
	cmd := exec.Command(iptablesPath, args...)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("firewall rule %q failed to apply: %v", args, err)
	}
	return nil
}

// iptables inserts one IPTables rule.
// The rule is inserted as the top rule, so if there are multiple matching
// rules, last-one wins.
// It returns a function which applies the inverse of the rule.
func iptables(chain string, args ...string) (func() error, error) {
	insertArgs := append([]string{"--insert", chain, "1"}, args...)
	deleteArgs := append([]string{"--delete", chain}, args...)

	err := execIPTables(insertArgs...)
	if err != nil {
		// Rule failed to apply. Inverse is now a no-op.
		noOp := func() error { return nil }
		return noOp, err
	}

	inverse := func() error {
		return execIPTables(deleteArgs...)
	}

	return inverse, nil
}

// ConfigureRedirect forwards ports from `source` to `target` using iptables.
// Returns an error and a function which undoes the change to the firewall.
func ConfigureRedirect(source, target int, ipAddress string) (func() error, error) {
	// This same ruleset is applied to both the PREROUTING and OUTPUT
	// chains.
	args := []string{
		"--table", "nat",
		"--protocol", "tcp",
		// Prevent redirection of packets already going to the container
		"--match", "tcp", "!", "--destination", ipAddress,
		// Prevent redirection of ports on remote servers
		// (i.e, don't make google:80 hit our container)
		"--match", "addrtype", "--dst-type", "LOCAL",
		// Traffic destined for our source port.
		"--destination-port", fmt.Sprint(source),
		"--jump", "REDIRECT",
		"--to-ports", fmt.Sprint(target),
	}

	// PREROUTING rule applies to traffic coming from off-machine.
	undoPreroute, err := iptables("PREROUTING", args...)
	if err != nil {
		return nil, err
	}

	// OUTPUT rule applies to traffic hitting the `localhost` interface.
	undoOutput, err := iptables("OUTPUT", args...)
	if err != nil {
		return nil, err
	}

	remove := func() error {
		// We must apply all inverses.
		// But we'll cope with only the first error we encounter
		// being propagated.
		err1 := undoPreroute()
		err2 := undoOutput()

		if err1 != nil {
			return err1
		}
		if err2 != nil {
			return err2
		}
		return nil
	}

	return remove, nil
}
