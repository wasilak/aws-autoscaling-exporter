# build stage
FROM quay.io/wasilak/golang:1.17-alpine as builder

ADD . /app
WORKDIR /app
RUN go build -o /aws-autoscaling-exporter

FROM quay.io/wasilak/alpine:3

COPY --from=builder /aws-autoscaling-exporter /aws-autoscaling-exporter
ENTRYPOINT ["/aws-autoscaling-exporter"]
