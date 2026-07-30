package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

const maxAppDatastoreItemBytes = 400 << 10

var (
	ErrAppNotHosted          = errors.New("application is not Slack-hosted")
	ErrAppDatastoreNotFound  = errors.New("application datastore was not found")
	ErrInvalidDatastoreItem  = errors.New("application datastore item is invalid")
	ErrInvalidDatastoreQuery = errors.New("application datastore query is invalid")
)

// PutAppDatastoreItems replaces or merges one to 25 items in a declared
// Slack-hosted app datastore. Items cross the process boundary as canonical
// JSON so local and gRPC-backed deployments apply exactly the same validation.
func (m Messages) PutAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, rawItems []string, merge bool) ([]string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	snapshot, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return nil, err
	}
	if len(rawItems) == 0 || len(rawItems) > 25 {
		return nil, fmt.Errorf("%w: a request must contain 1 to 25 items", ErrInvalidDatastoreItem)
	}

	items := make([]map[string]any, len(rawItems))
	ids := make([]string, len(rawItems))
	seen := make(map[string]struct{}, len(rawItems))
	for index, raw := range rawItems {
		item, id, err := decodeAppDatastoreItem(raw, datastoreDefinition)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate primary key %q", ErrInvalidDatastoreItem, id)
		}
		seen[id] = struct{}{}
		items[index], ids[index] = item, id
	}

	now := time.Now().UTC()
	values := make([]domain.AppDatastoreItem, len(items))
	canonical := make([]string, len(items))
	for index, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDatastoreItem, err)
		}
		if len(encoded) > maxAppDatastoreItemBytes {
			return nil, fmt.Errorf("%w: item exceeds 400 KiB", ErrInvalidDatastoreItem)
		}
		canonical[index] = string(encoded)
		values[index] = domain.AppDatastoreItem{
			AppID: snapshot.App.ID, WorkspaceID: workspaceID, Datastore: datastoreDefinition.Name,
			ID: ids[index], Item: canonical[index], UpdatedAt: now,
		}
	}
	if merge {
		merged, err := m.Store.MergeAppDatastoreItems(ctx, values)
		if err != nil {
			return nil, err
		}
		for index, value := range merged {
			var item map[string]any
			decoder := json.NewDecoder(strings.NewReader(value.Item))
			decoder.UseNumber()
			if err := decoder.Decode(&item); err != nil {
				return nil, fmt.Errorf("read merged datastore item: %w", err)
			}
			if err := validateAppDatastoreObject(item, datastoreDefinition); err != nil {
				return nil, fmt.Errorf("merged item %d: %w", index, err)
			}
			canonical[index] = value.Item
		}
	} else if err := m.Store.PutAppDatastoreItems(ctx, values); err != nil {
		return nil, err
	}
	return canonical, nil
}

func (m Messages) GetAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) ([]string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	_, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return nil, err
	}
	if err := validateDatastoreIDs(ids); err != nil {
		return nil, err
	}
	values, err := m.Store.GetAppDatastoreItems(ctx, appID, workspaceID, datastoreDefinition.Name, ids)
	if err != nil {
		return nil, err
	}
	items := make([]string, len(values))
	for index, value := range values {
		items[index] = value.Item
	}
	return items, nil
}

// QueryAppDatastoreItems preserves Slack's scan semantics: limit bounds rows
// evaluated before the filter is applied, so a short or empty result can still
// carry a next cursor. The expression grammar is the complete operator set
// Slack documents for hosted datastores, rather than a SQL-shaped approximation.
func (m Messages) QueryAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, query domain.AppDatastoreQuery) (domain.AppDatastoreQueryPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.AppDatastoreQueryPage{}, err
	}
	_, definition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return domain.AppDatastoreQueryPage{}, err
	}
	if query.Page.Limit < 1 || query.Page.Limit > 1000 || query.Page.Descending {
		return domain.AppDatastoreQueryPage{}, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalidDatastoreQuery)
	}
	matches, err := compileDatastoreExpression(query, definition)
	if err != nil {
		return domain.AppDatastoreQueryPage{}, err
	}
	values, hasMore, next, err := m.Store.ListAppDatastoreItems(ctx, appID, workspaceID, definition.Name, query.Page)
	if err != nil {
		return domain.AppDatastoreQueryPage{}, err
	}
	const scanByteLimit = 1 << 20
	evaluatedBytes := 0
	evaluated := len(values)
	for index, value := range values {
		if index > 0 && evaluatedBytes+len(value.Item) > scanByteLimit {
			evaluated = index
			hasMore = true
			next, err = domain.NewListCursor(values[index-1].ID)
			if err != nil {
				return domain.AppDatastoreQueryPage{}, err
			}
			break
		}
		evaluatedBytes += len(value.Item)
	}
	result := domain.AppDatastoreQueryPage{Items: make([]string, 0, evaluated), HasMore: hasMore, NextCursor: next}
	for _, value := range values[:evaluated] {
		var item map[string]any
		decoder := json.NewDecoder(strings.NewReader(value.Item))
		decoder.UseNumber()
		if err := decoder.Decode(&item); err != nil {
			return domain.AppDatastoreQueryPage{}, fmt.Errorf("decode stored datastore item: %w", err)
		}
		if matches(item) {
			result.Items = append(result.Items, value.Item)
		}
	}
	if !result.HasMore {
		result.NextCursor = ""
	}
	return result, nil
}

func (m Messages) CountAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, query domain.AppDatastoreQuery) (int, error) {
	query.Page = domain.PageRequest{Limit: 1000}
	total := 0
	for {
		page, err := m.QueryAppDatastoreItems(ctx, workspaceID, userID, appID, datastore, query)
		if err != nil {
			return 0, err
		}
		total += len(page.Items)
		if !page.HasMore {
			return total, nil
		}
		query.Page.Cursor = page.NextCursor
	}
}

func (m Messages) DeleteAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	_, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return err
	}
	if err := validateDatastoreIDs(ids); err != nil {
		return err
	}
	return m.Store.DeleteAppDatastoreItems(ctx, appID, workspaceID, datastoreDefinition.Name, ids)
}

func (m Messages) appDatastore(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, name string) (domain.AppManifestSnapshot, appmanifest.Datastore, error) {
	name = strings.TrimSpace(name)
	if appID == "" || name == "" {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppDatastoreNotFound
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, appID)
	if err != nil {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, err
	}
	if !parsed.IsHosted || parsed.FunctionRuntime != "slack" {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppNotHosted
	}
	definition, exists := parsed.Datastores[name]
	if !exists {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppDatastoreNotFound
	}
	return snapshot, definition, nil
}

func validateDatastoreIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > 25 {
		return fmt.Errorf("%w: a request must contain 1 to 25 ids", ErrInvalidDatastoreItem)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: id is required", ErrInvalidDatastoreItem)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate id %q", ErrInvalidDatastoreItem, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func decodeAppDatastoreItem(raw string, definition appmanifest.Datastore) (map[string]any, string, error) {
	if len(raw) == 0 || len(raw) > maxAppDatastoreItemBytes {
		return nil, "", fmt.Errorf("%w: item must be a JSON object no larger than 400 KiB", ErrInvalidDatastoreItem)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var item map[string]any
	if err := decoder.Decode(&item); err != nil || item == nil {
		return nil, "", fmt.Errorf("%w: item must be a JSON object", ErrInvalidDatastoreItem)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, "", fmt.Errorf("%w: item contains multiple JSON values", ErrInvalidDatastoreItem)
	} else if !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("%w: item contains invalid trailing JSON", ErrInvalidDatastoreItem)
	}
	if err := validateAppDatastoreObject(item, definition); err != nil {
		return nil, "", err
	}
	id, _ := item[definition.PrimaryKey].(string)
	return item, id, nil
}

func validateAppDatastoreObject(item map[string]any, definition appmanifest.Datastore) error {
	for name, value := range item {
		attribute, exists := definition.Attributes[name]
		if !exists {
			return fmt.Errorf("%w: attribute %q is not declared", ErrInvalidDatastoreItem, name)
		}
		if !validAppDatastoreValue(value, attribute.Type) {
			return fmt.Errorf("%w: attribute %q does not match %s", ErrInvalidDatastoreItem, name, attribute.Type)
		}
	}
	id, exists := item[definition.PrimaryKey].(string)
	if !exists || strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: primary key %q must be a non-empty string", ErrInvalidDatastoreItem, definition.PrimaryKey)
	}
	return nil
}

func validAppDatastoreValue(value any, declaredType string) bool {
	kind := strings.ToLower(strings.TrimSpace(declaredType))
	if slash := strings.LastIndex(kind, "/"); slash >= 0 {
		kind = kind[slash+1:]
	}
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean", "bool":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := number.Float64()
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	case "timestamp":
		switch timestamp := value.(type) {
		case string:
			return strings.TrimSpace(timestamp) != ""
		case json.Number:
			_, err := timestamp.Int64()
			return err == nil
		default:
			return false
		}
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		// Slack schema references cover richer platform types whose wire value is
		// defined by the referenced schema. Rejecting a structurally valid value
		// here would make the runtime less capable than the accepted manifest.
		return value != nil
	}
}

type datastoreExpressionToken struct {
	kind string
	text string
}

type datastoreExpressionParser struct {
	tokens     []datastoreExpressionToken
	index      int
	attributes map[string]string
	values     map[string]any
	primaryKey string
}

type datastoreItemPredicate func(map[string]any) bool

func compileDatastoreExpression(query domain.AppDatastoreQuery, definition appmanifest.Datastore) (datastoreItemPredicate, error) {
	expression := strings.TrimSpace(query.Expression)
	if expression == "" {
		if strings.TrimSpace(query.ExpressionAttributes) != "" || strings.TrimSpace(query.ExpressionValues) != "" {
			return nil, fmt.Errorf("%w: expression is required when expression maps are provided", ErrInvalidDatastoreQuery)
		}
		return func(map[string]any) bool { return true }, nil
	}
	attributes := make(map[string]string)
	values := make(map[string]any)
	if err := decodeDatastoreExpressionObject(query.ExpressionAttributes, &attributes); err != nil {
		return nil, err
	}
	if err := decodeDatastoreExpressionObject(query.ExpressionValues, &values); err != nil {
		return nil, err
	}
	for alias, attribute := range attributes {
		if !strings.HasPrefix(alias, "#") || alias == "#" {
			return nil, fmt.Errorf("%w: expression attribute aliases must begin with #", ErrInvalidDatastoreQuery)
		}
		if _, exists := definition.Attributes[attribute]; !exists {
			return nil, fmt.Errorf("%w: attribute %q is not declared", ErrInvalidDatastoreQuery, attribute)
		}
	}
	for alias := range values {
		if !strings.HasPrefix(alias, ":") || alias == ":" {
			return nil, fmt.Errorf("%w: expression value aliases must begin with :", ErrInvalidDatastoreQuery)
		}
	}
	tokens, err := tokenizeDatastoreExpression(expression)
	if err != nil {
		return nil, err
	}
	parser := datastoreExpressionParser{tokens: tokens, attributes: attributes, values: values, primaryKey: definition.PrimaryKey}
	predicate, err := parser.parseConjunction()
	if err != nil {
		return nil, err
	}
	if parser.index != len(parser.tokens) {
		return nil, fmt.Errorf("%w: unexpected token %q", ErrInvalidDatastoreQuery, parser.tokens[parser.index].text)
	}
	return predicate, nil
}

func decodeDatastoreExpressionObject(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: expression maps must be JSON objects", ErrInvalidDatastoreQuery)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: expression maps contain trailing data", ErrInvalidDatastoreQuery)
	}
	return nil
}

func tokenizeDatastoreExpression(expression string) ([]datastoreExpressionToken, error) {
	var result []datastoreExpressionToken
	for index := 0; index < len(expression); {
		if expression[index] == ' ' || expression[index] == '\t' || expression[index] == '\n' || expression[index] == '\r' {
			index++
			continue
		}
		switch expression[index] {
		case '(', ')', ',':
			result = append(result, datastoreExpressionToken{kind: string(expression[index]), text: string(expression[index])})
			index++
		case '=', '<', '>':
			start := index
			index++
			if index < len(expression) && expression[index] == '=' && expression[start] != '=' {
				index++
			}
			result = append(result, datastoreExpressionToken{kind: "operator", text: expression[start:index]})
		default:
			start := index
			for index < len(expression) {
				character := expression[index]
				if character == ' ' || character == '\t' || character == '\n' || character == '\r' ||
					character == '(' || character == ')' || character == ',' || character == '=' || character == '<' || character == '>' {
					break
				}
				index++
			}
			if start == index {
				return nil, fmt.Errorf("%w: invalid expression character", ErrInvalidDatastoreQuery)
			}
			text := expression[start:index]
			result = append(result, datastoreExpressionToken{kind: "word", text: text})
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: expression is empty", ErrInvalidDatastoreQuery)
	}
	return result, nil
}

func (p *datastoreExpressionParser) parseConjunction() (datastoreItemPredicate, error) {
	predicates := make([]datastoreItemPredicate, 0, 2)
	for {
		predicate, err := p.parseClause()
		if err != nil {
			return nil, err
		}
		predicates = append(predicates, predicate)
		if !p.consumeWord("AND") {
			break
		}
	}
	return func(item map[string]any) bool {
		for _, predicate := range predicates {
			if !predicate(item) {
				return false
			}
		}
		return true
	}, nil
}

func (p *datastoreExpressionParser) parseClause() (datastoreItemPredicate, error) {
	if p.peekWord("contains") || p.peekWord("begins_with") {
		function := strings.ToLower(p.take().text)
		if !p.consumeKind("(") {
			return nil, fmt.Errorf("%w: %s requires parentheses", ErrInvalidDatastoreQuery, function)
		}
		attribute, err := p.parseAttribute()
		if err != nil {
			return nil, err
		}
		if !p.consumeKind(",") {
			return nil, fmt.Errorf("%w: %s requires two arguments", ErrInvalidDatastoreQuery, function)
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if !p.consumeKind(")") {
			return nil, fmt.Errorf("%w: %s has an unclosed argument list", ErrInvalidDatastoreQuery, function)
		}
		return func(item map[string]any) bool {
			left, ok := item[attribute].(string)
			right, valid := value.(string)
			if !ok || !valid {
				return false
			}
			if function == "contains" {
				return strings.Contains(left, right)
			}
			return strings.HasPrefix(left, right)
		}, nil
	}
	attribute, err := p.parseAttribute()
	if err != nil {
		return nil, err
	}
	if p.consumeWord("BETWEEN") {
		lower, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if !p.consumeWord("AND") {
			return nil, fmt.Errorf("%w: BETWEEN requires AND", ErrInvalidDatastoreQuery)
		}
		upper, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return func(item map[string]any) bool {
			value, exists := item[attribute]
			if !exists {
				return false
			}
			lowerComparison, lowerOK := compareDatastoreValues(value, lower)
			upperComparison, upperOK := compareDatastoreValues(value, upper)
			return lowerOK && upperOK && lowerComparison >= 0 && upperComparison <= 0
		}, nil
	}
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != "operator" {
		return nil, fmt.Errorf("%w: comparison operator is required", ErrInvalidDatastoreQuery)
	}
	operator := p.take().text
	right, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return func(item map[string]any) bool {
		left, exists := item[attribute]
		if !exists {
			return false
		}
		if operator == "=" {
			return datastoreValuesEqual(left, right)
		}
		comparison, ok := compareDatastoreValues(left, right)
		if !ok {
			return false
		}
		switch operator {
		case "<":
			return comparison < 0
		case "<=":
			return comparison <= 0
		case ">":
			return comparison > 0
		case ">=":
			return comparison >= 0
		default:
			return false
		}
	}, nil
}

func (p *datastoreExpressionParser) parseAttribute() (string, error) {
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != "word" {
		return "", fmt.Errorf("%w: expression attribute is required", ErrInvalidDatastoreQuery)
	}
	alias := p.take().text
	attribute, exists := p.attributes[alias]
	if !exists {
		return "", fmt.Errorf("%w: expression attribute %q is not defined", ErrInvalidDatastoreQuery, alias)
	}
	if attribute == p.primaryKey {
		return "", fmt.Errorf("%w: expressions cannot contain the primary key", ErrInvalidDatastoreQuery)
	}
	return attribute, nil
}

func (p *datastoreExpressionParser) parseValue() (any, error) {
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != "word" {
		return nil, fmt.Errorf("%w: expression value is required", ErrInvalidDatastoreQuery)
	}
	alias := p.take().text
	value, exists := p.values[alias]
	if !exists {
		return nil, fmt.Errorf("%w: expression value %q is not defined", ErrInvalidDatastoreQuery, alias)
	}
	return value, nil
}

func (p *datastoreExpressionParser) peekWord(word string) bool {
	return p.index < len(p.tokens) && p.tokens[p.index].kind == "word" && strings.EqualFold(p.tokens[p.index].text, word)
}

func (p *datastoreExpressionParser) consumeWord(word string) bool {
	if !p.peekWord(word) {
		return false
	}
	p.index++
	return true
}

func (p *datastoreExpressionParser) consumeKind(kind string) bool {
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != kind {
		return false
	}
	p.index++
	return true
}

func (p *datastoreExpressionParser) take() datastoreExpressionToken {
	token := p.tokens[p.index]
	p.index++
	return token
}

func datastoreValuesEqual(left, right any) bool {
	if comparison, ok := compareDatastoreValues(left, right); ok {
		return comparison == 0
	}
	return reflect.DeepEqual(left, right)
}

func compareDatastoreValues(left, right any) (int, bool) {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return 0, false
		}
		leftValue, leftErr := leftNumber.Float64()
		rightValue, rightErr := rightNumber.Float64()
		if leftErr != nil || rightErr != nil {
			return 0, false
		}
		switch {
		case leftValue < rightValue:
			return -1, true
		case leftValue > rightValue:
			return 1, true
		default:
			return 0, true
		}
	}
	leftString, leftIsString := left.(string)
	rightString, rightIsString := right.(string)
	if !leftIsString || !rightIsString {
		return 0, false
	}
	return strings.Compare(leftString, rightString), true
}
