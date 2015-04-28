package main

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
	return exec.Command(IPTablesPath, "-L").Run()
}

// Invoke one iptables command.
// Expects "iptables" in the path to be runnable with reasonable permissions.
func iptables(action Action, chain string, source, target int, ipAddress string) *exec.Cmd {
	var cmd *exec.Cmd

	switch action {
	case INSERT:
		cmd = exec.Command(
			IPTablesPath, "--insert", chain, "1",
			"--table", "nat",
			"--protocol", "tcp",
			// Prevent redirection of packets already going to the container
			"--match", "tcp", "!", "--destination", ipAddress,
			// Prevent redirection of ports on remote servers
			// (i.e, don't make google:80 hit our container)
			"--match", "addrtype", "--dst-type", "LOCAL",
			"--dport", fmt.Sprint(source),
			"--jump", "REDIRECT",
			"--to-ports", fmt.Sprint(target))
	case DELETE:
		cmd = exec.Command(
			IPTablesPath, "--delete", chain,
			"--table", "nat",
			"--protocol", "tcp",
			"--match", "tcp", "!", "--destination", ipAddress,
			"--match", "addrtype", "--dst-type", "LOCAL",
			"--dport", fmt.Sprint(source),
			"--jump", "REDIRECT",
			"--to-ports", fmt.Sprint(target))
	}
	cmd.Stderr = os.Stderr
	return cmd
}

// Configure one port redirect from `source` to `target` using iptables.
// Returns an error and a function which undoes the change to the firewall.
func ConfigureRedirect(source, target int, ipAddress string) (func(), error) {

	err := iptables(INSERT, "PREROUTING", source, target, ipAddress).Run()
	if err != nil {
		return nil, err
	}
	err = iptables(INSERT, "OUTPUT", source, target, ipAddress).Run()
	if err != nil {
		return nil, err
	}

	remove := func() {
		err := iptables(DELETE, "PREROUTING", source, target, ipAddress).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
		err = iptables(DELETE, "OUTPUT", source, target, ipAddress).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
	}
	return remove, nil
}
