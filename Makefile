# This box is memory-starved (swap is usually full), which makes the Go
# compiler segfault. GOMEMLIMIT keeps the compiler inside its budget.
GOMEMLIMIT ?= 700MiB
BIN        ?= telegram-bot

.PHONY: all build run test vet fmt clean

all: build

build:
	CGO_ENABLED=0 GOMEMLIMIT=$(GOMEMLIMIT) go build -o $(BIN) .

# use `run`, not `go run` - go run does not inherit the memlimit below
run:
	CGO_ENABLED=0 GOMEMLIMIT=$(GOMEMLIMIT) go run .

test:
	CGO_ENABLED=0 GOMEMLIMIT=$(GOMEMLIMIT) go test ./...

vet:
	CGO_ENABLED=0 GOMEMLIMIT=$(GOMEMLIMIT) go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BIN)
