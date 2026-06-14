package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"context"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"log"

	"github.com/51ddhesh/exchange-bench/internal/botworker"
	"github.com/51ddhesh/exchange-bench/internal/coordinator/proto"
	"google.golang.org/grpc"
)

func main() {
	listen := flag.String("listen", ":9090", "host:port to serve gRPC on")
	workerID := flag.String("worker-id", hostname(), "human-readable worker name")
	seccomp := flag.String("seccomp", "deployments/docker/seccomp/contestant.json", "seccomp profile path")
	localMode := flag.Bool("local", false, "Local testing mode (bypass AWS S3)")
	flag.Parse()

	var s3Client *s3.Client
	if !*localMode {
		awsCfg, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			log.Fatalf("worker: failed to load AWS config: %v", err)
		}
		s3Client = s3.NewFromConfig(awsCfg)
	}

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: listen %s: %v\n", *listen, err)
		os.Exit(1)
	}

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(64 * 1024 * 1024),
	)
	proto.RegisterWorkerServiceServer(srv, botworker.NewWorkerServer(*workerID, *seccomp, s3Client))

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
