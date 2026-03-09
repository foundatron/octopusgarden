package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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

// protoMarshaler emits zero-valued fields so the judge sees complete data
// (e.g. {"count":"0","min":0} instead of {}).
var protoMarshaler = protojson.MarshalOptions{EmitDefaultValues: true}

var (
	_ StepExecutor = (*GRPCExecutor)(nil)

	errGRPCMissingService  = errors.New("grpc step missing required field: service")
	errGRPCMissingMethod   = errors.New("grpc step missing required field: method")
	errGRPCNotAService     = errors.New("resolved name is not a service")
	errGRPCMethodNotFound  = errors.New("method not found in service")
	errGRPCStreamNotFound  = errors.New("background stream not found")
	errGRPCNoParentService = errors.New("method descriptor has no parent service")
)

// fullMethodPath returns the full gRPC method path (e.g. "/pkg.Service/Method")
// from a method descriptor, with a guarded type assertion on the parent.
func fullMethodPath(methodDesc protoreflect.MethodDescriptor) (string, error) {
	parent := methodDesc.Parent()
	svcDesc, ok := parent.(protoreflect.ServiceDescriptor)
	if !ok {
		return "", fmt.Errorf("method %s: %w", methodDesc.Name(), errGRPCNoParentService)
	}
	return fmt.Sprintf("/%s/%s", svcDesc.FullName(), methodDesc.Name()), nil
}

// GRPCExecutor executes gRPC steps using server reflection for dynamic invocation.
// A single connection persists across steps within a scenario.
// GRPCExecutor is NOT safe for concurrent use from multiple goroutines.
type GRPCExecutor struct {
	Target string
	Logger *slog.Logger

	conn      *grpc.ClientConn
	bgStreams map[string]*backgroundStream
	bgCounter int // auto-increment for unnamed background streams
	bgCancel  context.CancelFunc
	bgCtx     context.Context //nolint:containedctx // background streams need a parent context for cleanup
}

// backgroundStream buffers messages received from a server-streaming RPC in a goroutine.
type backgroundStream struct {
	mu       sync.Mutex
	messages []string
	err      error
	done     chan struct{}
}

// ValidCaptureSources returns the valid capture source names for gRPC steps.
func (e *GRPCExecutor) ValidCaptureSources() []string {
	return []string{GRPCSourceStatus, GRPCSourceHeaders}
}

// Execute dispatches a gRPC call and returns the step output.
func (e *GRPCExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteGRPCRequest(*step.GRPC, vars)

	// Collect from a named background stream (no new RPC needed).
	if req.Stream != nil && req.Stream.ID != "" && req.Service == "" {
		return e.collectBackground(req)
	}

	if err := validateGRPCRequest(req); err != nil {
		return StepOutput{}, err
	}

	timeout, err := parseStepTimeout(req.Timeout, defaultGRPCTimeout)
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

// Close releases the gRPC connection and cancels all background streams.
// Safe to call on a never-initialized executor.
func (e *GRPCExecutor) Close() {
	if e.bgCancel != nil {
		e.bgCancel()
	}
	// Wait for all background streams to finish.
	for _, bg := range e.bgStreams {
		<-bg.done
	}
	e.bgStreams = nil
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
	fullMethod, err := fullMethodPath(methodDesc)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: %w", err)
	}

	var headerMD metadata.MD
	err = e.conn.Invoke(ctx, fullMethod, inputMsg, outputMsg, grpc.Header(&headerMD))

	st := status.Convert(err)
	respJSON, _ := protoMarshaler.Marshal(outputMsg)

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

func (e *GRPCExecutor) executeClientStream(ctx context.Context, methodDesc protoreflect.MethodDescriptor, req GRPCRequest) (StepOutput, error) {
	fullMethod, err := fullMethodPath(methodDesc)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: %w", err)
	}

	streamDesc := &grpc.StreamDesc{
		StreamName:    string(methodDesc.Name()),
		ClientStreams: true,
	}

	var headerMD metadata.MD
	stream, err := e.conn.NewStream(ctx, streamDesc, fullMethod, grpc.Header(&headerMD))
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: open client stream: %w", err)
	}

	for i, msgJSON := range req.Stream.Messages {
		inputMsg := dynamicpb.NewMessage(methodDesc.Input())
		if err := protojson.Unmarshal([]byte(msgJSON), inputMsg); err != nil {
			return StepOutput{}, fmt.Errorf("grpc: unmarshal stream message %d: %w", i, err)
		}
		if err := stream.SendMsg(inputMsg); err != nil {
			return StepOutput{}, fmt.Errorf("grpc: send stream message %d: %w", i, err)
		}
	}

	// Close send side and receive the response.
	outputMsg := dynamicpb.NewMessage(methodDesc.Output())
	if err := stream.CloseSend(); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: close send: %w", err)
	}
	recvErr := stream.RecvMsg(outputMsg)

	st := status.Convert(recvErr)
	respJSON, _ := protoMarshaler.Marshal(outputMsg)
	streamInfo := fmt.Sprintf("client-streaming, sent %d messages", len(req.Stream.Messages))

	return buildGRPCOutput(req.Service, req.Method, streamInfo, st, headerMD, string(respJSON)), nil
}

func (e *GRPCExecutor) executeServerStream(ctx context.Context, methodDesc protoreflect.MethodDescriptor, req GRPCRequest) (StepOutput, error) {
	recv := req.Stream.Receive

	// Background persistent stream: start receiving in a goroutine and return immediately.
	if recv.Background {
		return e.startBackgroundStream(ctx, methodDesc, req)
	}

	// Foreground server-streaming: send request, collect up to count messages within timeout.
	return e.executeForegroundServerStream(ctx, methodDesc, req)
}

func (e *GRPCExecutor) executeForegroundServerStream(ctx context.Context, methodDesc protoreflect.MethodDescriptor, req GRPCRequest) (StepOutput, error) {
	recv := req.Stream.Receive
	fullMethod, err := fullMethodPath(methodDesc)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: %w", err)
	}

	inputMsg := dynamicpb.NewMessage(methodDesc.Input())
	if req.Body != "" {
		if err := protojson.Unmarshal([]byte(req.Body), inputMsg); err != nil {
			return StepOutput{}, fmt.Errorf("grpc: unmarshal request body: %w", err)
		}
	}

	streamDesc := &grpc.StreamDesc{
		StreamName:    string(methodDesc.Name()),
		ServerStreams: true,
	}

	var headerMD metadata.MD
	stream, err := e.conn.NewStream(ctx, streamDesc, fullMethod, grpc.Header(&headerMD))
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: open server stream: %w", err)
	}
	if err := stream.SendMsg(inputMsg); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: send request: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: close send: %w", err)
	}

	messages, recvErr := receiveMessages(ctx, stream, methodDesc, recv)
	st := status.Convert(recvErr)

	return buildServerStreamOutput(req, messages, st, headerMD), nil
}

func receiveMessages(ctx context.Context, stream grpc.ClientStream, methodDesc protoreflect.MethodDescriptor, recv *GRPCReceive) ([]string, error) {
	recvTimeout, err := parseStepTimeout(recv.Timeout, defaultGRPCTimeout)
	if err != nil {
		slog.Warn("grpc: invalid receive timeout, using default",
			"timeout", recv.Timeout,
			"default", defaultGRPCTimeout,
			"error", err,
		)
		recvTimeout = defaultGRPCTimeout
	}

	recvCtx, recvCancel := context.WithTimeout(ctx, recvTimeout)
	defer recvCancel()

	count := recv.Count
	if count <= 0 {
		count = 1
	}

	messages := make([]string, 0, count)
	for len(messages) < count {
		if recvCtx.Err() != nil {
			break
		}
		outputMsg := dynamicpb.NewMessage(methodDesc.Output())
		if err := stream.RecvMsg(outputMsg); err != nil {
			return messages, err
		}
		respJSON, _ := protoMarshaler.Marshal(outputMsg)
		messages = append(messages, string(respJSON))
	}
	return messages, nil
}

func buildServerStreamOutput(req GRPCRequest, messages []string, st *status.Status, headerMD metadata.MD) StepOutput {
	var observed strings.Builder
	fmt.Fprintf(&observed, "gRPC %s/%s (server-streaming)\n", req.Service, req.Method)
	fmt.Fprintf(&observed, "Status: %s\n", st.Code().String())
	if st.Message() != "" {
		fmt.Fprintf(&observed, "Message: %s\n", st.Message())
	}
	fmt.Fprintf(&observed, "Received %d messages:\n", len(messages))
	fmt.Fprintf(&observed, "[%s]", strings.Join(messages, ", "))

	captureBody := "[" + strings.Join(messages, ",") + "]"
	headersJSON, _ := json.Marshal(headerMD)

	return StepOutput{
		Observed:    observed.String(),
		CaptureBody: captureBody,
		CaptureSources: map[string]string{
			GRPCSourceStatus:  st.Code().String(),
			GRPCSourceHeaders: string(headersJSON),
		},
	}
}

// ensureBgContext creates the background context for background streams if not yet created.
// Uses context.Background() so that background streams outlive individual step timeouts.
// The Close() method cancels bgCtx to clean up all background streams.
func (e *GRPCExecutor) ensureBgContext() {
	if e.bgCtx == nil {
		e.bgCtx, e.bgCancel = context.WithCancel(context.Background())
		e.bgStreams = make(map[string]*backgroundStream)
	}
}

func (e *GRPCExecutor) startBackgroundStream(ctx context.Context, methodDesc protoreflect.MethodDescriptor, req GRPCRequest) (StepOutput, error) {
	e.ensureBgContext()

	fullMethod, err := fullMethodPath(methodDesc)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: %w", err)
	}

	inputMsg := dynamicpb.NewMessage(methodDesc.Input())
	if req.Body != "" {
		if err := protojson.Unmarshal([]byte(req.Body), inputMsg); err != nil {
			return StepOutput{}, fmt.Errorf("grpc: unmarshal request body: %w", err)
		}
	}

	streamDesc := &grpc.StreamDesc{
		StreamName:    string(methodDesc.Name()),
		ServerStreams: true,
	}

	stream, err := e.conn.NewStream(e.bgCtx, streamDesc, fullMethod)
	if err != nil {
		return StepOutput{}, fmt.Errorf("grpc: open background stream: %w", err)
	}
	if err := stream.SendMsg(inputMsg); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: send request on background stream: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return StepOutput{}, fmt.Errorf("grpc: close send on background stream: %w", err)
	}

	// Assign stream ID.
	streamID := req.Stream.ID
	if streamID == "" {
		streamID = fmt.Sprintf("%d", e.bgCounter)
		e.bgCounter++
	}

	bg := &backgroundStream{done: make(chan struct{})}
	e.bgStreams[streamID] = bg

	go func() {
		defer close(bg.done)
		for {
			outputMsg := dynamicpb.NewMessage(methodDesc.Output())
			if err := stream.RecvMsg(outputMsg); err != nil {
				bg.mu.Lock()
				bg.err = err
				bg.mu.Unlock()
				return
			}
			respJSON, _ := protoMarshaler.Marshal(outputMsg)
			bg.mu.Lock()
			bg.messages = append(bg.messages, string(respJSON))
			bg.mu.Unlock()
		}
	}()

	observed := fmt.Sprintf("gRPC %s/%s (background stream started, id=%s)", req.Service, req.Method, streamID)
	return StepOutput{Observed: observed}, nil
}

func (e *GRPCExecutor) collectBackground(req GRPCRequest) (StepOutput, error) {
	streamID := req.Stream.ID
	bg, ok := e.bgStreams[streamID]
	if !ok {
		return StepOutput{}, fmt.Errorf("grpc: background stream %q not found: %w", streamID, errGRPCStreamNotFound)
	}

	recv := req.Stream.Receive
	if recv == nil {
		recv = &GRPCReceive{Count: 1}
	}

	recvTimeout, err := parseStepTimeout(recv.Timeout, defaultGRPCTimeout)
	if err != nil {
		slog.Warn("grpc: invalid collect timeout, using default",
			"timeout", recv.Timeout,
			"default", defaultGRPCTimeout,
			"error", err,
		)
		recvTimeout = defaultGRPCTimeout
	}

	count := recv.Count
	if count <= 0 {
		count = 1
	}

	timer := time.NewTimer(recvTimeout)
	defer timer.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	messages := make([]string, 0, count)
loop:
	for len(messages) < count {
		bg.mu.Lock()
		available := len(bg.messages)
		if available > 0 {
			take := min(available, count-len(messages))
			taken := make([]string, take)
			copy(taken, bg.messages[:take])
			messages = append(messages, taken...)
			bg.messages = bg.messages[take:]
		}
		bgErr := bg.err
		bg.mu.Unlock()

		if len(messages) >= count {
			break
		}
		if bgErr != nil {
			break
		}

		// Poll briefly before checking again.
		select {
		case <-timer.C:
			break loop
		case <-ticker.C:
		}
	}

	captureBody := "[" + strings.Join(messages, ",") + "]"

	// Derive gRPC status from background stream error if available.
	grpcStatus := "OK"
	bg.mu.Lock()
	if bg.err != nil {
		st := status.Convert(bg.err)
		grpcStatus = st.Code().String()
	}
	bg.mu.Unlock()

	var observed strings.Builder
	fmt.Fprintf(&observed, "gRPC background stream %s: collected %d messages\n", streamID, len(messages))
	fmt.Fprintf(&observed, "Status: %s\n", grpcStatus)
	fmt.Fprintf(&observed, "[%s]", strings.Join(messages, ", "))

	return StepOutput{
		Observed:    observed.String(),
		CaptureBody: captureBody,
		CaptureSources: map[string]string{
			GRPCSourceStatus:  grpcStatus,
			GRPCSourceHeaders: "{}",
		},
	}, nil
}
