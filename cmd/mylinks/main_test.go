package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverBinary is the server built by TestMain, shared by the tests below.
var serverBinary string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "mylinks-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create temporary directory: %v\n", err)
		os.Exit(1)
	}

	serverBinary = filepath.Join(tempDir, "mylinks")
	// Note that this build is a plain one, the server under test is not
	// instrumented even when the tests themselves run with -race.
	if output, err := exec.Command("go", "build", "-o", serverBinary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build the server: %s\n", output)
		_ = os.RemoveAll(tempDir)
		os.Exit(1)
	}

	code := m.Run()

	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

// TestGracefulShutdown verifies that the server shuts down on SIGTERM and
// closes the database, which checkpoints and removes the WAL file.
func TestGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test which runs the server")
	}

	server, _, dataDir := startServer(t)

	// While the server is running, the database is open in WAL mode
	databaseFile := filepath.Join(dataDir, databaseName)
	require.FileExists(t, databaseFile+"-wal", "Expected a WAL file while running")

	require.NoError(t, server.Process.Signal(syscall.SIGTERM), "Failed to signal the server")
	assert.NoError(t, server.Wait(), "The server did not exit cleanly")

	assert.NoFileExists(t, databaseFile+"-wal", "WAL file left behind")
	assert.NoFileExists(t, databaseFile+"-shm", "Shared memory file left behind")
}

// TestShutdownWaitsForInFlightRequest verifies that a request which is being
// handled when the signal arrives is allowed to finish.
func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test which runs the server")
	}

	server, port, _ := startServer(t)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err, "Failed to connect to the server")
	t.Cleanup(func() {
		_ = conn.Close()
	})
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Minute)), "Failed to set deadline")

	// Send a request whose body arrives in two parts, so that the handler is
	// still waiting for the rest of it when the server is asked to shut down.
	body := "note-title=In+flight&note-text=Saved+during+shutdown"
	head := fmt.Sprintf("POST / HTTP/1.1\r\n"+
		"Host: 127.0.0.1:%d\r\n"+
		"Origin: http://127.0.0.1:%d\r\n"+
		"Content-Type: application/x-www-form-urlencoded\r\n"+
		"Content-Length: %d\r\n\r\n", port, port, len(body))
	_, err = io.WriteString(conn, head+body[:10])
	require.NoError(t, err, "Failed to send the request")

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, server.Process.Signal(syscall.SIGTERM), "Failed to signal the server")
	time.Sleep(100 * time.Millisecond)

	_, err = io.WriteString(conn, body[10:])
	require.NoError(t, err, "Failed to send the rest of the request")

	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "The in-flight request got no response")
	_ = response.Body.Close()
	assert.Equal(t, http.StatusCreated, response.StatusCode, "The in-flight request was not completed")

	assert.NoError(t, server.Wait(), "The server did not exit cleanly")
}

// bcryptSecret is the bcrypt hash of "secret", cost 10.
const bcryptSecret = "$2a$10$g6h.kOLcUo4A7Yg9X1hPGeCJT523p2.6xNk9Yldg2iyqz2F7U2o9e"

// TestRejectsMalformedHtpasswd verifies that a basic auth file the server
// cannot fully parse aborts startup, naming the offending line, rather than
// leaving the login the operator wrote silently non-existent.
func TestRejectsMalformedHtpasswd(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test which runs the server")
	}

	passwordFile := filepath.Join(t.TempDir(), "htpasswd")
	// A plain text password, which the strict loader refuses because it is
	// not a bcrypt hash.
	require.NoError(t, os.WriteFile(passwordFile, []byte("alice:secret\n"), 0600), "Failed to write the htpasswd file")

	server := exec.Command(serverBinary, "-addr", "127.0.0.1", "-port", strconv.Itoa(freePort(t)),
		"-data", filepath.Join(t.TempDir(), "data"), "-basic-auth-file", passwordFile)
	output, err := server.CombinedOutput()

	assert.Error(t, err, "The server started with an htpasswd file it could not parse")
	assert.Contains(t, string(output), "line 1", "The failure does not name the offending line")
}

// TestUnauthorizedResponse verifies what an unauthenticated request gets: the
// challenge, and a JSON body.
func TestUnauthorizedResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test which runs the server")
	}

	passwordFile := filepath.Join(t.TempDir(), "htpasswd")
	require.NoError(t, os.WriteFile(passwordFile, []byte("alice:"+bcryptSecret+"\n"), 0600), "Failed to write the htpasswd file")

	_, port, _ := startServerWithArgs(t, http.StatusUnauthorized, "-basic-auth-file", passwordFile, "-basic-auth-realm", "test-realm")

	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	require.NoError(t, err, "Failed to make the request")
	defer func() {
		_ = response.Body.Close()
	}()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Equal(t, `Basic realm="test-realm"`, response.Header.Get("WWW-Authenticate"))
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err, "Failed to read the response body")
	assert.JSONEq(t, `{"error":"unauthorized"}`, string(body))

	// The credentials from the file are accepted.
	request, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	require.NoError(t, err)
	request.SetBasicAuth("alice", "secret")
	authenticated, err := http.DefaultClient.Do(request)
	require.NoError(t, err, "Failed to make the authenticated request")
	_ = authenticated.Body.Close()
	assert.Equal(t, http.StatusOK, authenticated.StatusCode, "The credentials from the htpasswd file were not accepted")
}

// startServer starts the server on a free port with an empty data directory,
// and waits for it to serve requests.
func startServer(t *testing.T) (*exec.Cmd, int, string) {
	t.Helper()

	return startServerWithArgs(t, http.StatusOK)
}

// startServerWithArgs is startServer with extra command line arguments, and
// the status the root path answers with once the server is up — which is not
// 200 when the arguments put authentication in front of it.
func startServerWithArgs(t *testing.T, readyStatus int, extraArgs ...string) (*exec.Cmd, int, string) {
	t.Helper()

	dataDir := filepath.Join(t.TempDir(), "data")
	port := freePort(t)
	args := append([]string{"-addr", "127.0.0.1", "-port", strconv.Itoa(port), "-data", dataDir}, extraArgs...)
	server := exec.Command(serverBinary, args...)
	server.Stdout = os.Stderr
	server.Stderr = os.Stderr
	require.NoError(t, server.Start(), "Failed to start the server")
	t.Cleanup(func() {
		// Harmless once the test has shut the server down itself
		_ = server.Process.Kill()
		_ = server.Wait()
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	require.Eventually(t, func() bool {
		response, err := http.Get(baseURL)
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == readyStatus
	}, 30*time.Second, 50*time.Millisecond, "The server did not start serving")

	return server, port, dataDir
}

// freePort returns a port which is free at the time of the call. There is a
// small window in which something else can take it before the server binds.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "Failed to find a free port")
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close(), "Failed to release the port")

	return port
}
