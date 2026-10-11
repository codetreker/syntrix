.PHONY: build test coverage clean

build:
	go build -o bin/console-server ./cmd/console-server

test:
	go test -race ./... -count=1 -timeout=3m

coverage:
	go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 --coverprofile=coverage.out --skip-result-packages cmd/

clean:
	rm -rf bin coverage.out
