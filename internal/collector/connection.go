package collector

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
)

type Conn struct {
	PID         int
	ProcessName string
	User        string // add this early

	FD string

	Protocol string // tcp/udp
	Family   string // IPv4/IPv6

	LocalAddr  net.IP
	LocalPort  int
	RemoteAddr net.IP
	RemotePort int

	State string
}

// resolver + classifier
type EnrichedConn struct {
	Conn

	RemoteHost   string
	ServiceLabel string
	Category     string
}

func (c *Conn) Key() string {
	ep1 := fmt.Sprintf("%s:%d", c.LocalAddr, c.LocalPort)
	ep2 := fmt.Sprintf("%s:%d", c.RemoteAddr, c.RemotePort)

	if ep1 > ep2 {
		ep1, ep2 = ep2, ep1
	}

	base := fmt.Sprintf("%s|%s|%s", c.Protocol, ep1, ep2)

	// create new sha object
	sh := sha256.New()
	sh.Write([]byte(base))
	return fmt.Sprintf("%x", sh.Sum(nil))[:32]
}

// if the communication is not with other server, but myself
func (c *Conn) IsLoopBack() bool {
	//TODO: see if this is actually whole required, why not: c.RemoteAddr.IsLoopBack
	return c.LocalAddr.IsLoopback() && (c.RemoteAddr == nil || c.RemoteAddr.IsLoopback())
}

func (c *Conn) IsUDP() bool {
	return c.State == "" && c.RemoteAddr == nil
}

// this returns true if it's not an active connection, but a socket waiting for one
func (c *Conn) IsListen() bool {
	return strings.EqualFold(c.State, "LISTEN") || c.IsUDP()
}

func inferProtocol(file map[string]string) string {
	if p, ok := file["proto"]; ok && p != "" {
		return p
	}
	// If no state, it's UDP (stateless)
	if file["state"] == "" {
		return "udp"
	}
	return "tcp"
}
