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

func iptables(addOrRemove, chain string, source, target int64) *exec.Cmd {
	return exec.Command(
		"iptables",
		"-t", "nat",
		addOrRemove, chain,
		"-j", "REDIRECT",
		"-p", "tcp", "-m", "tcp",
		"--dport", fmt.Sprint(source),
		"--to-ports", fmt.Sprint(target))
}

func ConfigureRedirect(source, target int64) (func(), error) {

	log.Println("Setting up prerouting rule for", target)
	err := iptables("-A", "PREROUTING", source, target).Run()
	if err != nil {
		return nil, err
	}
	log.Println("Setting up output rule for", target)
	err = iptables("-A", "OUTPUT", source, target).Run()
	if err != nil {
		return nil, err
	}

	remove := func() {
		err := iptables("-D", "PREROUTING", source, target).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
		err = iptables("-D", "OUTPUT", source, target).Run()
		if err != nil {
			log.Println("Failed to remove iptables rule:", source, target)
		}
	}
	return remove, nil
}
