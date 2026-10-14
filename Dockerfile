# Use Go 1.26.9 as the base image for building. The official images set
# GOTOOLCHAIN=local, so this has to be at least the version go.mod asks for.
FROM golang:1.26.9@sha256:d7722066f0b60ceccb6c0643cbed1f5f9e15506ac237146c95333504d7805d89 AS builder

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the Go source code
COPY cmd/ cmd
COPY internal/ internal
COPY openapi.yaml api_document.go ./
COPY empty-efs.go ui/efs.go
COPY empty/ ui/

# Build the application
RUN go install "github.com/ogen-go/ogen/cmd/ogen@$(go list -m -f '{{.Version}}' github.com/ogen-go/ogen)"
RUN go generate ./...
RUN go build -v -o /app/mylinks ./cmd/mylinks

FROM chromedp/headless-shell@sha256:24b6acd183756b9cdc9b2c951141cefbc645a9b6a18341975babf0911a30c7e5

ENV CHROMEDP="wss://localhost:9222"

WORKDIR /

COPY --from=builder /app/mylinks /mylinks/mylinks
COPY run.sh /mylinks/run.sh
COPY ui/ /mylinks/ui

# Expose the default port
EXPOSE 8080

ENTRYPOINT [ "/mylinks/run.sh" ]
