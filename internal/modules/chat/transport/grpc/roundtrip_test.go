package grpc

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"hash/fnv"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	"google.golang.org/protobuf/proto"
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
	// pointer to the same type, together with the proto message the encoder
	// produced. The wire message is what lets a case assert that an omitted field
	// did not cross the boundary, rather than asserting it in a comment.
	through func(t *testing.T, filled any) (any, proto.Message, error)

	// omitted names the fields that must not cross the boundary, with the reason.
	// A field is listed here only when carrying it would be wrong, never because
	// a converter forgot it.
	omitted map[string]string

	// prepare adjusts the filled value where the converter legitimately derives
	// one field from another, so the expectation describes the contract rather
	// than the filler.
	prepare func(filled any)
}

type appHomeRoundTrip struct {
	App  domain.InstalledApp
	View domain.View
}

// omittedMarker is the value an omitted field is filled with before the wire
// check. It is distinctive enough that finding it in the marshalled message can
// only mean the encoder wrote that field.
const omittedMarker = "must-not-cross-the-boundary-9f3c1"

func TestEveryConverterCarriesEveryField(t *testing.T) {
	for name, testCase := range conversionCases() {
		// Each case runs once per bit of the bool numbering; see boolFillRuns.
		// Every bool used to be filled true, so two sibling flags carried
		// identical values and swapping them was invisible — swapping
		// InviteRequest.Restricted with .UltraRestricted, which is the difference
		// between a single-channel guest who may post and one who may not, left
		// all 271 tests in this package green.
		for run := 0; run < boolFillRuns; run++ {
			t.Run(fmt.Sprintf("%s/bools=%d", name, run), func(t *testing.T) {
				filled := reflect.New(reflect.TypeOf(testCase.sample).Elem())
				fillValue(t, filled.Elem(), name, &filler{run: run})
				for field := range testCase.omitted {
					zeroField(t, filled.Elem(), field)
				}
				if testCase.prepare != nil {
					testCase.prepare(filled.Interface())
				}
				result, _, err := testCase.through(t, filled.Interface())
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				compareValues(t, name, filled.Elem(), reflect.ValueOf(result).Elem())
			})
		}
		// A filled round trip cannot see how "unset" travels. time.Time{} is
		// the case that matters: its UnixNano is -6795364578871345152, so an
		// encoder that writes it raw and a decoder that reads any non-zero
		// value give back an instant in 1754. domain.FollowedThread.LastReplyAt
		// did exactly that — a followed thread with no replies came back from
		// the remote composition as last replied to three centuries ago — and
		// the filled pass was green the whole time because it never sends a
		// zero.
		t.Run(name+"/zero times stay zero", func(t *testing.T) {
			empty := reflect.New(reflect.TypeOf(testCase.sample).Elem())
			fillValue(t, empty.Elem(), name, &filler{})
			for field := range testCase.omitted {
				zeroField(t, empty.Elem(), field)
			}
			zeroEveryTime(t, empty.Elem())
			if testCase.prepare != nil {
				testCase.prepare(empty.Interface())
			}
			result, _, err := testCase.through(t, empty.Interface())
			if err != nil {
				// Refusing a value whose required timestamp is missing is a
				// contract, and it is a stronger answer than fabricating an
				// instant. What this pass forbids is the third outcome:
				// accepting the value and inventing a time for it.
				t.Skipf("the decoder refuses a value with no times, which is stronger than answering with a fabricated one: %v", err)
			}
			assertZeroTimesStayZero(t, name, reflect.ValueOf(result).Elem())
		})
		if len(testCase.omitted) == 0 {
			continue
		}
		// The omitted contract is "must not cross the boundary". Zeroing the field
		// before encoding only proves zero-in/zero-out: the encoder is never handed
		// a value, so no assertion in the round trip can observe whether it would
		// have put one on the wire. IncomingWebhook.SecretHash is the field that
		// matters — its reason says the hash must never leave the module — and it
		// was asserted by that sentence alone.
		t.Run(name+"/omitted fields never reach the wire", func(t *testing.T) {
			filled := reflect.New(reflect.TypeOf(testCase.sample).Elem())
			fillValue(t, filled.Elem(), name, &filler{})
			for field, reason := range testCase.omitted {
				setStringField(t, filled.Elem(), field, omittedMarker)
				_ = reason
			}
			if testCase.prepare != nil {
				testCase.prepare(filled.Interface())
			}
			_, wire, err := testCase.through(t, filled.Interface())
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if wire == nil {
				t.Fatal("the case does not expose the encoded message, so the omitted contract cannot be checked")
			}
			encoded, err := proto.Marshal(wire)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(encoded, []byte(omittedMarker)) {
				fields := make([]string, 0, len(testCase.omitted))
				for field, reason := range testCase.omitted {
					fields = append(fields, fmt.Sprintf("%s (%s)", field, reason))
				}
				t.Errorf("%s: the encoder put an omitted field on the wire; the case names %v", name, fields)
			}
		})
	}
}

// zeroEveryTime sets every time.Time in a filled value back to zero. The value
// is filled first so decoders that refuse an incomplete message still reach
// their time fields: the question this pass asks is how "unset" travels, not
// whether a decoder validates.
func zeroEveryTime(t *testing.T, value reflect.Value) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			if value.CanSet() {
				value.Set(reflect.ValueOf(time.Time{}))
			}
			return
		}
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				zeroEveryTime(t, value.Field(index))
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			zeroEveryTime(t, value.Index(index))
		}
	case reflect.Pointer:
		if !value.IsNil() {
			zeroEveryTime(t, value.Elem())
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			item := reflect.New(value.Type().Elem()).Elem()
			item.Set(value.MapIndex(key))
			zeroEveryTime(t, item)
			value.SetMapIndex(key, item)
		}
	}
}

// assertZeroTimesStayZero walks the decoded value and reports any time that
// came back set. It reports the path so a failure names the field rather than
// the type.
func assertZeroTimesStayZero(t *testing.T, path string, value reflect.Value) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			if instant, ok := value.Interface().(time.Time); ok && !instant.IsZero() {
				t.Errorf("%s: a zero time round-tripped to %s; encode it as zero rather than raw UnixNano", path, instant.UTC())
			}
			return
		}
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath != "" {
				continue
			}
			assertZeroTimesStayZero(t, path+"."+value.Type().Field(index).Name, value.Field(index))
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			assertZeroTimesStayZero(t, fmt.Sprintf("%s[%d]", path, index), value.Index(index))
		}
	case reflect.Pointer:
		if !value.IsNil() {
			assertZeroTimesStayZero(t, path, value.Elem())
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			assertZeroTimesStayZero(t, fmt.Sprintf("%s[%v]", path, key.Interface()), value.MapIndex(key))
		}
	}
}

// setStringField writes a marker into every field with the given name. It fails
// on a non-string field rather than skipping it, because an omitted field this
// check cannot reach is an omitted field nothing asserts.
func setStringField(t *testing.T, value reflect.Value, name, marker string) {
	t.Helper()
	found := false
	var walk func(reflect.Value)
	walk = func(value reflect.Value) {
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
					if value.Field(index).Kind() != reflect.String {
						t.Fatalf("omitted field %q is a %s; the wire check can only mark a string", name, value.Field(index).Kind())
					}
					value.Field(index).SetString(marker)
					found = true
					continue
				}
				walk(value.Field(index))
			}
		case reflect.Slice:
			for index := 0; index < value.Len(); index++ {
				walk(value.Index(index))
			}
		case reflect.Pointer:
			if !value.IsNil() {
				walk(value.Elem())
			}
		}
	}
	walk(value)
	if !found {
		t.Fatalf("omitted field %q does not exist; remove it from the case", name)
	}
}

func conversionCases() map[string]conversionCase {
	return map[string]conversionCase{
		"User":              {sample: &domain.User{}, through: through(encodeProtoUser, decodeProtoUser)},
		"Workspace":         {sample: &domain.Workspace{}, through: through(encodeProtoWorkspace, decodeProtoWorkspace)},
		"WorkspacePage":     {sample: &domain.WorkspacePage{}, through: through(encodeProtoWorkspacePage, decodeProtoWorkspacePage)},
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
		"Message":          {sample: &domain.Message{}, omitted: map[string]string{"BlobKey": "storage-internal file location"}, through: through(encodeProtoMessage, decodeProtoMessage)},
		"MessagePage":      {sample: &domain.MessagePage{}, omitted: map[string]string{"BlobKey": "storage-internal file location"}, through: through(encodeProtoMessagePage, decodeProtoMessagePage)},
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
		"RemoteFile":         {sample: &domain.RemoteFile{}, through: through(encodeProtoRemoteFile, decodeProtoRemoteFile)},
		"RemoteFilePage":     {sample: &domain.RemoteFilePage{}, through: through(encodeProtoRemoteFilePage, decodeProtoRemoteFilePage)},
		"ReadCursor":         {sample: &domain.ReadCursor{}, through: through(encodeProtoReadCursor, decodeProtoReadCursor)},
		"ThreadSummary":      {sample: &domain.ThreadSummary{}, through: through(encodeProtoThreadSummary, decodeProtoThreadSummary)},
		"AssistantThread":    {sample: &domain.AssistantThread{Prompts: []domain.AssistantPrompt{{}}}, through: through(encodeProtoAssistantThread, decodeProtoAssistantThread)},
		"FollowedThreadPage": {sample: &domain.FollowedThreadPage{Threads: []domain.FollowedThread{{}}}, through: through(encodeProtoFollowedThreadPage, decodeProtoFollowedThreadPage)},
		"WorkspaceNotificationPreferences": {
			sample: &domain.WorkspaceNotificationPreferences{},
			prepare: func(filled any) {
				preferences := filled.(*domain.WorkspaceNotificationPreferences)
				preferences.Level = domain.NotificationAll
				// The property fills every field with an arbitrary value, and a
				// schedule is the one field here whose values constrain each
				// other: an arbitrary day list, minute pair and zone name is
				// almost never a schedule anyone could have set. Making it a
				// real one keeps the property testing the conversion rather
				// than the validator, and every field of it is still non-zero
				// so a converter that dropped one would still be caught.
				preferences.Schedule = domain.NotificationSchedule{
					Enabled: true, Days: []time.Weekday{time.Monday, time.Saturday},
					StartMinute: 9*60 + 30, EndMinute: 18*60 + 45, TimeZone: "Europe/Berlin",
				}
			},
			through: through(encodeProtoWorkspaceNotificationPreferences, decodeProtoWorkspaceNotificationPreferences),
		},
		"ExternalTeam":     {sample: &domain.ExternalTeam{}, through: throughInfallible(encodeProtoExternalTeam, decodeProtoExternalTeam)},
		"ExternalTeamPage": {sample: &domain.ExternalTeamPage{Teams: []domain.ExternalTeam{{}}}, through: throughInfallible(encodeProtoExternalTeamPage, decodeProtoExternalTeamPage)},
		"CanvasGrant":      {sample: &domain.CanvasAccess{}, through: throughInfallible(encodeProtoCanvasGrant, decodeProtoCanvasGrant)},
		"ListGrant":        {sample: &domain.ListAccess{}, through: throughInfallible(encodeProtoListGrant, decodeProtoListGrant)},
		"ListGrants": {
			sample: &listGrantsRoundTrip{},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := filled.(*listGrantsRoundTrip)
				wire := &chatv1.ListGrantsResponse{Grants: encodeProtoListGrants(value.Grants)}
				return &listGrantsRoundTrip{Grants: decodeProtoListGrants(wire.GetGrants())}, wire, nil
			},
		},
		"CanvasGrants": {
			sample: &canvasGrantsRoundTrip{},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := filled.(*canvasGrantsRoundTrip)
				wire := &chatv1.CanvasGrantsResponse{Grants: encodeProtoCanvasGrants(value.Grants)}
				return &canvasGrantsRoundTrip{Grants: decodeProtoCanvasGrants(wire.GetGrants())}, wire, nil
			},
		},
		"CanvasComment":      {sample: &domain.CanvasComment{}, through: throughInfallible(encodeProtoCanvasComment, decodeProtoCanvasComment)},
		"CanvasCommentPage":  {sample: &domain.CanvasCommentPage{}, through: through(encodeProtoCanvasCommentPage, decodeProtoCanvasCommentPage)},
		"CanvasRevision":     {sample: &domain.CanvasRevision{}, through: throughInfallible(encodeProtoCanvasRevision, decodeProtoCanvasRevision)},
		"CanvasRevisionPage": {sample: &domain.CanvasRevisionPage{}, through: through(encodeProtoCanvasRevisionPage, decodeProtoCanvasRevisionPage)},
		"ListItemSummary": {
			sample:  &domain.ListItemSummary{},
			through: throughInfallible(encodeProtoListItemSummary, decodeProtoListItemSummary),
		},
		"NotificationSchedule": {
			sample: &domain.NotificationSchedule{},
			prepare: func(filled any) {
				schedule := filled.(*domain.NotificationSchedule)
				schedule.Enabled = true
				schedule.Days = []time.Weekday{time.Monday, time.Saturday}
				schedule.StartMinute = 9*60 + 30
				schedule.EndMinute = 18*60 + 45
				schedule.TimeZone = "Europe/Berlin"
			},
			through: throughInfallible(encodeProtoNotificationSchedule, decodeProtoNotificationSchedule),
		},
		"ConversationNotificationPreferences": {
			sample: &domain.ConversationNotificationPreferences{},
			prepare: func(filled any) {
				filled.(*domain.ConversationNotificationPreferences).Level = domain.NotificationMute
			},
			through: through(encodeProtoConversationNotificationPreferences, decodeProtoConversationNotificationPreferences),
		},
		"TokenRecord":   {sample: &domain.TokenRecord{}, through: through(encodeProtoToken, decodeProtoToken)},
		"SessionRecord": {sample: &domain.SessionRecord{}, through: through(encodeProtoSession, decodeProtoSession)},
		"AppConfigurationCredentials": {
			sample:  &domain.AppConfigurationCredentials{},
			through: through(encodeProtoAppConfigurationCredentials, decodeProtoAppConfigurationCredentials),
		},
		"DeveloperApp": {
			sample: &domain.App{},
			omitted: map[string]string{
				"SigningSecretHash":           "credential digest is storage-internal and must not cross the module boundary",
				"SigningSecretCiphertext":     "encrypted credential is storage-internal and must not cross the module boundary",
				"VerificationTokenCiphertext": "encrypted legacy credential is storage-internal and must not cross the module boundary",
				"VerificationTokenHash":       "credential digest is storage-internal and must not cross the module boundary",
			},
			through: through(encodeProtoDeveloperApp, decodeProtoDeveloperApp),
		},
		"InstalledApp":              {sample: &domain.InstalledApp{}, through: through(encodeProtoInstalledApp, decodeProtoInstalledApp)},
		"OAuthAuthorizationRequest": {sample: &domain.OAuthAuthorizationRequest{}, through: through(encodeProtoOAuthAuthorizationRequest, decodeProtoOAuthAuthorizationRequest)},
		"OAuthAuthorization":        {sample: &domain.OAuthAuthorization{}, through: through(encodeProtoOAuthAuthorization, decodeProtoOAuthAuthorization)},
		"Reaction":                  {sample: &domain.Reaction{}, through: through(encodeProtoReaction, decodeProtoReaction)},
		"UserReactionPage":          {sample: &domain.UserReactionPage{}, omitted: map[string]string{"BlobKey": "storage-internal file location"}, through: through(encodeProtoUserReactionPage, decodeProtoUserReactionPage)},
		"Pin":                       {sample: &domain.Pin{}, through: through(encodeProtoPin, decodeProtoPin)},
		"Star":                      {sample: &domain.Star{}, omitted: map[string]string{"BlobKey": "storage-internal file location"}, through: through(encodeProtoStar, decodeProtoStar)},
		"SavedItem": {
			sample:  &domain.SavedItem{},
			omitted: map[string]string{"BlobKey": "storage-internal file location"},
			prepare: func(filled any) {
				item := filled.(*domain.SavedItem)
				item.State = domain.SavedItemInProgress
				item.SourceAvailable = true
			},
			through: through(encodeProtoSavedItem, decodeProtoSavedItem),
		},
		"SavedItemPage": {
			sample:  &domain.SavedItemPage{},
			omitted: map[string]string{"BlobKey": "storage-internal file location"},
			prepare: func(filled any) {
				page := filled.(*domain.SavedItemPage)
				for index := range page.Items {
					page.Items[index].State = domain.SavedItemInProgress
					page.Items[index].SourceAvailable = true
				}
			},
			through: through(encodeProtoSavedItemPage, decodeProtoSavedItemPage),
		},
		"Bookmark": {sample: &domain.Bookmark{}, through: through(encodeProtoBookmark, decodeProtoBookmark)},
		"Reminder": {sample: &domain.Reminder{}, through: through(encodeProtoReminder, decodeProtoReminder)},
		"LaterReminder": {
			sample: &domain.LaterReminder{},
			prepare: func(filled any) {
				reminder := filled.(*domain.LaterReminder)
				reminder.Target = domain.LaterReminderPersonal
				reminder.Channel = ""
				reminder.Recurrence = domain.ReminderMonthly
			},
			through: through(encodeProtoLaterReminder, decodeProtoLaterReminder),
		},
		"ActivityItem": {
			sample:  &domain.ActivityItem{},
			omitted: map[string]string{"BlobKey": "storage-internal file location"},
			prepare: func(filled any) {
				item := filled.(*domain.ActivityItem)
				item.Kinds = []domain.ActivityKind{domain.ActivityDM, domain.ActivityMention}
				item.SourceAvailable = true
				item.Reminder.Target = domain.LaterReminderPersonal
				item.Reminder.Channel = ""
				item.Reminder.Recurrence = domain.ReminderMonthly
			},
			through: through(encodeProtoActivityItem, decodeProtoActivityItem),
		},
		"ActivityPreferences": {
			sample: &domain.ActivityPreferences{},
			prepare: func(filled any) {
				filled.(*domain.ActivityPreferences).Layout = domain.ActivityDense
			},
			through: throughInfallible(encodeProtoActivityPreferences, decodeProtoActivityPreferences),
		},
		"ScheduledMessage": {sample: &domain.ScheduledMessage{}, through: through(encodeProtoScheduledMessage, decodeProtoScheduledMessage)},
		"ScheduledStatus":  {sample: &domain.ScheduledStatus{}, through: through(encodeProtoScheduledStatus, decodeProtoScheduledStatus)},
		"Draft":            {sample: &domain.Draft{}, through: through(encodeProtoDraft, decodeProtoDraft)},
		"DraftAttachments": {
			sample: &draftAttachmentsRoundTrip{},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := filled.(*draftAttachmentsRoundTrip)
				wire := &chatv1.Draft{Attachments: encodeProtoDraftAttachments(value.Attachments)}
				return &draftAttachmentsRoundTrip{Attachments: decodeProtoDraftAttachments(wire.GetAttachments())}, wire, nil
			},
		},
		"AssistantPrompts": {
			sample: &assistantPromptsRoundTrip{},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := filled.(*assistantPromptsRoundTrip)
				wire := &chatv1.AssistantThread{Prompts: encodeProtoAssistantPrompts(value.Prompts)}
				return &assistantPromptsRoundTrip{Prompts: decodeProtoAssistantPrompts(wire.GetPrompts())}, wire, nil
			},
		},
		"TypingSignals": {
			sample: &typingSignalsRoundTrip{},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := filled.(*typingSignalsRoundTrip)
				wire := &chatv1.TypingSignalsResponse{Signals: encodeProtoTypingSignals(value.Signals)}
				return &typingSignalsRoundTrip{Signals: decodeProtoTypingSignals(wire.GetSignals())}, wire, nil
			},
		},
		"AppDeliveryHealth": {
			sample: &domain.AppDeliveryHealth{},
			prepare: func(filled any) {
				health := filled.(*domain.AppDeliveryHealth)
				health.Surface = "http"
				health.Configured = true
			},
			through: through(encodeProtoAppDeliveryHealth, decodeProtoAppDeliveryHealth),
		},
		"DoNotDisturb":               {sample: &domain.DoNotDisturb{}, through: through(encodeProtoDoNotDisturb, decodeProtoDoNotDisturb)},
		"UserGroup":                  {sample: &domain.UserGroup{}, through: through(encodeProtoUserGroup, decodeProtoUserGroup)},
		"Call":                       {sample: &domain.Call{}, through: through(encodeProtoCall, decodeProtoCall)},
		"Canvas":                     {sample: &domain.Canvas{}, through: through(encodeProtoCanvas, decodeProtoCanvas)},
		"CanvasPage":                 {sample: &domain.CanvasPage{}, through: through(encodeProtoCanvasPage, decodeProtoCanvasPage)},
		"List":                       {sample: &domain.List{}, through: through(encodeProtoList, decodeProtoList)},
		"ListPage":                   {sample: &domain.ListPage{}, through: through(encodeProtoListPage, decodeProtoListPage)},
		"ListItem":                   {sample: &domain.ListItem{}, through: through(encodeProtoListItem, decodeProtoListItem)},
		"ListItemPage":               {sample: &domain.ListItemPage{}, through: through(encodeProtoListItemPage, decodeProtoListItemPage)},
		"ListDownload":               {sample: &domain.ListDownload{}, through: through(encodeProtoListDownload, decodeProtoListDownload)},
		"RetentionPolicy":            {sample: &domain.RetentionPolicy{}, through: through(encodeProtoRetentionPolicy, decodeProtoRetentionPolicy)},
		"AccessLog":                  {sample: &domain.AccessLog{}, through: through(encodeProtoAccessLog, decodeProtoAccessLog)},
		"SharedInvite":               {sample: &domain.SharedInvite{}, through: through(encodeProtoSharedInvite, decodeProtoSharedInvite)},
		"WorkspaceMembershipSummary": {sample: &domain.WorkspaceMembershipSummary{}, through: through(encodeProtoWorkspaceMembershipSummary, decodeProtoWorkspaceMembershipSummary)},
		"WorkspaceAnalytics": {sample: &domain.WorkspaceAnalytics{}, through: through(encodeProtoWorkspaceAnalytics, func(value *chatv1.WorkspaceAnalytics) (domain.WorkspaceAnalytics, error) {
			return decodeProtoWorkspaceAnalytics(value), nil
		})},
		"View": {sample: &domain.View{}, through: through(encodeProtoView, decodeProtoView)},
		"AppHome": {
			sample: &appHomeRoundTrip{},
			through: through(
				func(value appHomeRoundTrip) *chatv1.AppHomeResponse {
					return encodeProtoAppHome(value.App, value.View)
				},
				func(value *chatv1.AppHomeResponse) (appHomeRoundTrip, error) {
					app, view, err := decodeProtoAppHome(value)
					return appHomeRoundTrip{App: app, View: view}, err
				},
			),
		},
		"SocketModeInteraction": {sample: &domain.SocketModeInteraction{}, through: through(func(value domain.SocketModeInteraction) *chatv1.SocketModeInteraction {
			return encodeProtoSocketModeInteraction(value, true)
		}, decodeProtoSocketModeInteraction)},
		"Bot":           {sample: &domain.Bot{}, through: through(encodeProtoBot, decodeProtoBot)},
		"InviteRequest": {sample: &domain.InviteRequest{}, through: throughInfallible(encodeProtoInviteRequest, decodeProtoInviteRequest)},
		"AppApproval":   {sample: &domain.AppApproval{}, through: throughInfallible(encodeProtoAppApproval, decodeProtoAppApproval)},
		"RTMConnection": {sample: &domain.RTMConnection{}, through: throughInfallible(encodeProtoRTMConnection, decodeProtoRTMConnection)},
		"IncomingWebhook": {
			sample: &domain.IncomingWebhook{},
			omitted: map[string]string{
				"SecretHash": "the hash must never cross the boundary; only the one-time plaintext secret does, as a separate return value",
			},
			through: func(t *testing.T, filled any) (any, proto.Message, error) {
				value := *filled.(*domain.IncomingWebhook)
				wire := encodeProtoIncomingWebhook(value, "plaintext-secret")
				decodedValue, secret, err := decodeProtoIncomingWebhook(wire)
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if secret != "plaintext-secret" {
					t.Fatalf("secret = %q, want the plaintext the encoder was given", secret)
				}
				return &decodedValue, wire, nil
			},
		},
		"WorkspaceMembership": {sample: &domain.WorkspaceMembership{}, through: through(encodeProtoWorkspaceMembership, decodeProtoWorkspaceMembership)},
		// The three paginated wrappers below decode into anonymous structs rather
		// than a domain page type, so the case restates the shape. They were the
		// only converters covered nowhere: dropping NextCursor and HasMore from
		// encodeProtoPinPage left all 271 tests in this package green, and
		// pins.list would have lost pagination in the distributed composition
		// while the monolith kept it.
		"PinPage": {sample: &pinPage{}, through: func(t *testing.T, filled any) (any, proto.Message, error) {
			value := filled.(*pinPage)
			wire := encodeProtoPinPage(value.Pins, value.NextCursor, value.HasMore)
			decoded, err := decodeProtoPinPage(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			return &pinPage{Pins: decoded.Pins, NextCursor: decoded.NextCursor, HasMore: decoded.HasMore}, wire, nil
		}},
		"StarPage": {sample: &starPage{}, omitted: map[string]string{"BlobKey": "storage-internal file location"}, through: func(t *testing.T, filled any) (any, proto.Message, error) {
			value := filled.(*starPage)
			wire := encodeProtoStarPage(value.Stars, value.NextCursor, value.HasMore)
			decoded, err := decodeProtoStarPage(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			return &starPage{Stars: decoded.Stars, NextCursor: decoded.NextCursor, HasMore: decoded.HasMore}, wire, nil
		}},
		"ReactionPage": {sample: &reactionPage{}, through: func(t *testing.T, filled any) (any, proto.Message, error) {
			value := filled.(*reactionPage)
			wire := encodeProtoReactionPage(value.Reactions, value.NextCursor, value.HasMore)
			decoded, err := decodeProtoReactionPage(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			return &reactionPage{Reactions: decoded.Reactions, NextCursor: decoded.NextCursor, HasMore: decoded.HasMore}, wire, nil
		}},
		"events.Record": {sample: &events.Record{}, through: func(t *testing.T, filled any) (any, proto.Message, error) {
			wire := encodeProtoEventRecord(*filled.(*events.Record))
			record, err := decodeProtoEventRecord(wire)
			if err != nil {
				return &events.Record{}, wire, err
			}
			return &record, wire, nil
		}},
		"events.Record page": {sample: &events.Record{}, through: func(t *testing.T, filled any) (any, proto.Message, error) {
			wire := encodeProtoEvents([]events.Record{*filled.(*events.Record)})
			records, err := decodeProtoEvents(wire)
			if err != nil {
				return &events.Record{}, wire, err
			}
			if len(records) != 1 {
				t.Fatalf("decoded %d records, want 1", len(records))
			}
			return &records[0], wire, nil
		}},
	}
}

// pinPage, starPage and reactionPage restate the anonymous structs
// decodeProtoPinPage, decodeProtoStarPage and decodeProtoReactionPage return, so
// the property can be expressed for them. They are the reason those three
// converters were outside the property; the underlying fix is for
// Pins/Stars/Reactions to return a page type, which is a change to
// chatapi.Service rather than to this package.
type pinPage struct {
	Pins       []domain.Pin
	NextCursor domain.Cursor
	HasMore    bool
}

type starPage struct {
	Stars      []domain.Star
	NextCursor domain.Cursor
	HasMore    bool
}

type reactionPage struct {
	Reactions  []domain.Reaction
	NextCursor domain.Cursor
	HasMore    bool
}

type assistantPromptsRoundTrip struct {
	Prompts []domain.AssistantPrompt
}

// typingSignalsRoundTrip wraps the slice because a signal only ever crosses the
// wire inside one. The zero-time pass matters more here than for most records:
// an expiry is the whole of a signal's meaning, and a zero time that decoded to
// the year 1754 would make an expired signal read as live for two centuries.
type typingSignalsRoundTrip struct {
	Signals []domain.TypingSignal
}

// canvasGrantsRoundTrip wraps the slice because a grant only ever crosses the
// wire inside one: a sharing surface asks who a canvas is shared with, never
// about one grant on its own.
type canvasGrantsRoundTrip struct {
	Grants []domain.CanvasAccess
}

type listGrantsRoundTrip struct {
	Grants []domain.ListAccess
}

type draftAttachmentsRoundTrip struct {
	Attachments []domain.DraftAttachment
}

// TestEveryConverterPairIsExercisedByTheProperty derives the case list instead
// of trusting it.
//
// conversionCases is a hand-written map, and nothing asserted that it named
// every encodeProto/decodeProto pair in the package. Four were absent —
// including encodeProtoWorkspaceMembership, the record that carries the role
// authorising every administrative operation — so the property that exists to
// stop a converter dropping a field could not see them at all. This test reads
// both sides from source: the pairs from grpc.go and the converters the case
// list actually names, so adding a converter without a case fails here rather
// than shipping.
func TestEveryConverterPairIsExercisedByTheProperty(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "grpc.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	encoders, decoders := make(map[string]struct{}), make(map[string]struct{})
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		if name, found := strings.CutPrefix(function.Name.Name, "encodeProto"); found {
			encoders[name] = struct{}{}
		}
		if name, found := strings.CutPrefix(function.Name.Name, "decodeProto"); found {
			decoders[name] = struct{}{}
		}
	}
	if len(encoders) == 0 {
		t.Fatal("no converters discovered in grpc.go; the source scan is broken")
	}

	named := convertersNamedByTheCaseList(t)
	for name := range encoders {
		if _, symmetric := decoders[name]; !symmetric {
			// An encoder with no decoder of its own is read by another decoder
			// (encodeProtoProfile is read by decodeProtoUser) and is covered
			// through it.
			continue
		}
		if _, exercised := named["encodeProto"+name]; !exercised {
			t.Errorf("encodeProto%s/decodeProto%s round-trip no domain value in conversionCases: add a case, so a field either converter drops is caught", name, name)
		}
		if _, exercised := named["decodeProto"+name]; !exercised {
			t.Errorf("decodeProto%s is never called by conversionCases: a case that encodes without decoding proves nothing", name)
		}
	}
}

// convertersNamedByTheCaseList reads the converter names conversionCases
// mentions out of this file's own source, so the two sides of the comparison are
// both derived and neither is a list to maintain.
func convertersNamedByTheCaseList(t *testing.T) map[string]struct{} {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "roundtrip_test.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	named := make(map[string]struct{})
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "conversionCases" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok {
				named[identifier.Name] = struct{}{}
			}
			return true
		})
	}
	if len(named) == 0 {
		t.Fatal("conversionCases was not found in roundtrip_test.go; the source scan is broken")
	}
	return named
}

// through builds the round trip for a symmetric converter pair. Naming the two
// functions rather than writing the call out per case is what keeps a case a
// single line, so adding a converter to this test costs nothing.
func through[Value any, Proto proto.Message](encode func(Value) Proto, decode func(Proto) (Value, error)) func(*testing.T, any) (any, proto.Message, error) {
	return func(t *testing.T, filled any) (any, proto.Message, error) {
		t.Helper()
		wire := encode(*filled.(*Value))
		result, err := decode(wire)
		return &result, wire, err
	}
}

// throughInfallible is through for the three converters whose decoder cannot fail.
func throughInfallible[Value any, Proto proto.Message](encode func(Value) Proto, decode func(Proto) Value) func(*testing.T, any) (any, proto.Message, error) {
	return func(t *testing.T, filled any) (any, proto.Message, error) {
		t.Helper()
		wire := encode(*filled.(*Value))
		return ptr(decode(wire)), wire, nil
	}
}

func ptr[Value any](value Value) *Value { return &value }

// fillTime is the instant every timestamp is filled with. It is whole-second and
// UTC; see the file comment.
var fillTime = time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

// fillValue populates every field reachable from value with a distinctive
// non-zero value.
//
// A few fields carry a value from a closed set rather than an arbitrary string,
// because the surrounding code normalises them and a normalised value is not the
// value that was sent — which would look like a dropped field without being one.
// boolFillRuns is how many times a case is filled and round-tripped.
//
// A bool can only take two values, so no single filling can distinguish more
// than two boolean fields. filler numbers the bools in traversal order and uses
// bit `run` of that number, so across boolFillRuns runs any two distinct bool
// fields differ in at least one of them, for up to 2^boolFillRuns bools in one
// value. That is what makes a swap between two sibling flags detectable rather
// than a coin toss: no domain type on this contract carries anywhere near 64.
const boolFillRuns = 6

// filler carries the state fillValue needs across one filling: which run this
// is, and how many bools have been assigned so far.
type filler struct {
	run   int
	bools int
}

func (f *filler) nextBool() bool {
	index := f.bools
	f.bools++
	return (index>>f.run)&1 == 1
}

// Numeric scalars are derived from a hash of the whole field path rather than
// from its length: two fields whose names happened to be the same length used to
// receive the same integer, so a converter that swapped them was
// indistinguishable from one that carried them correctly.
func fillValue(t *testing.T, value reflect.Value, path string, state *filler) {
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
			fillValue(t, value.Field(index), path+"."+field.Name, state)
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
		value.SetBool(state.nextBool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(int64(7 + pathHash(path)%13))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(uint64(11 + pathHash(path)%17))
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
			fillValue(t, slice.Index(index), fmt.Sprintf("%s[%d]", path, index), state)
		}
		value.Set(slice)
	case reflect.Map:
		result := reflect.MakeMap(value.Type())
		for index := 0; index < 2; index++ {
			key := reflect.New(value.Type().Key()).Elem()
			fillValue(t, key, fmt.Sprintf("%s.key%d", path, index), state)
			element := reflect.New(value.Type().Elem()).Elem()
			fillValue(t, element, fmt.Sprintf("%s.value%d", path, index), state)
			result.SetMapIndex(key, element)
		}
		value.Set(result)
	case reflect.Pointer:
		allocated := reflect.New(value.Type().Elem())
		fillValue(t, allocated.Elem(), path, state)
		value.Set(allocated)
	default:
		t.Fatalf("%s: the filler does not handle %s; extend it rather than skipping the field", path, value.Kind())
	}
}

func pathHash(path string) int {
	digest := fnv.New32a()
	_, _ = digest.Write([]byte(path))
	return int(digest.Sum32() % (1 << 24))
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
	case "Kind":
		// A call's kind is a closed set the decoder validates, and the external
		// kind is the one whose other required fields this generator also fills.
		return string(domain.CallKindExternal)
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
