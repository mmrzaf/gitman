package handlers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxGitHTTPHeaderBytes = 64 * 1024
	maxGitHTTPStderrBytes = 64 * 1024
	gitHTTPWaitDelay      = 500 * time.Millisecond
)

type boundedBuffer struct {
	buf       bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit < 0 {
		limit = 0
	}
	return &boundedBuffer{remaining: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if b.remaining <= 0 {
		if original > 0 {
			b.truncated = true
		}
		return original, nil
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	b.remaining -= len(p)
	return original, nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// gitHTTPBackendResult reports whether response headers have already reached
// the client. Once Started is true, callers must only log backend errors; they
// can no longer replace the response with a Gitman error body.
type gitHTTPBackendResult struct {
	Started         bool
	Stderr          string
	StderrTruncated bool
}

// serveGitHTTPBackend implements the small CGI boundary required by
// git-http-backend while keeping the child process tied to ctx. The standard
// net/http/cgi handler uses exec.Cmd without a request context, which can leave
// a Git process alive after Gitman's operation deadline has expired.
func serveGitHTTPBackend(ctx context.Context, w http.ResponseWriter, r *http.Request, gitBin, repoPath, projectRoot, remoteUser string) (gitHTTPBackendResult, error) {
	var result gitHTTPBackendResult
	// net/http removes HTTP chunk framing before exposing r.Body. For requests
	// with an unknown decoded length, CGI CONTENT_LENGTH is intentionally omitted
	// and git-http-backend reads the body stream until EOF. Stock Git uses this
	// path for larger Smart HTTP pushes.
	cmd := exec.CommandContext(ctx, gitBin, "http-backend")
	cmd.Dir = repoPath
	cmd.Env = gitHTTPBackendEnv(r, gitBin, projectRoot, remoteUser)
	cmd.WaitDelay = gitHTTPWaitDelay
	if r.Body != nil && r.ContentLength != 0 {
		cmd.Stdin = r.Body
	}

	stderr := newBoundedBuffer(maxGitHTTPStderrBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("open git-http-backend output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start git-http-backend: %w", err)
	}

	reader := bufio.NewReaderSize(stdout, 4096)
	status, headers, err := readGitHTTPCGIHeaders(reader)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		result.Stderr = stderr.String()
		result.StderrTruncated = stderr.truncated
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, err
	}

	for name, values := range headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(status)
	result.Started = true

	_, copyErr := io.Copy(w, reader)
	if copyErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	result.Stderr = stderr.String()
	result.StderrTruncated = stderr.truncated

	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if copyErr != nil {
		return result, fmt.Errorf("stream git-http-backend response: %w", copyErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("git-http-backend exited: %w", waitErr)
	}
	return result, nil
}

func readGitHTTPCGIHeaders(reader *bufio.Reader) (int, http.Header, error) {
	headers := make(http.Header)
	status := http.StatusOK
	readBytes := 0
	seenHeader := false

	for {
		line, err := reader.ReadString('\n')
		readBytes += len(line)
		if readBytes > maxGitHTTPHeaderBytes {
			return 0, nil, fmt.Errorf("git-http-backend headers exceed %d bytes", maxGitHTTPHeaderBytes)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, nil, fmt.Errorf("git-http-backend ended before completing CGI headers")
			}
			return 0, nil, fmt.Errorf("read git-http-backend headers: %w", err)
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			break
		}
		seenHeader = true
		name, value, ok := strings.Cut(line, ":")
		if !ok || !validHTTPHeaderName(name) {
			return 0, nil, fmt.Errorf("git-http-backend returned a malformed CGI header")
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			if len(value) < 3 {
				return 0, nil, fmt.Errorf("git-http-backend returned an invalid CGI status")
			}
			code, err := strconv.Atoi(value[:3])
			if err != nil || code < 100 || code > 599 {
				return 0, nil, fmt.Errorf("git-http-backend returned an invalid CGI status")
			}
			status = code
			continue
		}
		headers.Add(name, value)
	}

	if !seenHeader {
		return 0, nil, fmt.Errorf("git-http-backend returned no CGI headers")
	}
	if headers.Get("Content-Type") == "" {
		return 0, nil, fmt.Errorf("git-http-backend response is missing Content-Type")
	}
	return status, headers, nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func gitHTTPBackendEnv(r *http.Request, gitBin, projectRoot, remoteUser string) []string {
	port := "80"
	if r.TLS != nil {
		port = "443"
	}
	host := r.Host
	if parsedHost, parsedPort, err := net.SplitHostPort(r.Host); err == nil {
		host = parsedHost
		port = parsedPort
	}

	remoteAddr := r.RemoteAddr
	remotePort := ""
	if parsedAddr, parsedPort, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remoteAddr = parsedAddr
		remotePort = parsedPort
	}

	env := []string{
		"SERVER_SOFTWARE=gitman",
		"SERVER_PROTOCOL=HTTP/1.1",
		"HTTP_HOST=" + r.Host,
		"GATEWAY_INTERFACE=CGI/1.1",
		"REQUEST_METHOD=" + r.Method,
		"QUERY_STRING=" + r.URL.RawQuery,
		"REQUEST_URI=" + r.URL.RequestURI(),
		"PATH_INFO=" + r.URL.Path,
		"SCRIPT_NAME=",
		"SCRIPT_FILENAME=" + gitBin,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"REMOTE_ADDR=" + remoteAddr,
		"REMOTE_HOST=" + remoteAddr,
		"GIT_PROJECT_ROOT=" + projectRoot,
		"GIT_HTTP_EXPORT_ALL=true",
		"REMOTE_USER=" + remoteUser,
	}
	if remotePort != "" {
		env = append(env, "REMOTE_PORT="+remotePort)
	}
	if r.TLS != nil {
		env = append(env, "HTTPS=on")
	}
	if r.ContentLength > 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		env = append(env, "CONTENT_TYPE="+contentType)
	}

	// Git's protocol-v2 negotiation is the only request header the backend
	// needs after Gitman has authenticated and authorized the request. Keeping
	// the child environment minimal avoids exposing PATs, cookies, proxy
	// credentials, or arbitrary client-controlled headers to the Git process.
	if values := r.Header.Values("Git-Protocol"); len(values) > 0 {
		env = append(env, "HTTP_GIT_PROTOCOL="+strings.Join(values, ", "))
	}

	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		pathValue = "/bin:/usr/bin:/usr/local/bin"
	}
	env = append(env, "PATH="+pathValue)
	for _, name := range gitHTTPInheritedEnvNames() {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func gitHTTPInheritedEnvNames() []string {
	switch runtime.GOOS {
	case "darwin", "ios":
		return []string{"DYLD_LIBRARY_PATH"}
	case "android", "linux", "freebsd", "netbsd", "openbsd":
		return []string{"LD_LIBRARY_PATH"}
	case "windows":
		return []string{"SystemRoot", "COMSPEC", "PATHEXT", "WINDIR"}
	default:
		return nil
	}
}
