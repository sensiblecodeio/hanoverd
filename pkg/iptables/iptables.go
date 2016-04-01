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

func localnetRoutingEnabled() bool {
	fd, err := os.Open("/proc/sys/net/ipv4/conf/docker0/route_localnet")
	if err != nil {
		return false
	}
	var routeLocalnet int
	_, err = fmt.Fscan(fd, &routeLocalnet)
	if err != nil {
		return false
	}
	return routeLocalnet == 1
}

func localhostRedirect(source, mappedPort int, ip string, target int) []string {
	if localnetRoutingEnabled() {
		// route_localnet is enabled on the docker bridge.
		// So we can use the same rule as for the remote traffic,
		// except that the rule is applied on the OUTPUT chain instead
		// of the PREROUTING chain.
		return remoteTrafficDNAT(source, ip, target)
	}
	return []string{
		"--table", "nat",
		"--protocol", "tcp",
		// Prevent redirection of packets already going to the container
		"--match", "tcp",
		"!", "--destination", ip,
		// Prevent redirection of ports on remote servers
		// (i.e, don't make google.com:source hit our container)
		"--match", "addrtype", "--dst-type", "LOCAL",
		// Traffic destined for our source port.
		"--destination-port", fmt.Sprint(source),
		"--jump", "REDIRECT",
		"--to-ports", fmt.Sprint(mappedPort),
		"-m", "comment", "--comment", "hanoverd-localhostRedirect",
	}
}

func remoteTrafficDNAT(source int, ip string, target int) []string {
	return []string{
		"--table", "nat",
		"--protocol", "tcp",
		"--match", "tcp",
		// Traffic destined for the source port.
		"--destination-port", fmt.Sprint(source),
		"--jump", "DNAT",
		// Traditional port forward to ip:port.
		// (This sends inbound traffic there. Outbound traffic can
		//  return because of an existing MASQUERADE rule put in place
		//  by docker along the lines of:
		//   -t nat -A POSTROUTING -s {ip}/32 -d {ip}/32 -p tcp -m tcp
		//   --dport {target} -j MASQUERADE
		// )
		"--to-destination", fmt.Sprintf("%v:%v", ip, target),
		"-m", "comment", "--comment", "hanoverd-remoteTrafficDNAT",
	}
}

// ConfigureRedirect forwards ports from `source` to `target` using iptables.
// Returns an error and a function which undoes the change to the firewall.
//
// Beware, there are multiple pieces involved.
//
// Parameters:
// * There is the port listened to inside the container (ipAddress:targetPort)
// * There is the port listened to on the host which docker chooses (mappedPort)
// * There is the source port, where traffic will go to in order to use our
//   service (sourcePort)
//
// Unfortunately, we cannot easily redirect localhost traffic to
// ipAddress:TargetPort. This is not supported without changing scary kernel
// and docker options that I don't want to touch.
//
// In this case, docker has the userland proxy, which accepts the connection on
// localhost and makes an outbound connection to the target.
//
// So we simply make an OUTPUT rule which jumps to REDIRECT for the local
// connections. This redirects localhost->localhost, which is OK, and goes via
// the userland proxy.
//
// For non-local connections in particular we want the receiver to see the
// correct origin IP address. In order for this to happen we want to do a
// traditional PREROUTING DNAT port forward from :sourcePort -> ipAddress:targetPort.
// We also take advantage of the fact docker has a MASQUERADE rule which means
// that packets leaving our machine back towards the remote machine are stamped
// with the correct return address (that of the host, not the container).
func ConfigureRedirect(
	sourcePort, mappedPort int,
	ipAddress string, targetPort int,
) (func() error, error) {
	// PREROUTING rule applies to traffic coming from off-machine.
	undoPreroute, err := iptables(
		"PREROUTING",
		remoteTrafficDNAT(sourcePort, ipAddress, targetPort)...,
	)
	if err != nil {
		return nil, err
	}

	// OUTPUT rule applies to traffic hitting the `localhost` interface.
	undoOutput, err := iptables(
		"OUTPUT",
		localhostRedirect(sourcePort, mappedPort, ipAddress, targetPort)...,
	)
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
