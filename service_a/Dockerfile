FROM golang:1.22.0-alpine
WORKDIR /app
COPY . .
RUN go mod tidy
RUN go build -o service_a .
EXPOSE 8080
CMD ["./service_a"]