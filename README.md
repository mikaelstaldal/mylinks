# MyLinks

A simple and efficient web application for saving and managing your favorite links. 
MyLinks automatically extracts page title, description and optionally screenshots from URLs and 
provides a web interface for organizing your bookmarks.

You can also save short notes which are not connected to any particular URL along with your bookmarks.

## Clients

In addition to the built-in web interface, there is also 

* A [native Android app](https://github.com/mikaelstaldal/mylinks-android)
* A [desktop application](https://github.com/mikaelstaldal/mylinks-desktop)

## Features

- **Save Links**: Add URLs with automatic title and description extraction and screenshots from web pages
- **Save Nodes**: Write short notes and save them among your bookmarks.
- **SQLite Storage**: Lightweight, file-based database with no external dependencies
- **Docker Support**: Easy deployment with Docker containers
- **HTTP basic auth**: Protect the application with username and password

## Run with Docker (with screenshot support)

1. Build the image:
   ```bash
   docker build -t mylinks .
   ```
2. Run the container without authentication, listing on localhost only:
   ```bash
   docker run --mount "type=bind,src=$(pwd)/data,dst=/data" --cap-drop ALL --security-opt no-new-privileges -p 127.0.0.1:8080:8080 mylinks
   ```
3. Run the container with HTTP basic authentication, listing externally:
   ```bash
   htpasswd -cBC 12 pwfile my_username
   docker run --mount "type=bind,src=$(pwd)/pwfile,dst=/pwfile" --mount "type=bind,src=$(pwd)/data,dst=/data" --cap-drop ALL --security-opt no-new-privileges -p 8080:8080 mylinks -basic-auth-file /pwfile -basic-auth-realm my-realm
   ```
Note: This is only secure if you also use https.   

The application will store data in the directory mounted at `/data`, using `data/mylinks.sqlite` as the database file 
and store screenshots in `data/screenshots`. 


## Run without Docker (without screenshot support)

1. Build the standalone executable
   ```bash
   go build -tags netgo -v ./cmd/mylinks/
   ```
2. Run it without authentication, listing on localhost only:
   ```bash
   ./mylinks -port 8080 -addr 127.0.0.1 -data data
   ```  
3. Run it with HTTP basic authentication, listing externally:
   ```bash
   htpasswd -cBC 12 pwfile my_username
   ./mylinks -port 8080 -data data -basic-auth-file pwfile -basic-auth-realm my-realm
   ```  
Note: This is only secure if you also use https.   

The application will store data in the `./data` directory, using `./data/mylinks.sqlite` as the database file.

You can use the `apparmor-profile` file as a template for an Apparmor profile, you need to substitute 
`${PATH_TO_EXECUTABLE}` and `${PATH_TO_DATA}` with absolute paths. 
This has only been tested on Ubuntu and Debian Linux.

## Web Interface

Once the application is running, open your web browser and navigate to:
- `http://localhost:8080` (or your configured port)

From the web interface, you can:
1. **Add a new link**: Enter a URL in the input field and click "Add Link"
2. **Add a new note**: Enter title and text in the input fields and click "Add Note"
3. **View all links**: All saved links/notes are displayed on the main page
4. **Delete a link**: Click the delete button next to any link/note to remove it

## Development

### Project Structure

```
├── cmd/mylinks/          # Main application
│   ├── main.go             # Application entry point
│   ├── handlers.go         # HTTP request handlers
│   ├── handlers_test.go    # Handler tests
│   └── db/                 # Database layer
│       ├── db.go           # Database operations
│       └── db_test.go      # Database tests
├── ui/                     # User interface assets
│   ├── templates/          # HTML templates
│   └── static/             # CSS, JavaScript files
├── Dockerfile              # Docker configuration
├── run.sh                  # Start script for Docker image
├── go.mod                  # Go module definition
├── apparmor-profile        # Apparmor profile template
└── README.md               # This file
```

### Running Tests

To run all tests:
```bash
go test ./...
```

## API Endpoints

The application provides the following HTTP endpoints:

- `GET /` - Display all saved links
- `GET /?s=term` - Search for links
- `POST /` - Add a new link/note
- `GET /{id}` - Get a specific link
- `PATCH /{id}` - Edit a specific link
- `DELETE /{id}` - Delete a specific link

## Dependencies

- **Go**: at least the version in the `go` directive of `go.mod`
- **modernc.org/sqlite**: Pure Go SQLite driver
- **htmx**: High power tools for HTML
- **_hyperscript**: An easy & approachable language for modern web front-ends
- **missing.css**: The Missing CSS Stylesheet
- **chromedp**: Run headless Chrome browser to fetch page, extract title, description and take screenshot

## License

Copyright 2025-2026 Mikael Ståldal.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
