package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/51ddhesh/exchange-bench/internal/botworker"
	"github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"google.golang.org/grpc"
)

func main() {
	listen := flag.String("listen", ":9090", "host:port to serve gRPC on")
	workerID := flag.String("worker-id", hostname(), "human-readable worker name")
	seccomp := flag.String("seccomp", "deployments/docker/seccomp/contestant.json", "seccomp profile path")
	flag.Parse()

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: listen %s: %v\n", *listen, err)
		os.Exit(1)
	}

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(64 * 1024 * 1024),
	)
	proto.RegisterWorkerServiceServer(srv, botworker.NewWorkerServer(*workerID, *seccomp))

	fmt.Printf("[worker %s] listening on %s\n", *workerID, *listen)

	if err := srv.Serve(lis); err != nil {
		fmt.Fprintf(os.Stderr, "worker: serve: %v\n", err)
		os.Exit(1)
	}
}

func hostname() string {
	h, err := os.Hostname()

	if err != nil {
		return "unknown"
	}

	return h
}
