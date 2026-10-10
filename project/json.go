package project

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

type jsonField struct {
	schema   *jsonShape
	required bool
}

type jsonShape struct {
	kind   string
	fields map[string]jsonField
	elem   *jsonShape
	values *jsonShape
}

func object(fields map[string]jsonField) *jsonShape {
	return &jsonShape{kind: "object", fields: fields}
}
func mapObject(values *jsonShape) *jsonShape {
	return &jsonShape{kind: "object", fields: map[string]jsonField{}, values: values}
}
func array(elem *jsonShape) *jsonShape    { return &jsonShape{kind: "array", elem: elem} }
func scalar(kind string) *jsonShape       { return &jsonShape{kind: kind} }
func required(shape *jsonShape) jsonField { return jsonField{schema: shape, required: true} }
func optional(shape *jsonShape) jsonField { return jsonField{schema: shape} }

var (
	stringShape      = scalar("string")
	intShape         = scalar("number")
	positiveIntShape = scalar("positive-number")

	portShape = object(map[string]jsonField{
		"from": required(intShape), "to": required(intShape),
	})
	networkRuleShape = object(map[string]jsonField{
		"protocol": required(stringShape), "host": optional(stringShape),
		"ports": required(array(portShape)), "groups": required(array(stringShape)),
	})
	capabilitiesShape = object(map[string]jsonField{
		"env": required(array(stringShape)),
		"filesystem": required(array(object(map[string]jsonField{
			"root": required(stringShape), "modes": required(array(stringShape)),
		}))),
		"process": required(array(object(map[string]jsonField{
			"executable": required(stringShape), "maxArgs": optional(positiveIntShape),
			"maxOutputBytes": optional(positiveIntShape),
		}))),
		"network": required(object(map[string]jsonField{
			"connect": required(array(networkRuleShape)), "listen": required(array(networkRuleShape)),
		})),
		"lifecycle": required(object(map[string]jsonField{
			"signals": required(array(stringShape)),
		})),
	})
	limitsShape = object(map[string]jsonField{
		"maxWorkers": optional(positiveIntShape), "maxQueueCapacity": optional(positiveIntShape),
		"maxDeadlineMs": optional(positiveIntShape), "maxFileBytes": optional(positiveIntShape),
		"maxProcessOutputBytes":  optional(positiveIntShape),
		"maxNetworkConnections":  optional(positiveIntShape),
		"maxDatabaseConnections": optional(positiveIntShape),
	})
	manifestShape = object(map[string]jsonField{
		"schemaVersion": required(intShape), "language": required(stringShape),
		"entry": required(stringShape),
		"target": required(object(map[string]jsonField{
			"os": required(array(stringShape)), "arch": required(array(stringShape)),
		})),
		"capabilities": required(capabilitiesShape),
		"databases": required(mapObject(object(map[string]jsonField{
			"engine": required(stringShape), "config": required(stringShape),
		}))),
		"limits": required(limitsShape),
	})
	grantsShape = object(map[string]jsonField{
		"schemaVersion": required(intShape), "manifestSha256": required(stringShape),
		"capabilities": required(capabilitiesShape),
		"databases": required(mapObject(object(map[string]jsonField{
			"url": required(stringShape),
		}))),
		"limits": required(limitsShape),
	})
)

func strictDecode(data []byte, dst any) error {
	if len(data) == 0 || len(data) > MaxDocumentBytes || !utf8.Valid(data) {
		return ErrInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder, 0)
	if err != nil {
		return ErrInvalidManifest
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidManifest
	}
	shape := manifestShape
	if _, ok := dst.(*Grants); ok {
		shape = grantsShape
	}
	if !matchesShape(value, shape) {
		return ErrInvalidManifest
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dst); err != nil {
		return ErrInvalidManifest
	}
	if err := strict.Decode(new(any)); err != io.EOF {
		return ErrInvalidManifest
	}
	return nil
}

func parseJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 128 {
		return nil, ErrInvalidManifest
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		objectValue := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, ErrInvalidManifest
			}
			if _, exists := objectValue[key]; exists {
				return nil, ErrInvalidManifest
			}
			value, err := parseJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			objectValue[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return objectValue, nil
	case json.Delim('['):
		arrayValue := make([]any, 0)
		for decoder.More() {
			value, err := parseJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			arrayValue = append(arrayValue, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return arrayValue, nil
	default:
		return token, nil
	}
}

func matchesShape(value any, shape *jsonShape) bool {
	switch shape.kind {
	case "object":
		fields, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for name := range fields {
			if _, known := shape.fields[name]; !known && shape.values == nil {
				return false
			}
		}
		for name, field := range shape.fields {
			fieldValue, exists := fields[name]
			if !exists {
				if field.required {
					return false
				}
				continue
			}
			if !matchesShape(fieldValue, field.schema) {
				return false
			}
		}
		if shape.values != nil {
			for _, fieldValue := range fields {
				if !matchesShape(fieldValue, shape.values) {
					return false
				}
			}
		}
		return true
	case "array":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, element := range values {
			if !matchesShape(element, shape.elem) {
				return false
			}
		}
		return true
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "positive-number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		integer, err := number.Int64()
		return err == nil && integer > 0
	default:
		return false
	}
}
