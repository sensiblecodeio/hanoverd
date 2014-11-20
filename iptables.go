package main

import (
	"fmt"
	"log"
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

func iptables(action Action, chain string, source, target int64) *exec.Cmd {
	switch action {
	case INSERT:
		return exec.Command(
			"iptables", "-I", chain, "1",
			"-t", "nat", "-j", "REDIRECT",
			"-p", "tcp", "-m", "tcp",
			"--dport", fmt.Sprint(source), "--to-ports", fmt.Sprint(target))
	case DELETE:
		return exec.Command(
			"iptables", "-D", chain,
			"-t", "nat", "-j", "REDIRECT",
			"-p", "tcp", "-m", "tcp",
			"--dport", fmt.Sprint(source), "--to-ports", fmt.Sprint(target))
	}
	panic("unreachable")
}

func ConfigureRedirect(source, target int64) (func(), error) {

	log.Println("Setting up prerouting rule for", target)
	err := iptables(INSERT, "PREROUTING", source, target).Run()
	if err != nil {
		return nil, err
	}
	log.Println("Setting up output rule for", target)
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
