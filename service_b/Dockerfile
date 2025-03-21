FROM golang:1.22.0-alpine
WORKDIR /app
COPY . .
RUN go mod tidy
RUN go build -o service_b .
EXPOSE 8081
CMD ["./service_b"]