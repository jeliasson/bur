// Package ports parses port specs and allocates free host ports for
// publishing sandbox services.
package ports

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultPublishIP is the host interface published ports bind to unless the
// spec names one explicitly. Loopback keeps a sandboxed agent's services off
// the LAN by default; widen it per-port with an "ip:host:container" spec.
const DefaultPublishIP = "127.0.0.1"

type Mapping struct {
	HostIP    string
	Host      int
	Container int
}

// BindIP is the interface a port publishes on: the spec's explicit host IP,
// or the loopback default when the spec omits one.
func (p Mapping) BindIP() string {
	if p.HostIP == "" {
		return DefaultPublishIP
	}
	return p.HostIP
}

// ParseSpec parses a port spec in one of three forms:
//
//	containerPort                    -> host==container, default (loopback) IP
//	hostPort:containerPort           -> default (loopback) IP
//	hostIP:hostPort:containerPort    -> explicit host IP (e.g. 0.0.0.0 for LAN)
//
// An empty returned hostIP means "use DefaultPublishIP". Only IPv4 host IPs
// are supported (the spec is colon-split, so bracketed IPv6 does not parse).
func ParseSpec(spec string) (hostIP string, host, container int, err error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		p, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid port %q", spec)
		}
		return "", p, p, nil
	case 2:
		h, err1 := strconv.Atoi(parts[0])
		c, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return "", 0, 0, fmt.Errorf("invalid port mapping %q", spec)
		}
		return "", h, c, nil
	case 3:
		ip := parts[0]
		if net.ParseIP(ip) == nil {
			return "", 0, 0, fmt.Errorf("invalid host IP in port mapping %q", spec)
		}
		h, err1 := strconv.Atoi(parts[1])
		c, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			return "", 0, 0, fmt.Errorf("invalid port mapping %q", spec)
		}
		return ip, h, c, nil
	default:
		return "", 0, 0, fmt.Errorf("invalid port mapping %q", spec)
	}
}

func portFree(ip string, p int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, p))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// Allocate resolves requested ports to free host ports, scanning
// upward from the requested port when taken. The free check probes the
// same interface the port will publish on.
func Allocate(specs []string) ([]Mapping, error) {
	used := map[int]bool{}
	var maps []Mapping
	for _, spec := range specs {
		hostIP, host, container, err := ParseSpec(spec)
		if err != nil {
			return nil, err
		}
		m := Mapping{HostIP: hostIP, Container: container}
		p := host
		for ; p <= 65535; p++ {
			if !used[p] && portFree(m.BindIP(), p) {
				break
			}
		}
		if p > 65535 {
			return nil, fmt.Errorf("no free host port found for %q", spec)
		}
		used[p] = true
		m.Host = p
		maps = append(maps, m)
	}
	return maps, nil
}
