FROM golang:1.23-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agent ./cmd/agent

FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=builder /out/agent /app/agent
COPY configs /app/configs

USER nonroot:nonroot
ENTRYPOINT ["/app/agent"]
CMD ["--config", "/app/configs/config.example.yaml"]
