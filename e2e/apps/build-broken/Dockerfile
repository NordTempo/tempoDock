FROM golang:1.25-alpine
WORKDIR /src
COPY . .
RUN go build -o /app .
CMD ["/app"]
