// Package shell — Docker exec via daemon Unix socket API.
//
// `docker exec -it` from Go fails with "the input device is not a TTY" because
// Go's StdinPipe is a pipe, not a TTY. Calling the Docker daemon directly
// over /var/run/docker.sock solves this: the daemon allocates a PTY inside the
// container regardless of what the caller's stdin looks like.
package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

const dockerSocket  = "/var/run/docker.sock"
const dockerAPIVer  = "v1.41"

// DockerExec wraps a hijacked Docker exec session with a PTY in the container.
type DockerExec struct {
	conn   net.Conn     // underlying Unix socket connection
	reader io.Reader    // reads PTY output (bufio wrapping conn, after HTTP headers consumed)
	execID string
}

// NewDockerExec creates and starts an interactive exec session in containerID.
// cols/rows set the initial PTY size.
func NewDockerExec(containerID string, cols, rows int, _ string) (*DockerExec, error) {
	// ── Step 1: Create the exec instance ─────────────────────────────────────
	// We can't request "bash" directly and fall back on create error: Docker's
	// exec-create succeeds even when the binary is absent (the "not found" error
	// only surfaces at start). So we always launch sh — present in every image
	// (ash on Alpine) — and exec bash from within it only when it exists.
	execID, err := createExec(containerID)
	if err != nil {
		return nil, err
	}

	// ── Step 2: Start exec — response body IS the raw PTY stream ────────────
	conn, reader, err := startExec(execID)
	if err != nil {
		return nil, err
	}

	de := &DockerExec{conn: conn, reader: reader, execID: execID}

	// Set initial PTY size
	de.Resize(rows, cols)

	return de, nil
}

// createExec calls POST /containers/{id}/exec with a shell that prefers bash but
// falls back to sh — so it works in Alpine images (ash, no bash) as well as
// Debian-based ones. sh is launched first (it exists virtually everywhere) and
// re-execs bash only when present, avoiding a request for a missing binary.
func createExec(containerID string) (string, error) {
	body := `{"AttachStdin":true,"AttachStdout":true,"AttachStderr":true,"Tty":true,` +
		`"Cmd":["sh","-c","if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"],` +
		`"Env":["TERM=xterm-256color"]}`
	return doCreateExec(containerID, body)
}

// doCreateExec posts the exec-create request and returns the exec ID.
func doCreateExec(containerID, body string) (string, error) {
	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return "", fmt.Errorf("dial docker socket: %w", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn,
		"POST /%s/containers/%s/exec HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		dockerAPIVer, containerID, len(body), body,
	)

	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("exec create returned %d (shell may not exist)", resp.StatusCode)
	}

	var result struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.ID == "" {
		return "", fmt.Errorf("exec create: empty exec ID")
	}
	return result.ID, nil
}

// startExec calls POST /exec/{id}/start and returns the hijacked connection.
// After the HTTP response headers, the connection carries the raw, full-duplex
// PTY stream: we write stdin to it and read stdout/stderr from it.
func startExec(execID string) (net.Conn, io.Reader, error) {
	body := `{"Detach":false,"Tty":true}`

	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return nil, nil, fmt.Errorf("dial docker socket (start): %w", err)
	}

	// Request a connection HIJACK via the tcp upgrade headers. This is essential:
	// without "Connection: Upgrade" + "Upgrade: tcp" the daemon streams stdout but
	// never reads stdin, so every keystroke is silently dropped (the terminal looks
	// connected but won't accept input). With them the daemon answers 101 and wires
	// stdin AND stdout onto this single connection. (Matches the docker CLI / dockerode.)
	fmt.Fprintf(conn,
		"POST /%s/exec/%s/start HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: %d\r\n\r\n%s",
		dockerAPIVer, execID, len(body), body,
	)

	// Parse the response headers; the bytes AFTER them (held in the bufio.Reader)
	// are the first PTY bytes.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("exec start: %w", err)
	}
	resp.Body.Close()
	// 101 Switching Protocols = the hijack succeeded. Older daemons may answer 200
	// with a raw stream (stdin may not work there, but accept it as a fallback).
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, nil, fmt.Errorf("exec start returned %d", resp.StatusCode)
	}

	// IMPORTANT: read PTY output from the buffered reader (positioned right after the
	// response headers), NOT resp.Body — for a 101 response resp.Body is empty.
	return conn, br, nil
}

// Read reads PTY output bytes.
func (e *DockerExec) Read(p []byte) (int, error) {
	return e.reader.Read(p)
}

// Write sends bytes to the PTY stdin.
func (e *DockerExec) Write(p []byte) (int, error) {
	return e.conn.Write(p)
}

// Close terminates the exec session.
func (e *DockerExec) Close() error {
	return e.conn.Close()
}

// Resize sends a PTY resize (SIGWINCH) to the running process.
func (e *DockerExec) Resize(rows, cols int) {
	if rows <= 0 || cols <= 0 {
		return
	}
	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(conn,
		"POST /%s/exec/%s/resize?h=%d&w=%d HTTP/1.1\r\nHost: localhost\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
		dockerAPIVer, e.execID, rows, cols,
	)
	// Drain the response so the daemon processes it before we return
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err == nil {
		resp.Body.Close()
	}
}
