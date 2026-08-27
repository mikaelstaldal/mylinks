package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/mikaelstaldal/go-server-common/auth"
	"github.com/mikaelstaldal/go-server-common/csrf"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/web"
)

const databaseName = "mylinks.sqlite"
const screenshotsDir = "screenshots"

// shutdownTimeout bounds how long in-flight requests get to finish on
// shutdown. It exceeds the server write timeout, so a request which is
// allowed to run to completion is not cut short here.
const shutdownTimeout = 25 * time.Second

func main() {
	os.Exit(run())
}

// run is separate from main so that deferred cleanup, notably closing the
// database, runs before the process exits. The exit code is a named return so
// that the deferred cleanup can report a failure of its own.
func run() (exitCode int) {
	// Determine the path of executable
	executablePath, err := os.Executable()
	if err != nil {
		log.Fatalf("could not determine executable path: %v", err)
	}
	executableDir := filepath.Dir(executablePath)

	// Define command line flags
	version := flag.Bool("version", false, "print version information and exit")
	port := flag.Int("port", 8080, "port to listen on")
	addr := flag.String("addr", "127.0.0.1", "address to listen on")
	dataDir := flag.String("data", "data", "directory to store data in")
	basicAuthFile := flag.String("basic-auth-file", "", "enable HTTP basic auth with username and password from given file in htpasswd format (bcrypt only)")
	basicAuthRealm := flag.String("basic-auth-realm", "mylinks", "realm for HTTP basic auth")
	publicURL := flag.String("public-url", "", "Public-facing base URL for CSRF validation, e.g. https://example.com (defaults to http://<addr>:<port>)")
	flag.Parse()

	if *version {
		printVersion()
		return 0
	}

	if *port < 1 || *port > 65535 {
		log.Fatalf("Invalid port number: %d. Must be between 1 and 65535", *port)
	}

	info, err := os.Stat(*dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(*dataDir, 0700); err != nil {
				log.Fatalf("Could not create data directory: %s", *dataDir)
			}
		} else {
			log.Fatalf("Failed to access data directory %s: %v", *dataDir, err)
		}
	} else {
		if !info.IsDir() {
			log.Fatalf("Data directory path is not a directory: %s", *dataDir)
		}
	}
	databaseFile := filepath.Join(*dataDir, databaseName)

	info, err = os.Stat(databaseFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Fatalf("Failed to access database file %s: %v", databaseFile, err)
		}
	} else {
		if !info.Mode().IsRegular() {
			log.Fatalf("Database file is not a regular file: %s", databaseFile)
		}
	}

	var authMiddleware func(http.Handler) http.Handler
	if *basicAuthFile != "" {
		// Strict: this file is mylinks' own, written by the operator for
		// mylinks alone, so a line it cannot parse is a mistake to report at
		// startup rather than a login to silently drop. No username validator,
		// mylinks puts the name nowhere but the credential check.
		htpasswd, err := auth.LoadHtpasswdStrict(*basicAuthFile, nil)
		if err != nil {
			log.Fatalf("load htpasswd: %v", err)
		}
		authMiddleware = htpasswd.Middleware(*basicAuthRealm)
		log.Printf("basic authentication enabled")
	}

	serverOrigin, err := csrf.ResolveServerOrigin(*publicURL, *addr, *port)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// Initialize database, once everything which can fail before it is done
	database, err := db.InitDB(databaseFile)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer func() {
		// Closing the database checkpoints and removes its WAL file
		if err := database.Close(); err != nil {
			log.Printf("Failed to close database: %v", err)
			exitCode = 1
		}
	}()

	// Initialize handlers
	mux := web.NewHandlers(executableDir, database, filepath.Join(*dataDir, screenshotsDir)).Routes()
	var root = csrf.Middleware(serverOrigin)(mux)

	if authMiddleware != nil {
		root = authMiddleware(root)
	}

	// Start server
	serverAddr := fmt.Sprintf("%s:%d", *addr, *port)
	log.Printf("Starting server on %s", serverAddr)
	server := http.Server{
		Addr:         serverAddr,
		Handler:      root,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  time.Minute,
	}

	// Shut down on SIGINT/SIGTERM, letting in-flight requests finish.
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serverError := make(chan error, 1)
	go func() {
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		log.Printf("Server error: %v", err)
		exitCode = 1
	case <-signalContext.Done():
		// A second signal should kill the process rather than be ignored.
		stopSignals()
		log.Printf("Shutting down")
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			// Requests may still hold database connections, in which case
			// closing the database leaves the WAL file behind.
			log.Printf("Failed to shut down server gracefully: %v", err)
			exitCode = 1
		}
	}

	return exitCode
}

func printVersion() {
	fmt.Println("Mylinks")
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	settings := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if vcs, ok := settings["vcs"]; ok {
		fmt.Printf("%s ", vcs)
	}
	modified := settings["vcs.modified"] == "true"
	if rev, ok := settings["vcs.revision"]; ok {
		if modified {
			fmt.Printf("revision: %s (dirty)\n", rev)
		} else {
			fmt.Printf("revision: %s\n", rev)
		}
	}
	if t, ok := settings["vcs.time"]; ok {
		if parsedTime, err := time.Parse(time.RFC3339, t); err == nil {
			fmt.Printf("updated at: %s\n", parsedTime.Local().Format("2006-01-02 15:04:05"))
		} else {
			fmt.Printf("updated at: %s\n", t)
		}
	}
}
