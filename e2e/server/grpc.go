package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The gRPC service of notes, notes.v1.Notes, which e2e/api/notes.proto
// describes. Its definition is built here rather than generated, and served
// with reflection. Its ListNotes gives the count as a string, where
// notes.proto declares an int32, for a persona test to find.

// notesFile is the definition the server serves.
var notesFile = func() protoreflect.FileDescriptor {
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	field := func(name string, n int32, t *descriptorpb.FieldDescriptorProto_Type, l *descriptorpb.FieldDescriptorProto_Label, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(n), Type: t, Label: l, JsonName: proto.String(name)}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	message := func(name string, fields ...*descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{Name: proto.String(name), Field: fields}
	}
	readOnly := &descriptorpb.MethodOptions{IdempotencyLevel: descriptorpb.MethodOptions_NO_SIDE_EFFECTS.Enum()}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("notes/v1/notes.proto"),
		Package: proto.String("notes.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			message("Note", field("id", 1, str, opt, ""), field("text", 2, str, opt, "")),
			message("CreateNoteRequest", field("text", 1, str, opt, "")),
			message("GetNoteRequest", field("id", 1, str, opt, "")),
			message("ListNotesRequest"),
			message("ListNotesResponse", field("notes", 1, msg, rep, ".notes.v1.Note"), field("count", 2, str, opt, "")),
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Notes"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{Name: proto.String("CreateNote"), InputType: proto.String(".notes.v1.CreateNoteRequest"), OutputType: proto.String(".notes.v1.Note")},
				{Name: proto.String("GetNote"), InputType: proto.String(".notes.v1.GetNoteRequest"), OutputType: proto.String(".notes.v1.Note"), Options: readOnly},
				{Name: proto.String("ListNotes"), InputType: proto.String(".notes.v1.ListNotesRequest"), OutputType: proto.String(".notes.v1.ListNotesResponse"), Options: readOnly},
			},
		}},
	}, nil)
	if err != nil {
		panic(err)
	}
	return fd
}()

// grpcNotes keeps the notes of the gRPC service, apart from those of the
// HTTP API.
type grpcNotes struct {
	mu    sync.Mutex
	notes [][2]string
}

// newGRPCServer returns the server of notes.v1.Notes, with reflection.
func newGRPCServer() *grpc.Server {
	// Reflection finds the definition among the files registered.
	if _, err := protoregistry.GlobalFiles.FindFileByPath(notesFile.Path()); err != nil {
		if err := protoregistry.GlobalFiles.RegisterFile(notesFile); err != nil {
			panic(err)
		}
	}
	n := &grpcNotes{}
	sd := notesFile.Services().ByName("Notes")
	desc := grpc.ServiceDesc{ServiceName: string(sd.FullName()), HandlerType: (*any)(nil), Metadata: notesFile.Path()}
	for _, name := range []string{"CreateNote", "GetNote", "ListNotes"} {
		md := sd.Methods().ByName(protoreflect.Name(name))
		desc.Methods = append(desc.Methods, grpc.MethodDesc{
			MethodName: name,
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				in := dynamicpb.NewMessage(md.Input())
				if err := dec(in); err != nil {
					return nil, err
				}
				if err := authorized(ctx); err != nil {
					return nil, err
				}
				return n.handle(name, in, dynamicpb.NewMessage(md.Output()))
			},
		})
	}
	s := grpc.NewServer()
	s.RegisterService(&desc, nil)
	reflection.Register(s)
	return s
}

// authorized refuses a call without the key the HTTP API takes, in the
// metadata x-api-key.
func authorized(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-api-key"); len(v) == 0 || v[0] != apiKey {
		return status.Error(codes.Unauthenticated, "a valid x-api-key is required")
	}
	return nil
}

func (n *grpcNotes) handle(method string, in, out *dynamicpb.Message) (any, error) {
	get := func(m *dynamicpb.Message, name string) string {
		return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).String()
	}
	set := func(m *dynamicpb.Message, name, v string) {
		m.Set(m.Descriptor().Fields().ByName(protoreflect.Name(name)), protoreflect.ValueOfString(v))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch method {
	case "CreateNote":
		text := strings.TrimSpace(get(in, "text"))
		if text == "" {
			return nil, status.Error(codes.InvalidArgument, "text is required")
		}
		id := "g" + strconv.Itoa(len(n.notes)+1)
		n.notes = append(n.notes, [2]string{id, text})
		set(out, "id", id)
		set(out, "text", text)
	case "GetNote":
		id := get(in, "id")
		for _, note := range n.notes {
			if note[0] == id {
				set(out, "id", note[0])
				set(out, "text", note[1])
				return out, nil
			}
		}
		return nil, status.Errorf(codes.NotFound, "no note is %s", id)
	case "ListNotes":
		fd := out.Descriptor().Fields().ByName("notes")
		list := out.Mutable(fd).List()
		for _, note := range n.notes {
			m := dynamicpb.NewMessage(fd.Message())
			set(m, "id", note[0])
			set(m, "text", note[1])
			list.Append(protoreflect.ValueOfMessage(m))
		}
		set(out, "count", fmt.Sprint(len(n.notes)))
	}
	return out, nil
}
