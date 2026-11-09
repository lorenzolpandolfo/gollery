# Gollery

Lightweight self-hosted photo and media gallery web server built with Go and HTMX.

## Features

- Fast and dependency-free backend (Go standard library)
- Interactive and responsive UI with HTMX
- Photo and video management with albums and search
- Trash bin with automatic retention cleanup
- Optimized for low-resource environments (ARM32, ARM64, x86)

## Getting Started

Run directly during development:

```bash
go run main.go
```

The web interface will be available at `http://localhost:8092`.

## Build

### Multi-Architecture Build (ARM32, ARM64, x86)

Build Linux binaries for ARM32 (ARMv7), ARM64, and x86_64 with a single command:

```bash
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-arm32 . && \
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-arm64 . && \
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-x86 .
```

### Single Build (Host Architecture)

```bash
go build -trimpath -ldflags="-s -w" -o gollery .
```

### Android (ARM64)

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gollery-android-arm64 .
```

## Running

Run the binary matching your target platform:

```bash
./gollery-x86
# or ./gollery-arm64 / ./gollery-arm32
```

## Storage

- `media/`: Uploaded photos and videos
- `data.json`: Application metadata and state
- `.trash/`: Soft-deleted items pending cleanup
