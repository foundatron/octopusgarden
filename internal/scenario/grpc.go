package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jhump/protoreflect/v2/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const defaultGRPCTimeout = 30 * time.Second

var (
	errGRPCMissingService   = errors.New("grpc step missing required field: service")
	errGRPCMissingMethod    = errors.New("grpc step missing required field: method")
	errGRPCNotAService      = errors.New("resolved name is not a service")
	errGRPCMethodNotFound   = errors.New("method not found in service")
	errGRPCClientStreamStub = errors.New("grpc: client-streaming not yet implemented")
	errGRPCServerStreamStub = errors.New("grpc: server-streaming not yet implemented")
)

// GRPCExecutor executes gRPC steps using server reflection for dynamic invocation.
// A single connection persists across steps within a scenario.
// GRPCExecutor is NOT safe for concurrent use from multiple goroutines.
type GRPCExecutor struct {
	Target string
	Logger *slog.Logger

	conn *grpc.ClientConn
}

// ValidCaptureSources returns the valid capture source names for gRPC steps.
func (e *GRPCExecutor) ValidCaptureSources() []string {
	return []string{GRPCSourceStatus, GRPCSourceHeaders}
}

// Execute dispatches a gRPC call and returns the step output.
func (e *GRPCExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteGRPCRequest(*step.GRPC, vars)

	if err := validateGRPCRequest(req); err != nil {
		return StepOutput{}, err
	}

	timeout, err := parseGRPCTimeout(req.Timeout)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: parse timeout: %w", err)
	}

	if err := e.ensureConnection(); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: connect: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Add metadata/headers.
	if len(req.Headers) > 0 {
		md := metadata.New(req.Headers)
		ctx = metadata.NewOutgoingContext(ctx, md)
	}

	// Resolve method via server reflection.
	methodDesc, err := e.resolveMethod(ctx, req.Service, req.Method)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: resolve method: %w", err)
	}

	// Determine call type.
	if req.Stream != nil && len(req.Stream.Messages) > 0 {
		return e.executeClientStream(ctx, methodDesc, req)
	}
	if req.Stream != nil && req.Stream.Receive != nil {
		return e.executeServerStream(ctx, methodDesc, req)
	}

	return e.executeUnary(ctx, methodDesc, req)
}

// Close releases the gRPC connection. Safe to call on a never-initialized executor.
func (e *GRPCExecutor) Close() {
	if e.conn != nil {
		_ = e.conn.Close()
		e.conn = nil
	}
}

func (e *GRPCExecutor) ensureConnection() error {
	if e.conn != nil {
		return nil
	}
	conn, err := grpc.NewClient(e.Target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial %s: %w", e.Target, err)
	}
	e.conn = conn
	return nil
}

func (e *GRPCExecutor) resolveMethod(ctx context.Context, service, method string) (protoreflect.MethodDescriptor, error) {
	refClient := grpcreflect.NewClientAuto(ctx, e.conn)
	defer refClient.Reset()

	// Use the reflection client as a resolver to find the service descriptor.
	resolver := refClient.AsResolver()
	desc, err := resolver.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, fmt.Errorf("resolve service %q: %w", service, err)
	}

	svcDesc, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s: %w", service, errGRPCNotAService)
	}

	methodDesc := svcDesc.Methods().ByName(protoreflect.Name(method))
	if methodDesc == nil {
		return nil, fmt.Errorf("%s/%s: %w", service, method, errGRPCMethodNotFound)
	}
	return methodDesc, nil
}

func (e *GRPCExecutor) executeUnary(ctx context.Context, methodDesc protoreflect.MethodDescriptor, req GRPCRequest) (StepOutput, error) {
	inputMsg := dynamicpb.NewMessage(methodDesc.Input())
	if req.Body != "" {
		if err := protojson.Unmarshal([]byte(req.Body), inputMsg); err != nil {
			return StepOutput{}, fmt.Errorf("grpc: unmarshal request body: %w", err)
		}
	}

	outputMsg := dynamicpb.NewMessage(methodDesc.Output())
	fullMethod := fmt.Sprintf("/%s/%s", methodDesc.Parent().(protoreflect.ServiceDescriptor).FullName(), methodDesc.Name())

	var headerMD metadata.MD
	err := e.conn.Invoke(ctx, fullMethod, inputMsg, outputMsg, grpc.Header(&headerMD))

	st := status.Convert(err)
	respJSON, _ := protojson.Marshal(outputMsg)

	return buildGRPCOutput(req.Service, req.Method, "", st, headerMD, string(respJSON)), nil
}

func buildGRPCOutput(service, method, streamInfo string, st *status.Status, headerMD metadata.MD, respJSON string) StepOutput {
	var observed strings.Builder
	if streamInfo != "" {
		fmt.Fprintf(&observed, "gRPC %s/%s (%s)\n", service, method, streamInfo)
	} else {
		fmt.Fprintf(&observed, "gRPC %s/%s\n", service, method)
	}
	fmt.Fprintf(&observed, "Status: %s\n", st.Code().String())
	if st.Message() != "" {
		fmt.Fprintf(&observed, "Message: %s\n", st.Message())
	}
	fmt.Fprintf(&observed, "Response: %s", respJSON)

	headersJSON, _ := json.Marshal(headerMD)

	return StepOutput{
		Observed:    observed.String(),
		CaptureBody: respJSON,
		CaptureSources: map[string]string{
			GRPCSourceStatus:  st.Code().String(),
			GRPCSourceHeaders: string(headersJSON),
		},
	}
}

func validateGRPCRequest(req GRPCRequest) error {
	if req.Service == "" {
		return errGRPCMissingService
	}
	if req.Method == "" {
		return errGRPCMissingMethod
	}
	return nil
}

func parseGRPCTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultGRPCTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

func substituteGRPCRequest(req GRPCRequest, vars map[string]string) GRPCRequest {
	out := GRPCRequest{
		Service: substituteVars(req.Service, vars),
		Method:  substituteVars(req.Method, vars),
		Body:    substituteVarsJSON(req.Body, vars),
		Headers: make(map[string]string, len(req.Headers)),
		Timeout: req.Timeout,
		Stream:  req.Stream,
	}
	for k, v := range req.Headers {
		out.Headers[k] = substituteVars(v, vars)
	}
	// Substitute vars in stream messages if present.
	if req.Stream != nil && len(req.Stream.Messages) > 0 {
		msgs := make([]string, len(req.Stream.Messages))
		for i, m := range req.Stream.Messages {
			msgs[i] = substituteVarsJSON(m, vars)
		}
		streamCopy := *req.Stream
		streamCopy.Messages = msgs
		out.Stream = &streamCopy
	}
	return out
}

// Stub methods for streaming — will be implemented in Phase 3 and 4.

func (e *GRPCExecutor) executeClientStream(_ context.Context, _ protoreflect.MethodDescriptor, _ GRPCRequest) (StepOutput, error) {
	return StepOutput{}, errGRPCClientStreamStub
}

func (e *GRPCExecutor) executeServerStream(_ context.Context, _ protoreflect.MethodDescriptor, _ GRPCRequest) (StepOutput, error) {
	return StepOutput{}, errGRPCServerStreamStub
}
