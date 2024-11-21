# Stage 1 - Build the Go app
FROM golang:1.23.3-alpine AS build

# Set environment variables for a static build
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go.mod and go.sum and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code and build the binary
COPY . .
RUN go build -ldflags="-w -s" -o /app/book-to-kindle-bot

# Stage 2 - Create minimal runtime image
FROM alpine:3.18

# Create a non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Copy the binary from the build stage and set permissions
COPY --from=build /app/book-to-kindle-bot /app/book-to-kindle-bot
COPY --from=build /app/.env /app/.env
RUN chmod +x /app/book-to-kindle-bot && chown -R appuser:appgroup /app

# Set the working directory and switch to non-root user
WORKDIR /app
USER appuser

EXPOSE 8080

# Command to run the executable
CMD ["/app/book-to-kindle-bot"]