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
	// ── Step 1: Create the exec instance (bash → sh fallback) ────────────────
	execID, err := createExec(containerID)
	if err != nil {
		// bash not available — retry with sh
		execID, err = createExecSh(containerID)
		if err != nil {
			return nil, err
		}
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

// createExec calls POST /containers/{id}/exec with bash.
func createExec(containerID string) (string, error) {
	body := `{"AttachStdin":true,"AttachStdout":true,"AttachStderr":true,"Tty":true,` +
		`"Cmd":["bash"],"Env":["TERM=xterm-256color"]}`
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

// createExecSh is the fallback for containers that don't have bash.
func createExecSh(containerID string) (string, error) {
	body := `{"AttachStdin":true,"AttachStdout":true,"AttachStderr":true,"Tty":true,` +
		`"Cmd":["sh"],"Env":["TERM=xterm-256color"]}`
	return doCreateExec(containerID, body)
}

// startExec calls POST /exec/{id}/start and returns the hijacked connection.
// After the HTTP response headers, the body is the raw PTY stream.
func startExec(execID string) (net.Conn, io.Reader, error) {
	body := `{"Detach":false,"Tty":true}`

	conn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		return nil, nil, fmt.Errorf("dial docker socket (start): %w", err)
	}

	fmt.Fprintf(conn,
		"POST /%s/exec/%s/start HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		dockerAPIVer, execID, len(body), body,
	)

	// Read the HTTP response headers — after them, conn carries raw PTY bytes.
	// We use a bufio.Reader to parse HTTP headers; any bytes buffered in it
	// after the headers are the first bytes of PTY output.
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("exec start: %w", err)
	}

	// The Docker daemon responds with 200 OK; the body is the live PTY stream.
	// IMPORTANT: do NOT close resp.Body — it IS the stream we will read from.
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		conn.Close()
		return nil, nil, fmt.Errorf("exec start returned %d", resp.StatusCode)
	}

	// resp.Body wraps r (the bufio.Reader), which wraps conn.
	// Reading from resp.Body correctly drains any buffered HTTP bytes first,
	// then reads live PTY output from conn.
	return conn, resp.Body, nil
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
