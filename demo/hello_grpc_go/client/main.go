package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	pb "github.com/whale-net/everything/demo/hello_grpc_go/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	addr   = flag.String("addr", "localhost:50051", "the address to connect to")
	name   = flag.String("name", "World", "name to greet")
	stream = flag.Bool("stream", false, "use streaming RPC")
)

func main() {
	flag.Parse()

	// Set up a connection to the server
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	c := pb.NewGreeterClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if *stream {
		// Use streaming RPC
		streamClient, err := c.SayHelloStream(ctx, &pb.HelloRequest{Name: *name})
		if err != nil {
			log.Fatalf("Failed to call SayHelloStream: %v", err)
		}

		fmt.Println("Receiving streaming responses:")
		for {
			resp, err := streamClient.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Fatalf("Failed to receive: %v", err)
			}
			fmt.Printf("  [%d] %s\n", resp.Count, resp.Message)
		}
	} else {
		// Use unary RPC
		resp, err := c.SayHello(ctx, &pb.HelloRequest{Name: *name})
		if err != nil {
			log.Fatalf("Failed to call SayHello: %v", err)
		}
		fmt.Printf("Response: %s\n", resp.Message)
	}
}
