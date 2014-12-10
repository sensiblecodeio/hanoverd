package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

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
	return exec.Command("iptables", "-L").Run()
}

// Invoke one iptables command.
// Expects "iptables" in the path to be runnable with reasonable permissions.
func iptables(action Action, chain string, source, target int) *exec.Cmd {
	var cmd *exec.Cmd

	switch action {
	case INSERT:
		cmd = exec.Command(
			"iptables", "--insert", chain, "1",
			"--table", "nat", "--jump", "REDIRECT",
			"--protocol", "tcp", "--match", "tcp",
			"--dport", fmt.Sprint(source), "--to-ports", fmt.Sprint(target))
	case DELETE:
		cmd = exec.Command(
			"iptables", "--delete", chain,
			"--table", "nat", "--jump", "REDIRECT",
			"--protocol", "tcp", "--match", "tcp",
			"--dport", fmt.Sprint(source), "--to-ports", fmt.Sprint(target))
	}
	cmd.Stderr = os.Stderr
	return cmd
}

// Configure one port redirect from `source` to `target` using iptables.
// Returns an error and a function which undoes the change to the firewall.
func ConfigureRedirect(source, target int) (func(), error) {

	err := iptables(INSERT, "PREROUTING", source, target).Run()
	if err != nil {
		return nil, err
	}
	err = iptables(INSERT, "OUTPUT", source, target).Run()
	if err != nil {
		return nil, err
	}

	remove := func() {
		err := iptables(DELETE, "PREROUTING", source, target).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
		err = iptables(DELETE, "OUTPUT", source, target).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
	}
	return remove, nil
}
