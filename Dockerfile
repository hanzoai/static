FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /static ./cmd/static

FROM scratch
COPY --from=build /static /static
ENTRYPOINT ["/static"]
