.PHONY: build test coverage clean

build:
	go build -o bin/console-server ./cmd/console-server

test: coverage

coverage:
	./scripts/coverage.sh

clean:
	rm -rf bin coverage.out
