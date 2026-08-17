# Use Go 1.26.6 as the base image for building. The official images set
# GOTOOLCHAIN=local, so this has to be at least the version go.mod asks for.
FROM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS builder

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the Go source code
COPY cmd/ cmd
COPY empty-efs.go ui/efs.go
COPY empty/ ui/

# Build the application
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
