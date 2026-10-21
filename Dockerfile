# Use Go 1.26.10 as the base image for building. The official images set
# GOTOOLCHAIN=local, so this has to be at least the version go.mod asks for.
FROM golang:1.26.10@sha256:3745f1aa11822297b79113f5e84ce2bffeb039734571c39855809cfe9e5d7be6 AS builder

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
