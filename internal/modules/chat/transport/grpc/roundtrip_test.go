package grpc

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// This file is the field-completeness property test for the proto conversion
// surface.
//
// decodeProtoExternalUpload dropped UploadedAt and CompletedAt: the encoder put
// both on the wire and the decoder never read them, so two fields were populated
// in the monolith and zero across the seam. Nothing failed, because every
// conversion test names the fields it checks and therefore only checks the fields
// its author remembered.
//
// The property asserted here needs no such list: a fully populated domain value,
// encoded and decoded, must come back equal. A field added to a domain struct and
// forgotten in either direction fails immediately, and the failure names the field
// path rather than the symptom.
//
// The filler uses whole-second timestamps because a third of this contract carries
// timestamps as Unix seconds, which truncates sub-second precision. That
// truncation is a real difference between the compositions and is recorded as such;
// this test is about fields that are dropped entirely, and a filler with
// nanoseconds would fail for a reason that has nothing to do with completeness.

// conversionCase round-trips one fully populated domain value through its encoder
// and decoder.
type conversionCase struct {
	// sample is a pointer to a zero value of the domain type; the filler
	// populates it.
	sample any

	// through encodes and decodes the filled value and returns the result as a
	// pointer to the same type.
	through func(t *testing.T, filled any) any

	// omitted names the fields that must not cross the boundary, with the reason.
	// A field is listed here only when carrying it would be wrong, never because
	// a converter forgot it.
	omitted map[string]string

	// prepare adjusts the filled value where the converter legitimately derives
	// one field from another, so the expectation describes the contract rather
	// than the filler.
	prepare func(filled any)
}

func TestEveryConverterCarriesEveryField(t *testing.T) {
	for name, testCase := range conversionCases() {
		t.Run(name, func(t *testing.T) {
			filled := reflect.New(reflect.TypeOf(testCase.sample).Elem())
			fillValue(t, filled.Elem(), name)
			for field := range testCase.omitted {
				zeroField(t, filled.Elem(), field)
			}
			if testCase.prepare != nil {
				testCase.prepare(filled.Interface())
			}
			result := testCase.through(t, filled.Interface())
			compareValues(t, name, filled.Elem(), reflect.ValueOf(result).Elem())
		})
	}
}

func conversionCases() map[string]conversionCase {
	return map[string]conversionCase{
		"User":              {sample: &domain.User{}, through: through(encodeProtoUser, decodeProtoUser)},
		"Workspace":         {sample: &domain.Workspace{}, through: through(encodeProtoWorkspace, decodeProtoWorkspace)},
		"Conversation":      {sample: &domain.Conversation{}, through: through(encodeProtoConversation, decodeProtoConversation)},
		"ConversationPage":  {sample: &domain.ConversationPage{}, through: through(encodeProtoConversationPage, decodeProtoConversationPage)},
		"ConversationPrefs": {sample: &domain.ConversationPrefs{}, through: through(encodeProtoConversationPrefs, decodeProtoConversationPrefs)},
		"UserPage":          {sample: &domain.UserPage{}, through: through(encodeProtoUserPage, decodeProtoUserPage)},
		"AdminUserPage": {
			sample:  &domain.AdminUserPage{},
			through: through(encodeProtoAdminUserPage, decodeProtoAdminUserPage),
			// The wire carries one identity per entry, and the membership is
			// resolved from the user the row is about, which is the single source
			// of truth for the pair.
			prepare: func(filled any) {
				page := filled.(*domain.AdminUserPage)
				for index := range page.Users {
					page.Users[index].Membership.WorkspaceID = page.Users[index].User.WorkspaceID
					page.Users[index].Membership.UserID = page.Users[index].User.ID
				}
			},
		},
		"Message":          {sample: &domain.Message{}, through: through(encodeProtoMessage, decodeProtoMessage)},
		"MessagePage":      {sample: &domain.MessagePage{}, through: through(encodeProtoMessagePage, decodeProtoMessagePage)},
		"EphemeralMessage": {sample: &domain.EphemeralMessage{}, through: through(encodeProtoEphemeralMessage, decodeProtoEphemeralMessage)},
		"File": {
			sample: &domain.File{},
			omitted: map[string]string{
				"BlobKey": "storage-internal: the blob key is used only inside the owning module and must not reach a caller",
			},
			through: through(encodeProtoFile, decodeProtoFile),
		},
		"FilePage": {sample: &domain.FilePage{}, omitted: map[string]string{"BlobKey": "storage-internal"}, through: through(encodeProtoFilePage, decodeProtoFilePage)},
		"ExternalUpload": {
			sample: &domain.ExternalUpload{},
			omitted: map[string]string{
				"BlobKey": "storage-internal",
				"FileID":  "storage-internal: assigned when the upload completes and resolved inside the module",
			},
			through: through(encodeProtoExternalUpload, decodeProtoExternalUpload),
		},
		"RemoteFile":       {sample: &domain.RemoteFile{}, through: through(encodeProtoRemoteFile, decodeProtoRemoteFile)},
		"RemoteFilePage":   {sample: &domain.RemoteFilePage{}, through: through(encodeProtoRemoteFilePage, decodeProtoRemoteFilePage)},
		"ReadCursor":       {sample: &domain.ReadCursor{}, through: through(encodeProtoReadCursor, decodeProtoReadCursor)},
		"TokenRecord":      {sample: &domain.TokenRecord{}, through: through(encodeProtoToken, decodeProtoToken)},
		"SessionRecord":    {sample: &domain.SessionRecord{}, through: through(encodeProtoSession, decodeProtoSession)},
		"Reaction":         {sample: &domain.Reaction{}, through: through(encodeProtoReaction, decodeProtoReaction)},
		"UserReactionPage": {sample: &domain.UserReactionPage{}, through: through(encodeProtoUserReactionPage, decodeProtoUserReactionPage)},
		"Pin":              {sample: &domain.Pin{}, through: through(encodeProtoPin, decodeProtoPin)},
		"Star":             {sample: &domain.Star{}, through: through(encodeProtoStar, decodeProtoStar)},
		"Bookmark":         {sample: &domain.Bookmark{}, through: through(encodeProtoBookmark, decodeProtoBookmark)},
		"Reminder":         {sample: &domain.Reminder{}, through: through(encodeProtoReminder, decodeProtoReminder)},
		"ScheduledMessage": {sample: &domain.ScheduledMessage{}, through: through(encodeProtoScheduledMessage, decodeProtoScheduledMessage)},
		"DoNotDisturb":     {sample: &domain.DoNotDisturb{}, through: through(encodeProtoDoNotDisturb, decodeProtoDoNotDisturb)},
		"UserGroup":        {sample: &domain.UserGroup{}, through: through(encodeProtoUserGroup, decodeProtoUserGroup)},
		"Call":             {sample: &domain.Call{}, through: through(encodeProtoCall, decodeProtoCall)},
		"Canvas":           {sample: &domain.Canvas{}, through: through(encodeProtoCanvas, decodeProtoCanvas)},
		"List":             {sample: &domain.List{}, through: through(encodeProtoList, decodeProtoList)},
		"ListItem":         {sample: &domain.ListItem{}, through: through(encodeProtoListItem, decodeProtoListItem)},
		"ListItemPage":     {sample: &domain.ListItemPage{}, through: through(encodeProtoListItemPage, decodeProtoListItemPage)},
		"ListDownload":     {sample: &domain.ListDownload{}, through: through(encodeProtoListDownload, decodeProtoListDownload)},
		"AccessLog":        {sample: &domain.AccessLog{}, through: through(encodeProtoAccessLog, decodeProtoAccessLog)},
		"View":             {sample: &domain.View{}, through: through(encodeProtoView, decodeProtoView)},
		"Bot":              {sample: &domain.Bot{}, through: through(encodeProtoBot, decodeProtoBot)},
		"InviteRequest":    {sample: &domain.InviteRequest{}, through: throughInfallible(encodeProtoInviteRequest, decodeProtoInviteRequest)},
		"AppApproval":      {sample: &domain.AppApproval{}, through: throughInfallible(encodeProtoAppApproval, decodeProtoAppApproval)},
		"RTMConnection":    {sample: &domain.RTMConnection{}, through: throughInfallible(encodeProtoRTMConnection, decodeProtoRTMConnection)},
		"IncomingWebhook": {
			sample: &domain.IncomingWebhook{},
			omitted: map[string]string{
				"SecretHash": "the hash must never cross the boundary; only the one-time plaintext secret does, as a separate return value",
			},
			through: func(t *testing.T, filled any) any {
				value := *filled.(*domain.IncomingWebhook)
				decodedValue, secret, err := decodeProtoIncomingWebhook(encodeProtoIncomingWebhook(value, "plaintext-secret"))
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if secret != "plaintext-secret" {
					t.Fatalf("secret = %q, want the plaintext the encoder was given", secret)
				}
				return &decodedValue
			},
		},
		"events.Record": {sample: &events.Record{}, through: func(t *testing.T, filled any) any {
			records, err := decodeProtoEvents(encodeProtoEvents([]events.Record{*filled.(*events.Record)}))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("decoded %d records, want 1", len(records))
			}
			return &records[0]
		}},
	}
}

// through builds the round trip for a symmetric converter pair. Naming the two
// functions rather than writing the call out per case is what keeps a case a
// single line, so adding a converter to this test costs nothing.
func through[Value, Proto any](encode func(Value) Proto, decode func(Proto) (Value, error)) func(*testing.T, any) any {
	return func(t *testing.T, filled any) any {
		t.Helper()
		result, err := decode(encode(*filled.(*Value)))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return &result
	}
}

// throughInfallible is through for the three converters whose decoder cannot fail.
func throughInfallible[Value, Proto any](encode func(Value) Proto, decode func(Proto) Value) func(*testing.T, any) any {
	return func(t *testing.T, filled any) any {
		t.Helper()
		result := decode(encode(*filled.(*Value)))
		return &result
	}
}

// fillTime is the instant every timestamp is filled with. It is whole-second and
// UTC; see the file comment.
var fillTime = time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

// fillValue populates every field reachable from value with a distinctive
// non-zero value.
//
// A few fields carry a value from a closed set rather than an arbitrary string,
// because the surrounding code normalises them and a normalised value is not the
// value that was sent — which would look like a dropped field without being one.
func fillValue(t *testing.T, value reflect.Value, path string) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			value.Set(reflect.ValueOf(fillTime))
			return
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			fillValue(t, value.Field(index), path+"."+field.Name)
		}
	case reflect.String:
		if value.Type() == reflect.TypeOf(domain.MessageTimestamp("")) {
			// A message timestamp is parsed by several decoders, so it has to be
			// a real one.
			value.SetString(string(domain.NewMessageTimestamp(fillTime)))
			return
		}
		value.SetString(fillString(path))
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(int64(7 + len(path)%13))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(uint64(11 + len(path)%17))
	case reflect.Float32, reflect.Float64:
		value.SetFloat(1.5)
	case reflect.Slice:
		if strings.HasSuffix(path, ".Scopes") {
			// Scopes are normalised (deduplicated and ordered), so the filler has
			// to supply distinct scopes that survive normalisation in order.
			value.Set(reflect.ValueOf([]string{"chat:write", "users:read"}).Convert(value.Type()))
			return
		}
		slice := reflect.MakeSlice(value.Type(), 2, 2)
		for index := 0; index < 2; index++ {
			fillValue(t, slice.Index(index), fmt.Sprintf("%s[%d]", path, index))
		}
		value.Set(slice)
	case reflect.Map:
		result := reflect.MakeMap(value.Type())
		for index := 0; index < 2; index++ {
			key := reflect.New(value.Type().Key()).Elem()
			fillValue(t, key, fmt.Sprintf("%s.key%d", path, index))
			element := reflect.New(value.Type().Elem()).Elem()
			fillValue(t, element, fmt.Sprintf("%s.value%d", path, index))
			result.SetMapIndex(key, element)
		}
		value.Set(result)
	case reflect.Pointer:
		allocated := reflect.New(value.Type().Elem())
		fillValue(t, allocated.Elem(), path)
		value.Set(allocated)
	default:
		t.Fatalf("%s: the filler does not handle %s; extend it rather than skipping the field", path, value.Kind())
	}
}

// fillString supplies a value for one string field. The overrides keep normalised
// fields inside the set their normaliser accepts.
func fillString(path string) string {
	field := path[strings.LastIndex(path, ".")+1:]
	if index := strings.Index(field, "["); index >= 0 {
		field = path[:strings.Index(path, "[")]
		field = field[strings.LastIndex(field, ".")+1:]
	}
	switch field {
	case "Presence":
		return string(domain.PresenceAway)
	case "Type", "BookmarkType":
		return "link"
	}
	return "value-" + strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(path, "."), ".", "-"))
}

func zeroField(t *testing.T, value reflect.Value, name string) {
	t.Helper()
	found := false
	zeroFieldRecursive(value, name, &found)
	if !found {
		t.Fatalf("omitted field %q does not exist; remove it from the case", name)
	}
}

func zeroFieldRecursive(value reflect.Value, name string, found *bool) {
	switch value.Kind() {
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			if field.Name == name {
				value.Field(index).Set(reflect.Zero(field.Type))
				*found = true
				continue
			}
			zeroFieldRecursive(value.Field(index), name, found)
		}
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			zeroFieldRecursive(value.Index(index), name, found)
		}
	case reflect.Pointer:
		if !value.IsNil() {
			zeroFieldRecursive(value.Elem(), name, found)
		}
	}
}

// compareValues reports the path of the first field that did not survive the
// round trip, so a failure names the dropped field instead of dumping two structs.
func compareValues(t *testing.T, path string, want, got reflect.Value) {
	t.Helper()
	if want.Type() != got.Type() {
		t.Fatalf("%s: type %s became %s", path, want.Type(), got.Type())
	}
	switch want.Kind() {
	case reflect.Struct:
		if want.Type() == reflect.TypeOf(time.Time{}) {
			wantTime, gotTime := want.Interface().(time.Time), got.Interface().(time.Time)
			if !wantTime.Equal(gotTime) {
				t.Errorf("%s: %s did not survive the round trip (got %s)", path, wantTime.Format(time.RFC3339Nano), gotTime.Format(time.RFC3339Nano))
			}
			return
		}
		for index := 0; index < want.NumField(); index++ {
			field := want.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			compareValues(t, path+"."+field.Name, want.Field(index), got.Field(index))
		}
	case reflect.Slice:
		if want.Len() != got.Len() {
			t.Errorf("%s: length %d became %d", path, want.Len(), got.Len())
			return
		}
		for index := 0; index < want.Len(); index++ {
			compareValues(t, fmt.Sprintf("%s[%d]", path, index), want.Index(index), got.Index(index))
		}
	case reflect.Map:
		if want.Len() != got.Len() {
			t.Errorf("%s: %d entries became %d", path, want.Len(), got.Len())
			return
		}
		for _, key := range want.MapKeys() {
			element := got.MapIndex(key)
			if !element.IsValid() {
				t.Errorf("%s: key %v was dropped", path, key.Interface())
				continue
			}
			compareValues(t, fmt.Sprintf("%s[%v]", path, key.Interface()), want.MapIndex(key), element)
		}
	case reflect.Pointer:
		if want.IsNil() != got.IsNil() {
			t.Errorf("%s: nil-ness changed (want nil = %t)", path, want.IsNil())
			return
		}
		if !want.IsNil() {
			compareValues(t, path, want.Elem(), got.Elem())
		}
	default:
		if want.Interface() != got.Interface() {
			t.Errorf("%s: %#v became %#v", path, want.Interface(), got.Interface())
		}
	}
}
