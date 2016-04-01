package iptables

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

var IPTablesPath = "iptables"

func init() {

	err := CheckIPTables()
	if err != nil {
		log.Printf("Unable to find iptables, using fallback")
		wd, err := os.Getwd()
		if err != nil {
			return
		}
		IPTablesPath = filepath.Join(wd, IPTablesPath)
	}

}

// NOTEs from messing with iptables proxying:
// For external:
// iptables -A PREROUTING -t nat -p tcp -m tcp --dport 5555 -j REDIRECT --to-ports 49278
// For internal:
// iptables -A OUTPUT -t nat -p tcp -m tcp --dport 5555 -j REDIRECT --to-ports 49278
// To delete a rule, use -D rather than -A.

type Action bool

const (
	INSERT Action = true
	DELETE        = false
)

func CheckIPTables() error {
	cmd := exec.Command(IPTablesPath, "--list", "--wait")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Invoke one iptables command.
// Expects "iptables" in the path to be runnable with reasonable permissions.
func iptables(action Action, chain string, args ...string) error {
	switch action {
	case INSERT:
		args = append([]string{"--insert", chain, "1"}, args...)
	case DELETE:
		args = append([]string{"--delete", chain}, args...)
	}

	cmd := exec.Command(IPTablesPath, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Configure one port redirect from `source` to `target` using iptables.
// Returns an error and a function which undoes the change to the firewall.
func ConfigureRedirect(source, target int, ipAddress string) (func(), error) {
	args := []string{
		"--table", "nat",
		"--protocol", "tcp",
		// Prevent redirection of packets already going to the container
		"--match", "tcp", "!", "--destination", ipAddress,
		// Prevent redirection of ports on remote servers
		// (i.e, don't make google:80 hit our container)
		"--match", "addrtype", "--dst-type", "LOCAL",
		"--dport", fmt.Sprint(source),
		"--jump", "REDIRECT",
		"--to-ports", fmt.Sprint(target), "--wait",
	}

	err := iptables(INSERT, "PREROUTING", args...)
	if err != nil {
		return nil, err
	}
	err = iptables(INSERT, "OUTPUT", args...)
	if err != nil {
		return nil, err
	}

	remove := func() {
		err := iptables(DELETE, "PREROUTING", args...)
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
		err = iptables(DELETE, "OUTPUT", args...)
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
	}
	return remove, nil
}
