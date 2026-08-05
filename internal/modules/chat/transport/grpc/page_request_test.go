package grpc

import (
	"context"
	"reflect"
	"strings"
	"testing"

	grpclib "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A page request crosses the seam by hand. The server side funnels through one
// protoPageRequest, but each of the twenty-two client methods spells the fields
// out inline, and nothing checked that a method carried them: domain.PageRequest
// is not a conversion case, so TestEveryConverterCarriesEveryField never sees it.
// Dropping a field there is silent and asymmetric — the monolith honours it and
// the split deployment ignores it — which is the divergence class this package
// exists to prevent. Adding a field to domain.PageRequest meant editing all
// twenty-two sites correctly with nothing to catch a miss.
//
// This derives the methods from the type rather than from a list, so a paged
// method added later is covered the day it is written.

// pageRequestProtoField maps a domain.PageRequest field to the proto field that
// carries it. A field absent here fails the test: a new field must state how it
// crosses the seam.
var pageRequestProtoField = map[string]string{
	"Limit":      "limit",
	"Cursor":     "cursor",
	"Descending": "descending",
}

// pageRequestExemptions records a method that deliberately does not carry a
// field, with the reason. An exemption is a decision; a silent drop is a defect.
var pageRequestExemptions = func() map[string]map[string]string {
	// History is the only read whose store contract implements reverse paging.
	// Every other paged RPC is explicit here so adding reverse paging to one
	// requires carrying the bit over its wire boundary rather than silently
	// ignoring it in split composition.
	const ascendingOnly = "the backing read explicitly rejects descending pages"
	methods := []string{
		"AdminConnectedChannelInfo",
		"AdminConversationTeams",
		"AdminListApps",
		"AdminListInviteRequests",
		"AdminListUsers",
		"AdminSearchConversations",
		"SearchChannels",
		"SearchPeople",
		"AdminTeamUsers",
		"ConversationMembers",
		"Files",
		"ListItems",
		"FollowedThreads",
		"ListSharedInvites",
		"ListUserGroups",
		"Pins",
		"Reactions",
		"Reminders",
		"RemoteFiles",
		"Replies",
		"ScheduledMessages",
		"Search",
		"Stars",
		"UserReactions",
		"Users",
	}
	exemptions := make(map[string]map[string]string, len(methods))
	for _, method := range methods {
		exemptions[method] = map[string]string{"Descending": ascendingOnly}
	}
	return exemptions
}()

// recordingConn captures the request message a Remote method puts on the wire.
type recordingConn struct {
	method  string
	request proto.Message
}

func (c *recordingConn) Invoke(_ context.Context, method string, args, _ any, _ ...grpclib.CallOption) error {
	message, ok := args.(proto.Message)
	if !ok {
		return nil
	}
	c.method = method
	c.request = proto.Clone(message)
	return nil
}

func (c *recordingConn) NewStream(context.Context, *grpclib.StreamDesc, string, ...grpclib.CallOption) (grpclib.ClientStream, error) {
	return nil, nil
}

// markedPageRequest fills every field of a domain.PageRequest with a value no
// zero value can be mistaken for, so a dropped field is visible as a zero on
// the wire rather than as a plausible default.
func markedPageRequest(t *testing.T) domain.PageRequest {
	t.Helper()
	request := domain.PageRequest{}
	value := reflect.ValueOf(&request).Elem()
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		if !field.IsExported() {
			continue
		}
		if _, ok := pageRequestProtoField[field.Name]; !ok {
			t.Fatalf("domain.PageRequest field %q has no declared proto field; say how it crosses the seam", field.Name)
		}
		switch value.Field(index).Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			value.Field(index).SetInt(37)
		case reflect.String:
			value.Field(index).SetString("page-request-marker-4c1f")
		case reflect.Bool:
			value.Field(index).SetBool(true)
		default:
			t.Fatalf("domain.PageRequest field %q is a %s, which this check cannot mark", field.Name, value.Field(index).Kind())
		}
	}
	return request
}

// argumentFor supplies a value for a parameter the paged method needs but this
// check is not about. Zero values are fine: only the page request is asserted.
func argumentFor(parameter reflect.Type, request domain.PageRequest) reflect.Value {
	if parameter == reflect.TypeOf(domain.PageRequest{}) {
		return reflect.ValueOf(request)
	}
	return reflect.Zero(parameter)
}

func TestEveryPagedRemoteMethodCarriesEveryPageRequestField(t *testing.T) {
	remoteType := reflect.TypeOf(Remote{})
	pageRequestType := reflect.TypeOf(domain.PageRequest{})
	marked := markedPageRequest(t)

	paged := 0
	for index := 0; index < remoteType.NumMethod(); index++ {
		method := remoteType.Method(index)
		carries := false
		for argument := 1; argument < method.Type.NumIn(); argument++ {
			if method.Type.In(argument) == pageRequestType {
				carries = true
				break
			}
		}
		if !carries {
			continue
		}
		paged++
		t.Run(method.Name, func(t *testing.T) {
			conn := &recordingConn{}
			remote, err := NewRemote(conn)
			if err != nil {
				t.Fatalf("build remote: %v", err)
			}
			arguments := make([]reflect.Value, 0, method.Type.NumIn())
			arguments = append(arguments, reflect.ValueOf(remote))
			for argument := 1; argument < method.Type.NumIn(); argument++ {
				parameter := method.Type.In(argument)
				if parameter == reflect.TypeOf((*context.Context)(nil)).Elem() {
					arguments = append(arguments, reflect.ValueOf(context.Background()))
					continue
				}
				arguments = append(arguments, argumentFor(parameter, marked))
			}
			// The reply is left zero; only the outgoing request is asserted, so a
			// decode failure on the empty reply is expected and not a result.
			func() {
				defer func() { _ = recover() }()
				method.Func.Call(arguments)
			}()
			if conn.request == nil {
				t.Fatalf("%s put no request on the wire, so its page request cannot be checked", method.Name)
			}
			assertCarriesPageRequest(t, method.Name, conn.request, marked)
		})
	}
	if paged == 0 {
		t.Fatal("no paged Remote method was found; the check is no longer looking at anything")
	}
	t.Logf("checked %d paged methods", paged)
}

func assertCarriesPageRequest(t *testing.T, methodName string, request proto.Message, marked domain.PageRequest) {
	t.Helper()
	message := request.ProtoReflect()
	descriptor := message.Descriptor()
	value := reflect.ValueOf(marked)
	for index := 0; index < value.NumField(); index++ {
		domainField := value.Type().Field(index)
		if !domainField.IsExported() {
			continue
		}
		protoName := pageRequestProtoField[domainField.Name]
		field := descriptor.Fields().ByName(protoreflect.Name(protoName))
		reason, exempt := pageRequestExemptions[methodName][domainField.Name]
		if field == nil {
			if exempt {
				continue
			}
			t.Errorf("%s sends %s, which has no %q field, so domain.PageRequest.%s cannot cross the seam; carry it or record an exemption saying why not",
				methodName, descriptor.FullName(), protoName, domainField.Name)
			continue
		}
		if exempt {
			t.Errorf("%s is exempted from carrying %s (%q) but %s has the field; drop the exemption or carry it",
				methodName, domainField.Name, reason, descriptor.FullName())
			continue
		}
		if !carriesNonZero(message, field) {
			t.Errorf("%s dropped domain.PageRequest.%s: %s.%s is zero after a request that set it",
				methodName, domainField.Name, descriptor.FullName(), protoName)
		}
	}
}

func carriesNonZero(message protoreflect.Message, field protoreflect.FieldDescriptor) bool {
	value := message.Get(field)
	switch field.Kind() {
	case protoreflect.BoolKind:
		return value.Bool()
	case protoreflect.StringKind:
		return strings.TrimSpace(value.String()) != ""
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return value.Int() != 0
	default:
		return value.IsValid()
	}
}
