# Stage 1 - Build the Go app
FROM golang:1.22.4-alpine AS build

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go.mod and go.sum and download dependencies
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Set environment variables for a static build
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

# Copy the source code and build the binary
COPY . .
RUN go build -ldflags="-w -s -extldflags '-static'" -o book-to-kindle-bot

# Stage 2 - Create minimal runtime image
FROM alpine:3.18

# Create a non-root user for security reasons
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Copy the binary from the build stage and set permissions
COPY --from=build /app/book-to-kindle-bot /app/book-to-kindle-bot
COPY --from=build /app/.env /app/.env
RUN chown -R appuser:appgroup /app

# Set the working directory and switch to non-root user
WORKDIR /app
USER appuser

# Command to run the executable
CMD ["/app/book-to-kindle-bot"]