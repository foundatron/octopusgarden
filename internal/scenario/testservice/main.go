// Package main implements a minimal HTTP+gRPC test service used by integration tests.
// HTTP endpoints: GET /healthz, GET /echo?msg=..., POST /echo {message: string}
// gRPC service echo.EchoService: Echo (unary), StreamEcho (server-streaming), CollectEcho (client-streaming)
// gRPC server reflection is enabled so the GRPCExecutor can resolve methods dynamically.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func main() {
	fd, err := buildEchoFileDescriptor()
	if err != nil {
		log.Fatalf("build file descriptor: %v", err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		log.Fatalf("register file descriptor: %v", err)
	}

	grpcLis, err := net.Listen("tcp", ":9090") //nolint:gosec,noctx // G102: test service binds to all interfaces; context-free listener is fine for a test server
	if err != nil {
		log.Fatalf("gRPC listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	grpcSrv.RegisterService(buildEchoServiceDesc(fd), nil)
	reflection.Register(grpcSrv)

	go func() {
		slog.Info("gRPC server listening", "addr", ":9090")
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC serve: %v", err)
		}
	}()

	mux := http.NewServeMux()
	registerHTTPHandlers(mux)

	slog.Info("HTTP server listening", "addr", ":8080")
	if err := http.ListenAndServe(":8080", mux); err != nil { //nolint:gosec // test service with fixed port
		log.Fatalf("HTTP serve: %v", err)
	}
}

// buildEchoFileDescriptor constructs the proto FileDescriptor for the echo service programmatically,
// avoiding the need for a protoc code-generation step.
func buildEchoFileDescriptor() (protoreflect.FileDescriptor, error) {
	strType := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	int32Type := descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
	labelOptional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	labelRepeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("echo.proto"),
		Package: proto.String("echo"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("EchoRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("message"), Number: proto.Int32(1), Type: strType, Label: labelOptional},
				},
			},
			{
				Name: proto.String("EchoResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("message"), Number: proto.Int32(1), Type: strType, Label: labelOptional},
					{Name: proto.String("timestamp"), Number: proto.Int32(2), Type: strType, Label: labelOptional},
				},
			},
			{
				Name: proto.String("StreamRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("message"), Number: proto.Int32(1), Type: strType, Label: labelOptional},
					{Name: proto.String("count"), Number: proto.Int32(2), Type: int32Type, Label: labelOptional},
				},
			},
			{
				Name: proto.String("CollectResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("messages"), Number: proto.Int32(1), Type: strType, Label: labelRepeated},
					{Name: proto.String("count"), Number: proto.Int32(2), Type: int32Type, Label: labelOptional},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("EchoService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Echo"),
						InputType:  proto.String(".echo.EchoRequest"),
						OutputType: proto.String(".echo.EchoResponse"),
					},
					{
						Name:            proto.String("StreamEcho"),
						InputType:       proto.String(".echo.StreamRequest"),
						OutputType:      proto.String(".echo.EchoResponse"),
						ServerStreaming: proto.Bool(true),
					},
					{
						Name:            proto.String("CollectEcho"),
						InputType:       proto.String(".echo.EchoRequest"),
						OutputType:      proto.String(".echo.CollectResponse"),
						ClientStreaming: proto.Bool(true),
					},
				},
			},
		},
	}
	return protodesc.NewFile(fdp, nil)
}

func msgDesc(fd protoreflect.FileDescriptor, name string) protoreflect.MessageDescriptor {
	return fd.Messages().ByName(protoreflect.Name(name))
}

// buildEchoServiceDesc returns a *grpc.ServiceDesc with dynamic proto handlers.
// HandlerType is a nil *any pointer; gRPC skips type checking when the
// server implementation (ss) passed to RegisterService is also nil.
func buildEchoServiceDesc(fd protoreflect.FileDescriptor) *grpc.ServiceDesc {
	echoReqDesc := msgDesc(fd, "EchoRequest")
	echoRespDesc := msgDesc(fd, "EchoResponse")
	streamReqDesc := msgDesc(fd, "StreamRequest")
	collectRespDesc := msgDesc(fd, "CollectResponse")

	return &grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Echo",
				Handler:    makeUnaryEchoHandler(echoReqDesc, echoRespDesc),
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "StreamEcho",
				Handler:       makeServerStreamHandler(streamReqDesc, echoRespDesc),
				ServerStreams: true,
			},
			{
				StreamName:    "CollectEcho",
				Handler:       makeClientStreamHandler(echoReqDesc, collectRespDesc),
				ClientStreams: true,
			},
		},
		Metadata: "echo.proto",
	}
}

// makeUnaryEchoHandler returns a gRPC unary handler for the Echo method.
// The handler signature matches grpc.MethodHandler exactly.
func makeUnaryEchoHandler(reqDesc, respDesc protoreflect.MessageDescriptor) func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		req := dynamicpb.NewMessage(reqDesc)
		if err := dec(req); err != nil {
			return nil, status.Errorf(codes.Internal, "decode: %v", err)
		}

		msg := req.Get(reqDesc.Fields().ByName("message")).String()

		resp := dynamicpb.NewMessage(respDesc)
		resp.Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString("echo: "+msg))
		resp.Set(respDesc.Fields().ByName("timestamp"), protoreflect.ValueOfString(time.Now().UTC().Format(time.RFC3339)))
		return resp, nil
	}
}

// makeServerStreamHandler returns a gRPC handler for the StreamEcho method.
// The client sends one StreamRequest; the server streams count EchoResponse messages.
func makeServerStreamHandler(reqDesc, respDesc protoreflect.MessageDescriptor) func(srv any, stream grpc.ServerStream) error {
	return func(_ any, stream grpc.ServerStream) error {
		req := dynamicpb.NewMessage(reqDesc)
		if err := stream.RecvMsg(req); err != nil {
			return status.Errorf(codes.Internal, "recv request: %v", err)
		}

		msg := req.Get(reqDesc.Fields().ByName("message")).String()
		count := int(req.Get(reqDesc.Fields().ByName("count")).Int())
		if count <= 0 {
			count = 1
		}

		msgField := respDesc.Fields().ByName("message")
		tsField := respDesc.Fields().ByName("timestamp")

		for i := 0; i < count; i++ {
			resp := dynamicpb.NewMessage(respDesc)
			resp.Set(msgField, protoreflect.ValueOfString(fmt.Sprintf("%s [%d/%d]", msg, i+1, count)))
			resp.Set(tsField, protoreflect.ValueOfString(time.Now().UTC().Format(time.RFC3339)))
			if err := stream.SendMsg(resp); err != nil {
				return err
			}
		}
		return nil
	}
}

// makeClientStreamHandler returns a gRPC handler for the CollectEcho method.
// The client streams EchoRequest messages; the server replies with one CollectResponse.
func makeClientStreamHandler(reqDesc, respDesc protoreflect.MessageDescriptor) func(srv any, stream grpc.ServerStream) error {
	return func(_ any, stream grpc.ServerStream) error {
		msgField := reqDesc.Fields().ByName("message")
		var msgs []string

		for {
			req := dynamicpb.NewMessage(reqDesc)
			if err := stream.RecvMsg(req); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
			msgs = append(msgs, req.Get(msgField).String())
		}

		resp := dynamicpb.NewMessage(respDesc)
		list := resp.Mutable(respDesc.Fields().ByName("messages")).List()
		for _, m := range msgs {
			list.Append(protoreflect.ValueOfString(m))
		}
		resp.Set(respDesc.Fields().ByName("count"), protoreflect.ValueOfInt32(int32(len(msgs)))) //nolint:gosec // G115: len(msgs) bounded by message count, safe conversion
		return stream.SendMsg(resp)
	}
}

func registerHTTPHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/echo", handleEcho)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"status":"ok"}`)
}

func handleEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var msg string
	switch r.Method {
	case http.MethodGet:
		msg = r.URL.Query().Get("msg")
	case http.MethodPost:
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		msg = body.Message
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	resp := map[string]string{
		"message":   msg,
		"method":    r.Method,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if h := r.Header.Get("X-Test-Header"); h != "" {
		resp["echo_header"] = h
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encode response", "error", err)
	}
}
