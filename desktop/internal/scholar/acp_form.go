package scholar

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	rpc "github.com/caelis-labs/acp-go-sdk"
	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

type ACPFormChoice struct {
	Value, Label string
}

type ACPFormField struct {
	Name, Label, Description, Kind string
	Required                       bool
	Choices                        []ACPFormChoice
	Default                        any
	minimum, maximum               *float64
	minInteger, maxInteger         *int64
	minLength, maxLength           *uint32
	minItems, maxItems             *uint64
	pattern                        *regexp.Regexp
	format                         acp.StringFormat
}

type ACPForm struct {
	Title, Message, Description string
	Fields                      []ACPFormField
	answers                     chan acp.CreateElicitationResponse
	done                        chan struct{}
}

func (f *ACPForm) Respond(action string, values map[string]any) bool {
	var answer acp.CreateElicitationResponse
	switch action {
	case "accept":
		for _, field := range f.Fields {
			if !field.Valid(values[field.Name]) {
				return false
			}
		}
		answer.Accept = &acp.CreateElicitationAccept{Action: action, Content: values}
	case "decline":
		answer.Decline = &acp.CreateElicitationDecline{Action: action}
	case "cancel":
		answer.Cancel = &acp.CreateElicitationCancel{Action: action}
	default:
		return false
	}
	select {
	case <-f.done:
		return false
	default:
	}
	select {
	case f.answers <- answer:
		return true
	case <-f.done:
	default:
	}
	return false
}

func (f ACPFormField) Valid(value any) bool {
	if value == nil {
		return !f.Required
	}
	switch f.Kind {
	case "string":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) || len(text) > 16<<10 {
			return false
		}
		if len(f.Choices) > 0 {
			return f.hasChoice(text)
		}
		count := uint32(utf8.RuneCountInString(text))
		return (f.minLength == nil || count >= *f.minLength) &&
			(f.maxLength == nil || count <= *f.maxLength) &&
			(f.pattern == nil || f.pattern.MatchString(text)) && acpFormFormat(f.format, text)
	case "integer":
		n, ok := value.(int64)
		return ok && (f.minInteger == nil || n >= *f.minInteger) && (f.maxInteger == nil || n <= *f.maxInteger)
	case "number":
		n, ok := value.(float64)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0) &&
			(f.minimum == nil || n >= *f.minimum) && (f.maximum == nil || n <= *f.maximum)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		values, ok := value.([]string)
		if !ok || f.minItems != nil && uint64(len(values)) < *f.minItems || f.maxItems != nil && uint64(len(values)) > *f.maxItems {
			return false
		}
		seen := make(map[string]bool, len(values))
		for _, choice := range values {
			if !f.hasChoice(choice) || seen[choice] {
				return false
			}
			seen[choice] = true
		}
		return true
	}
	return false
}

func acpFormFormat(format acp.StringFormat, text string) bool {
	switch format {
	case acp.StringFormatEmail:
		address, err := mail.ParseAddress(text)
		return err == nil && address.Address == text
	case acp.StringFormatUri:
		parsed, err := url.ParseRequestURI(text)
		return err == nil && parsed.IsAbs()
	case acp.StringFormatDate:
		_, err := time.Parse(time.DateOnly, text)
		return err == nil
	case acp.StringFormatDateTime:
		_, err := time.Parse(time.RFC3339, text)
		return err == nil
	default:
		return true
	}
}

func (f ACPFormField) hasChoice(value string) bool {
	return slices.ContainsFunc(f.Choices, func(choice ACPFormChoice) bool { return choice.Value == value })
}

func (e *acpEvents) CreateElicitation(ctx context.Context, req acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	if e.formChanged == nil {
		return acp.CreateElicitationResponse{}, rpc.NewMethodNotFound(acp.ClientMethodElicitationCreate)
	}
	e.mu.Lock()
	form := req.Form
	validScope := form != nil && (form.RequestId != nil || form.SessionId != nil && (e.session == "" || *form.SessionId == e.session))
	if !validScope || e.openForms >= 4 {
		e.mu.Unlock()
		return acp.CreateElicitationResponse{}, rpc.NewInvalidParams(map[string]string{"error": "unsupported elicitation mode, scope or pending form count"})
	}
	e.openForms++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.openForms--
		e.mu.Unlock()
	}()
	request, err := parseACPForm(form)
	if err != nil {
		return acp.CreateElicitationResponse{}, rpc.NewInvalidParams(map[string]string{"error": err.Error()})
	}
	e.formChanged(request, true)
	defer func() {
		close(request.done)
		e.formChanged(request, false)
	}()
	select {
	case response := <-request.answers:
		return response, nil
	case <-ctx.Done():
		return acp.CreateElicitationResponse{}, ctx.Err()
	case <-e.jobCtx.Done():
		return acp.CreateElicitationResponse{Cancel: &acp.CreateElicitationCancel{Action: "cancel"}}, nil
	}
}

func parseACPForm(form *acp.CreateElicitationForm) (*ACPForm, error) {
	schema := form.RequestedSchema
	if len(schema.Properties) > 16 {
		return nil, fmt.Errorf("too many elicitation fields")
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return nil, fmt.Errorf("unknown required elicitation field %q", name)
		}
	}
	result := &ACPForm{
		Message: acpTail(form.Message, 8<<10),
		answers: make(chan acp.CreateElicitationResponse, 1),
		done:    make(chan struct{}),
	}
	result.Title, result.Description = acpFormLabel(schema.Title, ""), acpFormLabel(schema.Description, "")
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		encoded, err := json.Marshal(schema.Properties[name])
		if err != nil || len(encoded) > 16<<10 || len(name) > 256 {
			return nil, fmt.Errorf("invalid elicitation field %q", acpTail(name, 256))
		}
		var property acp.ElicitationPropertySchema
		if err := json.Unmarshal(encoded, &property); err != nil {
			return nil, fmt.Errorf("invalid elicitation field %q: %w", name, err)
		}
		field := ACPFormField{Name: name, Required: slices.Contains(schema.Required, name)}
		if err := field.setSchema(property); err != nil {
			return nil, fmt.Errorf("elicitation field %q: %w", name, err)
		}
		if field.Kind != "" {
			result.Fields = append(result.Fields, field)
		}
	}
	return result, nil
}

func (f *ACPFormField) setSchema(p acp.ElicitationPropertySchema) error {
	switch {
	case p.String != nil:
		v := p.String
		f.Kind, f.Label, f.Description = "string", acpFormLabel(v.Title, f.Name), acpFormLabel(v.Description, "")
		if v.Default != nil {
			f.Default = *v.Default
		}
		f.minLength, f.maxLength = v.MinLength, v.MaxLength
		if v.Format != nil {
			f.format = *v.Format
		}
		if v.Pattern != nil && len(*v.Pattern) <= 512 {
			// The agent still validates ECMA-262 patterns outside Go's regexp subset.
			f.pattern, _ = regexp.Compile(*v.Pattern)
		}
		if len(v.Enum) > 0 && len(v.OneOf) > 0 {
			return fmt.Errorf("enum and oneOf are mutually exclusive")
		}
		f.Choices = acpFormChoices(v.Enum, v.OneOf)
	case p.Integer != nil:
		v := p.Integer
		f.Kind, f.Label, f.Description = "integer", acpFormLabel(v.Title, f.Name), acpFormLabel(v.Description, "")
		f.minInteger, f.maxInteger = v.Minimum, v.Maximum
		if v.Default != nil {
			f.Default = *v.Default
		}
	case p.Number != nil:
		v := p.Number
		f.Kind, f.Label, f.Description = "number", acpFormLabel(v.Title, f.Name), acpFormLabel(v.Description, "")
		f.minimum, f.maximum = v.Minimum, v.Maximum
		if v.Default != nil {
			f.Default = *v.Default
		}
	case p.Boolean != nil:
		v := p.Boolean
		f.Kind, f.Label, f.Description = "boolean", acpFormLabel(v.Title, f.Name), acpFormLabel(v.Description, "")
		if v.Default != nil {
			f.Default = *v.Default
		}
	case p.Array != nil:
		v := p.Array
		f.Kind, f.Label, f.Description = "array", acpFormLabel(v.Title, f.Name), acpFormLabel(v.Description, "")
		f.minItems, f.maxItems = v.MinItems, v.MaxItems
		switch {
		case v.Items.String != nil:
			f.Choices = acpFormChoices(v.Items.String.Enum, nil)
		case v.Items.Titled != nil:
			f.Choices = acpFormChoices(nil, v.Items.Titled.AnyOf)
		default:
			if !f.Required {
				f.Kind = ""
				return nil
			}
			return fmt.Errorf("unsupported multi-select items")
		}
		if v.Default != nil {
			f.Default = v.Default
		}
	default:
		if f.Required {
			return fmt.Errorf("unsupported required field")
		}
	}
	if len(f.Choices) > 64 {
		return fmt.Errorf("too many options")
	}
	if f.Default != nil && !f.Valid(f.Default) {
		f.Default = nil
	}
	return nil
}

func acpFormLabel(label *string, fallback string) string {
	if label != nil && *label != "" {
		return acpTail(*label, 1024)
	}
	return fallback
}

func acpFormChoices(values []string, titled []acp.EnumOption) []ACPFormChoice {
	options := make([]ACPFormChoice, 0, len(values)+len(titled))
	for _, value := range values {
		options = append(options, ACPFormChoice{Value: value, Label: acpTail(value, 512)})
	}
	for _, item := range titled {
		label := item.Title
		if label == "" {
			label = item.Const
		}
		options = append(options, ACPFormChoice{Value: item.Const, Label: acpTail(label, 512)})
	}
	return options
}
