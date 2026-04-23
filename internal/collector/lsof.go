package collector

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

type LsofCollector struct{}

type Snapshot map[string]Conn

func (c *LsofCollector) Stream(ctx context.Context, out chan<- Conn) error {
	cmd := exec.CommandContext(
		ctx,
		"lsof",
		"-i",
		"-n",
		"-P",
		"-F",
		"pcutnTf",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)

	currentProc := make(map[string]string)
	currentFile := make(map[string]string)

	var hasAddress bool

	resetFile := func() {
		currentFile = make(map[string]string)
		hasAddress = false
	}

	emit := func() {
		if !hasAddress {
			return
		}

		conn, err := buildConn(currentProc, currentFile)
		if err != nil {
			return
		}

		select {
		case out <- conn:
		case <-ctx.Done():
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			continue
		}

		key := string(line[0])
		val := line[1:]

		switch key {

		// 🔹 New process
		case "p":
			emit()
			currentProc = map[string]string{"p": val}
			resetFile()

		// 🔹 Process fields
		case "c", "u":
			currentProc[key] = val

		// 🔹 New file/socket
		case "f":
			emit()
			resetFile()
			currentFile["f"] = val

		// 🔹 File fields
		case "t":
			currentFile["t"] = val

		case "n":
			currentFile["n"] = val
			hasAddress = true

		case "T":
			parseTField(currentFile, val)
		}
	}

	// Emit last pending record
	emit()

	return cmd.Wait()
}

func buildConn(proc, file map[string]string) (Conn, error) {
	pid, err := strconv.Atoi(proc["p"])
	if err != nil {
		return Conn{}, err
	}

	localIP, localPort, remoteIP, remotePort := parseAddress(file["n"])

	// basic validation
	if localIP == nil && localPort == 0 {
		return Conn{}, errors.New("invalid address")
	}

	return Conn{
		PID:         pid,
		ProcessName: proc["c"],
		User:        proc["u"],

		FD:       file["f"],
		Protocol: inferProtocol(file),
		Family:   file["t"],

		LocalAddr:  localIP,
		LocalPort:  localPort,
		RemoteAddr: remoteIP,
		RemotePort: remotePort,

		State: file["state"],
	}, nil
}

func parseAddress(s string) (net.IP, int, net.IP, int) {
	parts := strings.Split(s, "->")

	lip, lport := splitHostPort(parts[0])

	var rip net.IP
	var rport int

	if len(parts) > 1 {
		rip, rport = splitHostPort(parts[1])
	}

	return lip, lport, rip, rport
}

func splitHostPort(s string) (net.IP, int) {
	s = strings.TrimSpace(s)

	if s == "" || s == "*" {
		return nil, 0
	}

	// Handle IPv6 without brackets (rare lsof weirdness)
	if strings.Count(s, ":") > 1 && !strings.Contains(s, "]") {
		lastColon := strings.LastIndex(s, ":")
		host := s[:lastColon]
		portStr := s[lastColon+1:]

		port, _ := strconv.Atoi(portStr)
		return net.ParseIP(host), port
	}

	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return nil, 0
	}

	port, _ := strconv.Atoi(portStr)
	return net.ParseIP(host), port
}

func parseTField(m map[string]string, val string) {
	switch {
	case strings.HasPrefix(val, "ST="):
		m["state"] = val[3:]

	case strings.HasPrefix(val, "PROTO="):
		m["proto"] = strings.ToLower(val[6:])

	case strings.HasPrefix(val, "QR="):
		m["recvq"] = val[3:]

	case strings.HasPrefix(val, "QS="):
		m["sendq"] = val[3:]
	}
}
