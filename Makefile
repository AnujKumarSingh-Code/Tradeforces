.PHONY: run sim test build gate

build:            ## build both binaries into ./bin
	go build -o bin/server ./cmd/server
	go build -o bin/sim ./cmd/sim

run: build        ## run the game on :8080
	./bin/server

test:             ## unit tests
	go test ./...

gate:             ## Phase 0 realism gate across 12 seeds
	go run ./cmd/sim -runs 12

coach:            ## scripted bad trader + the two-stage coach (needs ANTHROPIC_API_KEY for Claude)
	go run ./cmd/coachtest

sim:              ## one headless session with a CSV of mid and fair value
	go run ./cmd/sim -seed 42 -csv session.csv
