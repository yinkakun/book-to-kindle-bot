# Stage 1 - Build the app
FROM golang:1.23.3-alpine AS builder
WORKDIR /app
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-w -s" -o book-to-kindle-bot

# Stage 2 - Create minimal runtime image
FROM alpine:3.18
RUN addgroup -S appgroup && \
  adduser -S appuser -G appgroup && \
  mkdir -p /app/data && \
  chown -R appuser:appgroup /app

WORKDIR /app
COPY --from=builder /app/book-to-kindle-bot .
COPY --from=builder /app/.env .

USER appuser
EXPOSE 8080
CMD ["./book-to-kindle-bot"]