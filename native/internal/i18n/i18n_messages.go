package i18n

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

//go:embed locales/*.json
var localeFiles embed.FS

var MessageCatalogs = loadMessageCatalogs()

func loadMessageCatalogs() map[string]map[string]string {
	catalogs := make(map[string]map[string]string)
	for _, language := range []string{"zh-CN", "en"} {
		data, err := localeFiles.ReadFile("locales/" + language + ".json")
		if err != nil {
			panic(err)
		}
		var catalog map[string]string
		if err := json.Unmarshal(data, &catalog); err != nil {
			panic(err)
		}
		catalogs[language] = catalog
	}
	return catalogs
}

func NormalizeLanguage(language string) string {
	if language == "en" {
		return "en"
	}
	return "zh-CN"
}

// Application state retains message IDs and arguments until presentation, so
// switching languages also updates pending results and previously stored errors.
// The envelope permits composition in existing string status fields without
// matching, guessing or replacing any natural-language text.
const messageStart = "\x1ewebhola-i18n:"

const messageEnd = "\x1f"

type messageArgument struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type localizedMessage struct {
	ID   string            `json:"id"`
	Args []messageArgument `json:"args,omitempty"`
}

func MessageText(id string, args ...any) string {
	m := localizedMessage{ID: id}
	for _, value := range args {
		arg := messageArgument{Kind: "text", Value: fmt.Sprint(value)}
		if err, ok := value.(error); ok {
			arg.Value = ErrorMessage(err)
		}
		if value != nil {
			switch reflect.TypeOf(value).Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				arg.Kind = "int"
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				arg.Kind = "uint"
			case reflect.Float32, reflect.Float64:
				arg.Kind = "float"
			}
		}
		m.Args = append(m.Args, arg)
	}
	b, _ := json.Marshal(m)
	return messageStart + base64.RawURLEncoding.EncodeToString(b) + messageEnd
}

type localizedError struct {
	text  string
	cause error
}

func (e *localizedError) Error() string { return TranslateText("en", e.text) }

func (e *localizedError) Unwrap() error { return e.cause }

func ErrorMessage(err error) string {
	if e, ok := err.(*localizedError); ok {
		return e.text
	}
	return err.Error()
}

// Preserve the error chain while deferring localization to the active UI.
func MessageError(id string, args ...any) error {
	e := &localizedError{text: MessageText(id, args...)}
	for _, arg := range args {
		if cause, ok := arg.(error); ok {
			e.cause = cause
			break
		}
	}
	return e
}

func lookupMessage(language, id string) string {
	if text, ok := MessageCatalogs[NormalizeLanguage(language)][id]; ok {
		return text
	}
	if text, ok := MessageCatalogs["zh-CN"][id]; ok {
		return text
	}
	return id
}

func TranslateText(language, text string) string {
	if _, known := MessageCatalogs["zh-CN"][text]; known {
		return lookupMessage(language, text)
	}
	return renderMessageParts(language, text, 0)
}

func renderMessageParts(language, text string, depth int) string {
	if depth > 32 {
		return text
	}
	var out strings.Builder
	for {
		start := strings.Index(text, messageStart)
		if start < 0 {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:start])
		rest := text[start+len(messageStart):]
		end := strings.Index(rest, messageEnd)
		if end < 0 {
			out.WriteString(text[start:])
			break
		}
		var m localizedMessage
		b, err := base64.RawURLEncoding.DecodeString(rest[:end])
		if err != nil || json.Unmarshal(b, &m) != nil || m.ID == "" {
			out.WriteString(text[start : start+len(messageStart)+end+len(messageEnd)])
		} else {
			args := make([]any, 0, len(m.Args))
			for _, arg := range m.Args {
				switch arg.Kind {
				case "int":
					n, _ := strconv.ParseInt(arg.Value, 10, 64)
					args = append(args, n)
				case "uint":
					n, _ := strconv.ParseUint(arg.Value, 10, 64)
					args = append(args, n)
				case "float":
					n, _ := strconv.ParseFloat(arg.Value, 64)
					args = append(args, n)
				default:
					args = append(args, renderMessageParts(language, arg.Value, depth+1))
				}
			}
			template := lookupMessage(language, m.ID)
			if len(args) > 0 {
				template = fmt.Sprintf(strings.ReplaceAll(template, "%w", "%v"), args...)
			}
			out.WriteString(template)
		}
		text = rest[end+len(messageEnd):]
	}
	return out.String()
}
