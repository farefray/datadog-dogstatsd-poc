BINARY := bin/emitter

.PHONY: build clean run burst stream

build:
	GOOS=$${GOOS:-linux} GOARCH=$${GOARCH:-amd64} go build -o $(BINARY) ./cmd/emitter

clean:
	rm -f $(BINARY)

run:
	go run ./cmd/emitter --mode=burst --iterations=1

burst: run

stream:
	go run ./cmd/emitter --mode=stream --interval=5s --iterations=0
