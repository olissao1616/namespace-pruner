FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o pruner ./cmd/pruner

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/pruner /pruner
ENTRYPOINT ["/pruner"]
