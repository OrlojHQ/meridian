package harnessadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// Form-mode ACP elicitations are bounded before any prompt reaches the
// operator. A request outside these bounds is declined.
const (
	acpMaxElicitFields   = 16
	acpMaxFieldNameBytes = 256
	acpMaxPatternBytes   = 1024
)

// Operator answers that steer an elicitation rather than fill a field.
const (
	acpElicitAccept  = "Accept"
	acpElicitDecline = "Decline"
	acpElicitCancel  = "Cancel"
	acpElicitSkip    = "Skip"
	acpElicitYes     = "Yes"
	acpElicitNo      = "No"
	// acpElicitOmit leaves an optional text field out of the content.
	acpElicitOmit = "-"
)

type acpChoice struct {
	value string
	label string
}

// acpField is one requested property. Choice fields (a single-select string
// or a boolean) become permission prompts with one button per choice; every
// other field becomes an input prompt whose answer is checked here before it
// reaches the agent.
type acpField struct {
	name        string
	label       string
	description string
	kind        string
	required    bool
	format      string
	choices     []acpChoice
	minLength   int
	maxLength   int
	pattern     *regexp.Regexp
	minimum     *float64
	maximum     *float64
	minItems    int
	maxItems    int
}

// buttons reports whether the field is asked with one button per choice.
func (f *acpField) buttons() bool {
	return f.kind == "boolean" || f.kind == "string" && len(f.choices) != 0 &&
		len(f.choices)+2 <= adapterproto.MaxOptions
}

// acpElicitation walks a form one field at a time, so declining any field
// ends the request without leaving other prompts open.
type acpElicitation struct {
	message string
	fields  []acpField
	next    int
	content map[string]any
	labels  map[string]string
}

// elicit answers an elicitation/create request. Only session-scoped form
// requests reach the operator; URL flows, request-scoped forms, and schemas
// Meridian cannot ask faithfully are declined.
func (s *acpSession) elicit(rawID json.RawMessage, key string, raw json.RawMessage) error {
	var params struct {
		SessionID       string          `json:"sessionId"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return s.refuse(rawID, acpErrorInvalidParams, "elicitation request is malformed")
	}
	if s.cancelled || params.SessionID == "" || params.SessionID != s.sessionID {
		return s.reply(rawID, map[string]any{"action": "cancel"})
	}
	if !s.config.Permissions || params.Mode != "form" {
		return s.reply(rawID, map[string]any{"action": "decline"})
	}
	fields, err := acpElicitFields(params.RequestedSchema)
	if err != nil {
		return s.reply(rawID, map[string]any{"action": "decline"})
	}
	form := &acpElicitation{
		message: acpText(strings.TrimSpace(params.Message), adapterproto.MaxToolSummaryBytes/2),
		fields:  fields, content: map[string]any{},
	}
	if form.message == "" {
		form.message = "The agent asks for input"
	}
	return s.askElicitation(&acpPermission{rpcKey: key, rpcID: rawID, elicit: form}, "")
}

// askElicitation writes the prompt for the form's next field, or the single
// confirmation of a form without fields. retry explains why the previous
// answer to the same field was not accepted.
func (s *acpSession) askElicitation(prompt *acpPermission, retry string) error {
	form := prompt.elicit
	s.sequence++
	frameID := "acp-elicitation-" + strconv.Itoa(s.sequence)
	s.prompts[frameID] = prompt
	s.byRPC[prompt.rpcKey] = frameID
	if len(form.fields) == 0 {
		prompt.input = false
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindPermissionRequest, ID: frameID,
			Permission: &adapterproto.Permission{
				Kind: "question", Summary: form.message,
				Options: []string{acpElicitAccept, acpElicitDecline, acpElicitCancel},
			},
		})
	}
	field := &form.fields[form.next]
	question := field.label
	if field.description != "" {
		question += "\n" + field.description
	}
	if len(form.fields) > 1 {
		question = fmt.Sprintf("(%d of %d) %s", form.next+1, len(form.fields), question)
	}
	summary := form.message + "\n\n" + question
	if field.buttons() {
		prompt.input = false
		options, labels := field.buttonLabels()
		form.labels = labels
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindPermissionRequest, ID: frameID,
			Permission: &adapterproto.Permission{
				Kind: "question", Summary: acpText(summary, adapterproto.MaxToolSummaryBytes), Options: options,
			},
		})
	}
	prompt.input = true
	if hint := field.hint(); hint != "" {
		summary += "\n" + hint
	}
	if retry != "" {
		summary = "That answer was not accepted: " + retry + ".\n\n" + summary
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindInputRequest, ID: frameID,
		Input: &adapterproto.Input{Prompt: acpText(summary, adapterproto.MaxToolSummaryBytes)},
	})
}

// answerElicitation applies one operator answer: a button choice, or typed
// input. The frame that carried the question is already forgotten.
func (s *acpSession) answerElicitation(prompt *acpPermission, choice string, input *string) error {
	form := prompt.elicit
	if len(form.fields) == 0 {
		switch choice {
		case acpElicitAccept:
			return s.reply(prompt.rpcID, map[string]any{"action": "accept", "content": map[string]any{}})
		case acpElicitCancel:
			return s.reply(prompt.rpcID, map[string]any{"action": "cancel"})
		default:
			return s.reply(prompt.rpcID, map[string]any{"action": "decline"})
		}
	}
	field := &form.fields[form.next]
	if input == nil {
		value, selected := form.labels[choice]
		switch {
		case choice == acpElicitSkip && !field.required:
		case !selected:
			// Decline, or any choice the question did not offer.
			return s.reply(prompt.rpcID, map[string]any{"action": "decline"})
		case field.kind == "boolean":
			form.content[field.name] = value == "true"
		default:
			form.content[field.name] = value
		}
	} else {
		value, problem := field.parse(*input)
		if problem != "" {
			return s.askElicitation(prompt, problem)
		}
		if value != nil {
			form.content[field.name] = value
		}
	}
	form.next++
	if form.next < len(form.fields) {
		return s.askElicitation(prompt, "")
	}
	return s.reply(prompt.rpcID, map[string]any{"action": "accept", "content": form.content})
}

// buttonLabels returns the field's buttons and the value each choice
// button stands for. A choice whose label collides with a control button or
// an earlier choice is numbered.
func (f *acpField) buttonLabels() ([]string, map[string]string) {
	labels := map[string]string{}
	var options []string
	reserved := map[string]bool{
		strings.ToLower(acpElicitSkip): !f.required, strings.ToLower(acpElicitDecline): true,
	}
	choices := f.choices
	if f.kind == "boolean" {
		choices = []acpChoice{{value: "true", label: acpElicitYes}, {value: "false", label: acpElicitNo}}
	}
	for index, choice := range choices {
		label := acpLine(choice.label, adapterproto.MaxOptionBytes-16)
		if label == "" || reserved[strings.ToLower(label)] || labels[label] != "" {
			label = strings.TrimSpace(label + " (" + strconv.Itoa(index+1) + ")")
		}
		labels[label] = choice.value
		options = append(options, label)
	}
	if !f.required {
		options = append(options, acpElicitSkip)
	}
	return append(options, acpElicitDecline), labels
}

// hint says what a typed answer must look like.
func (f *acpField) hint() string {
	var parts []string
	switch f.kind {
	case "string":
		if len(f.choices) != 0 {
			parts = append(parts, "Reply with one of: "+acpChoiceList(f.choices)+".")
		}
		switch f.format {
		case "email":
			parts = append(parts, "Reply with an email address.")
		case "uri":
			parts = append(parts, "Reply with a URL.")
		case "date":
			parts = append(parts, "Reply with a date (YYYY-MM-DD).")
		case "date-time":
			parts = append(parts, "Reply with a date and time (RFC 3339).")
		}
		if f.minLength > 0 && f.maxLength > 0 {
			parts = append(parts, fmt.Sprintf("Use %d to %d characters.", f.minLength, f.maxLength))
		} else if f.minLength > 0 {
			parts = append(parts, fmt.Sprintf("Use at least %d characters.", f.minLength))
		} else if f.maxLength > 0 {
			parts = append(parts, fmt.Sprintf("Use at most %d characters.", f.maxLength))
		}
	case "number", "integer":
		noun := "a number"
		if f.kind == "integer" {
			noun = "a whole number"
		}
		switch {
		case f.minimum != nil && f.maximum != nil:
			noun += fmt.Sprintf(" from %s to %s", acpNumber(*f.minimum), acpNumber(*f.maximum))
		case f.minimum != nil:
			noun += " of at least " + acpNumber(*f.minimum)
		case f.maximum != nil:
			noun += " of at most " + acpNumber(*f.maximum)
		}
		parts = append(parts, "Reply with "+noun+".")
	case "array":
		parts = append(parts, "Choose from: "+acpChoiceList(f.choices)+". Separate choices with commas.")
		if f.minItems > 0 {
			parts = append(parts, fmt.Sprintf("Choose at least %d.", f.minItems))
		}
		if f.maxItems > 0 {
			parts = append(parts, fmt.Sprintf("Choose at most %d.", f.maxItems))
		}
	}
	if !f.required {
		parts = append(parts, "Optional: reply "+acpElicitOmit+" to leave it out.")
	}
	return strings.Join(parts, " ")
}

// parse checks a typed answer against the field's schema. It returns the
// JSON value to send, nil to leave an optional field out, or why the answer
// does not fit.
func (f *acpField) parse(answer string) (any, string) {
	answer = strings.TrimSpace(answer)
	if answer == acpElicitOmit && !f.required {
		return nil, ""
	}
	if answer == "" {
		return nil, "an answer is required"
	}
	switch f.kind {
	case "number", "integer":
		var number float64
		if f.kind == "integer" {
			value, err := strconv.ParseInt(answer, 10, 64)
			if err != nil {
				return nil, "expected a whole number"
			}
			number = float64(value)
		} else {
			value, err := strconv.ParseFloat(answer, 64)
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return nil, "expected a number"
			}
			number = value
		}
		if f.minimum != nil && number < *f.minimum || f.maximum != nil && number > *f.maximum {
			return nil, "the number is out of range"
		}
		if f.kind == "integer" {
			return int64(number), ""
		}
		return number, ""
	case "array":
		var values []string
		seen := map[string]bool{}
		for _, part := range strings.Split(answer, ",") {
			value, ok := acpMatchChoice(f.choices, strings.TrimSpace(part))
			if !ok {
				return nil, fmt.Sprintf("%q is not one of the choices", acpLine(strings.TrimSpace(part), 64))
			}
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
		if len(values) < f.minItems || f.maxItems > 0 && len(values) > f.maxItems {
			return nil, "the number of choices is out of range"
		}
		return values, ""
	}
	if len(f.choices) != 0 {
		value, ok := acpMatchChoice(f.choices, answer)
		if !ok {
			return nil, "the answer is not one of the choices"
		}
		return value, ""
	}
	length := utf8.RuneCountInString(answer)
	if length < f.minLength || f.maxLength > 0 && length > f.maxLength {
		return nil, "the answer's length is out of range"
	}
	switch f.format {
	case "email":
		at := strings.LastIndex(answer, "@")
		if at <= 0 || at == len(answer)-1 || strings.ContainsAny(answer, " \t") {
			return nil, "expected an email address"
		}
	case "uri":
		parsed, err := url.Parse(answer)
		if err != nil || parsed.Scheme == "" {
			return nil, "expected a URL"
		}
	case "date":
		if _, err := time.Parse(time.DateOnly, answer); err != nil {
			return nil, "expected a date (YYYY-MM-DD)"
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, answer); err != nil {
			return nil, "expected a date and time (RFC 3339)"
		}
	}
	if f.pattern != nil && !f.pattern.MatchString(answer) {
		return nil, "the answer does not match the expected pattern"
	}
	return answer, ""
}

func acpMatchChoice(choices []acpChoice, answer string) (string, bool) {
	for _, choice := range choices {
		if answer == choice.value || strings.EqualFold(answer, choice.label) {
			return choice.value, true
		}
	}
	return "", false
}

func acpChoiceList(choices []acpChoice) string {
	labels := make([]string, 0, len(choices))
	for _, choice := range choices {
		labels = append(labels, choice.label)
	}
	return strings.Join(labels, ", ")
}

func acpNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// acpElicitFields reads the requested schema's properties in their declared
// order. Unknown property types, unsupported shapes, and anything past the
// bounds reject the whole schema.
func acpElicitFields(raw json.RawMessage) ([]acpField, error) {
	var schema struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "" && schema.Type != "object" {
		return nil, errors.New("schema is not an object")
	}
	if len(schema.Properties) == 0 || string(schema.Properties) == "null" {
		return nil, nil
	}
	names, properties, err := orderedObject(schema.Properties)
	if err != nil || len(names) > acpMaxElicitFields {
		return nil, errors.New("schema properties are invalid")
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	fields := make([]acpField, 0, len(names))
	for _, name := range names {
		if name == "" || len(name) > acpMaxFieldNameBytes || !utf8.ValidString(name) {
			return nil, errors.New("property name is invalid")
		}
		field, err := acpElicitField(name, properties[name], required[name])
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func acpElicitField(name string, raw json.RawMessage, required bool) (acpField, error) {
	var property struct {
		Type        string            `json:"type"`
		Title       string            `json:"title"`
		Description string            `json:"description"`
		Format      string            `json:"format"`
		Enum        []json.RawMessage `json:"enum"`
		EnumNames   []string          `json:"enumNames"`
		OneOf       []acpConst        `json:"oneOf"`
		MinLength   *int              `json:"minLength"`
		MaxLength   *int              `json:"maxLength"`
		Pattern     string            `json:"pattern"`
		Minimum     *float64          `json:"minimum"`
		Maximum     *float64          `json:"maximum"`
		MinItems    *int              `json:"minItems"`
		MaxItems    *int              `json:"maxItems"`
		Items       *struct {
			Type  string            `json:"type"`
			Enum  []json.RawMessage `json:"enum"`
			AnyOf []acpConst        `json:"anyOf"`
			OneOf []acpConst        `json:"oneOf"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &property) != nil {
		return acpField{}, errors.New("property is malformed")
	}
	field := acpField{
		name: name, kind: property.Type, required: required,
		label:       acpLine(defaultString(strings.TrimSpace(property.Title), name), 256),
		description: acpText(strings.TrimSpace(property.Description), 2048),
		minimum:     property.Minimum, maximum: property.Maximum,
	}
	nonNegative := func(value *int) (int, error) {
		if value == nil {
			return 0, nil
		}
		if *value < 0 {
			return 0, errors.New("bound is negative")
		}
		return *value, nil
	}
	var err error
	switch property.Type {
	case "string":
		switch property.Format {
		case "", "email", "uri", "date", "date-time":
			field.format = property.Format
		default:
			return acpField{}, errors.New("string format is unsupported")
		}
		if field.choices, err = acpChoices(property.Enum, property.EnumNames, property.OneOf); err != nil {
			return acpField{}, err
		}
		if field.minLength, err = nonNegative(property.MinLength); err != nil {
			return acpField{}, err
		}
		if field.maxLength, err = nonNegative(property.MaxLength); err != nil {
			return acpField{}, err
		}
		if property.Pattern != "" {
			// Go's RE2 engine runs in linear time; a pattern it cannot
			// compile is left to the agent's own validation.
			if len(property.Pattern) > acpMaxPatternBytes {
				return acpField{}, errors.New("pattern is too long")
			}
			field.pattern, _ = regexp.Compile(property.Pattern)
		}
	case "number", "integer":
		if field.minimum != nil && field.maximum != nil && *field.minimum > *field.maximum {
			return acpField{}, errors.New("range is empty")
		}
	case "boolean":
	case "array":
		if property.Items == nil || property.Items.Type != "" && property.Items.Type != "string" {
			return acpField{}, errors.New("array items are unsupported")
		}
		options := property.Items.AnyOf
		if len(options) == 0 {
			options = property.Items.OneOf
		}
		if field.choices, err = acpChoices(property.Items.Enum, nil, options); err != nil || len(field.choices) == 0 {
			return acpField{}, errors.New("array choices are invalid")
		}
		if field.minItems, err = nonNegative(property.MinItems); err != nil {
			return acpField{}, err
		}
		if field.maxItems, err = nonNegative(property.MaxItems); err != nil {
			return acpField{}, err
		}
	default:
		return acpField{}, errors.New("property type is unsupported")
	}
	return field, nil
}

type acpConst struct {
	Const json.RawMessage `json:"const"`
	Title string          `json:"title"`
}

// acpChoices reads string choices from enum (with optional legacy
// enumNames) or oneOf/anyOf const entries.
func acpChoices(enum []json.RawMessage, names []string, consts []acpConst) ([]acpChoice, error) {
	if len(enum) != 0 && len(consts) != 0 {
		return nil, errors.New("choices are ambiguous")
	}
	var choices []acpChoice
	seen := map[string]bool{}
	add := func(raw json.RawMessage, title string) error {
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > adapterproto.MaxContentBytes/64 ||
			!utf8.ValidString(value) || seen[value] {
			return errors.New("choice is invalid")
		}
		seen[value] = true
		label := acpLine(defaultString(strings.TrimSpace(title), value), adapterproto.MaxOptionBytes-16)
		choices = append(choices, acpChoice{value: value, label: label})
		return nil
	}
	if len(enum)+len(consts) > adapterproto.MaxOptions*8 {
		return nil, errors.New("too many choices")
	}
	for index, raw := range enum {
		title := ""
		if index < len(names) {
			title = names[index]
		}
		if err := add(raw, title); err != nil {
			return nil, err
		}
	}
	for _, option := range consts {
		if err := add(option.Const, option.Title); err != nil {
			return nil, err
		}
	}
	return choices, nil
}

// orderedObject decodes a JSON object's keys in document order. Duplicate
// keys are rejected.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, nil, errors.New("not an object")
	}
	var names []string
	values := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, nil, errors.New("object key is invalid")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		if _, exists := values[name]; exists {
			return nil, nil, errors.New("object key is duplicated")
		}
		names = append(names, name)
		values[name] = value
		if len(names) > acpMaxElicitFields {
			return nil, nil, errors.New("too many properties")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	return names, values, nil
}
