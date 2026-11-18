package main

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestGRPCNotes(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := newGRPCServer()
	go func() { _ = s.Serve(lis) }()
	defer s.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	sd := notesFile.Services().ByName("Notes")
	call := func(ctx context.Context, method string, set map[string]string) (*dynamicpb.Message, error) {
		md := sd.Methods().ByName(protoreflect.Name(method))
		in := dynamicpb.NewMessage(md.Input())
		for k, v := range set {
			in.Set(md.Input().Fields().ByName(protoreflect.Name(k)), protoreflect.ValueOfString(v))
		}
		out := dynamicpb.NewMessage(md.Output())
		return out, conn.Invoke(ctx, "/notes.v1.Notes/"+method, in, out)
	}
	keyed := metadata.AppendToOutgoingContext(t.Context(), "x-api-key", apiKey)

	if _, err := call(t.Context(), "ListNotes", nil); status.Code(err) != codes.Unauthenticated {
		t.Errorf("without the key: %v", err)
	}
	if _, err := call(keyed, "CreateNote", map[string]string{"text": " "}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("without a text: %v", err)
	}
	out, err := call(keyed, "CreateNote", map[string]string{"text": "Buy milk"})
	if err != nil || out.Get(out.Descriptor().Fields().ByName("id")).String() != "g1" {
		t.Fatalf("a note written: %v, %v", out, err)
	}
	if out, err := call(keyed, "GetNote", map[string]string{"id": "g1"}); err != nil || out.Get(out.Descriptor().Fields().ByName("text")).String() != "Buy milk" {
		t.Errorf("a note read: %v, %v", out, err)
	}
	if _, err := call(keyed, "GetNote", map[string]string{"id": "g9"}); status.Code(err) != codes.NotFound {
		t.Errorf("a note that is not there: %v", err)
	}
	// The count is a string, against notes.proto.
	out, err = call(keyed, "ListNotes", nil)
	if err != nil || out.Get(out.Descriptor().Fields().ByName("count")).String() != "1" {
		t.Errorf("the list: %v, %v", out, err)
	}
}
