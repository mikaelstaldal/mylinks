package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/mikaelstaldal/go-server-common/auth"
	"github.com/mikaelstaldal/go-server-common/csrf"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/web"
)

const databaseName = "mylinks.sqlite"
const screenshotsDir = "screenshots"

func main() {
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
		return
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

	// Initialize database
	database, err := db.InitDB(databaseFile)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	var authMiddleware func(http.Handler) http.Handler
	if *basicAuthFile != "" {
		htpasswd, err := auth.LoadHtpasswd(*basicAuthFile)
		if err != nil {
			log.Fatalf("load htpasswd: %v", err)
		}
		authMiddleware = htpasswd.Middleware(*basicAuthRealm)
		log.Printf("basic authentication enabled")
	}

	// Initialize handlers
	mux := web.NewHandlers(executableDir, database, filepath.Join(*dataDir, screenshotsDir)).Routes()

	serverOrigin, err := csrf.ResolveServerOrigin(*publicURL, *addr, *port)
	if err != nil {
		log.Fatalf("%v", err)
	}
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
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
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
