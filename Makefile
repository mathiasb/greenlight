include .env.local
# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo "Are you sure? [y/N] " && read ans && [ $${ans:-N} = y ]

# ==================================================================================== #
# DEVELOPMENT
# ==================================================================================== #

## run/api: run the cmd/api application
.PHONY: run/api
run/api:
	go run ./cmd/api -db-dsn=${GREENLIGHT_DB_DSN}

## db/psql: connect to the database with psql
.PHONY: db/psql
db/psql:
	psql ${GREENLIGHT_DB_DSN}

## db/migrations/new name=$1: create a new database migration script
.PHONY: db/migrations/new
db/migrations/new:
	@echo "Make migration files for ${name}"
	migrate create -seq -ext .sql -dir ./migrations ${name}

##db/migrations/up: apply all up database migrations
.PHONY: db/migrations/up
db/migrations/up: confirm
	@echo "Running database migrations..."
	migrate -path ./migrations -database ${GREENLIGHT_DB_DSN} up

# ==================================================================================== #
# QUALITY CONTROL
# ==================================================================================== #

## tidy: format all .go files and tidy and vendor all module dependencies
.PHONY: tidy
tidy:
	@echo "Formatting .go files ..."
	go fmt ./...
	@echo "Tidying module dependencies ..."
	go mod tidy
	@echo "Verifying and vendoring module dependencies"
	go mod verify
	go mod vendor

## audit: tidy dependencies and format, vet, and test all code
.PHONY: audit
audit: tidy
	@echo 'Formatting code...'
	go fmt ./...
	@echo 'Vetting code...'
	go vet ./...
	staticcheck ./...
	@echo 'Running tests...'
	go test -race -vet=off ./...

# ==================================================================================== #
# BUILD
# ==================================================================================== #

## build/api:
.PHONY: build/api
build/api:
	@echo "Building cmd/api ..."
	go build -ldflags='-s' -o=./bin/api ./cmd/api
	GOOS=linux GOARCH=amd64 go build -ldflags='-s' -o=./bin/linux_amd64/api ./cmd/api

## build/apiv version=$1:
.PHONY: build/apiv
build/apiv:
	@echo "Building cmd/api ..."
	go build -ldflags='-s -X main.version=${version}' -o=./bin/api ./cmd/api
	GOOS=linux GOARCH=amd64 go build -ldflags='-s -X main.version=${version}' -o=./bin/linux_amd64/api ./cmd/api