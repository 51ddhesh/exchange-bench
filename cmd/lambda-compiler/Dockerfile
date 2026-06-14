FROM golang:1.26.3-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /lambda-compiler ./cmd/lambda-compiler

FROM public.ecr.aws/lambda/provided:al2

# Install compilers (Rust, GCC, Go, Python)
# Amazon Linux 2 uses yum
USER root
RUN yum update -y && \
    yum install -y gcc gcc-c++ python3 tar gzip curl cargo rust && \
    yum clean all

ENV PATH="/usr/local/go/bin:${PATH}"

COPY --from=builder /usr/local/go /usr/local/go

COPY --from=builder /lambda-compiler /var/runtime/bootstrap

# The lambda environment needs to find the bootstrap
ENTRYPOINT [ "/var/runtime/bootstrap" ]
