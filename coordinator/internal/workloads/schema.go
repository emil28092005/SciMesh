package workloads

import (
	"fmt"
	"math"
	"sort"
)

// validateSchema checks the strict JSON Schema subset used by SDK manifests.
// It mirrors what the SDK registry enforces on the Python side: an object
// schema with additionalProperties=false, typed properties, and the keyword
// subset the coordinator understands (type, enum, required, minimum/maximum,
// minLength/maxLength, oneOf, not).
func validateSchema(workloadName string, schema map[string]any) error {
	if schema == nil {
		return fmt.Errorf("workload %q has no parameter schema", workloadName)
	}
	if err := validateSchemaNode(workloadName+".parameters_schema", schema); err != nil {
		return err
	}
	if schemaType(schema) != "object" {
		return fmt.Errorf("workload %q parameter schema must be an object schema", workloadName)
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		return fmt.Errorf("workload %q parameter schema must set additionalProperties=false", workloadName)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("workload %q parameter schema must declare properties", workloadName)
	}
	for name, property := range properties {
		child, ok := property.(map[string]any)
		if !ok {
			return fmt.Errorf("workload %q parameter %q must be a schema object", workloadName, name)
		}
		if err := validateSchemaNode(name, child); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaNode(field string, node map[string]any) error {
	for keyword := range node {
		switch keyword {
		case "type", "enum", "required", "minimum", "maximum", "exclusiveMinimum",
			"exclusiveMaximum", "minLength", "maxLength", "properties",
			"additionalProperties", "oneOf", "not", "default", "description",
			"items", "minItems", "maxItems":
		default:
			return fmt.Errorf("%s uses unsupported JSON Schema keyword %q", field, keyword)
		}
	}
	if rawType, ok := node["type"]; ok {
		schemaType, ok := rawType.(string)
		if !ok {
			return fmt.Errorf("%s type must be a string", field)
		}
		switch schemaType {
		case "string", "number", "integer", "boolean", "object", "array":
		default:
			return fmt.Errorf("%s has unknown type %q", field, schemaType)
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if value, ok := node[keyword]; ok {
			if number, ok := value.(float64); !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return fmt.Errorf("%s %s must be a finite number", field, keyword)
			}
		}
	}
	for _, keyword := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		if value, ok := node[keyword]; ok {
			if number, ok := value.(float64); !ok || number < 0 || number != math.Trunc(number) {
				return fmt.Errorf("%s %s must be a non-negative integer", field, keyword)
			}
		}
	}
	if required, ok := node["required"]; ok {
		entries, ok := required.([]any)
		if !ok {
			return fmt.Errorf("%s required must be an array of strings", field)
		}
		for _, entry := range entries {
			if _, ok := entry.(string); !ok {
				return fmt.Errorf("%s required must be an array of strings", field)
			}
		}
	}
	if enums, ok := node["enum"]; ok {
		entries, ok := enums.([]any)
		if !ok || len(entries) == 0 {
			return fmt.Errorf("%s enum must be a non-empty array", field)
		}
	}
	if oneOf, ok := node["oneOf"]; ok {
		entries, ok := oneOf.([]any)
		if !ok || len(entries) == 0 {
			return fmt.Errorf("%s oneOf must be a non-empty array", field)
		}
		for index, entry := range entries {
			child, ok := entry.(map[string]any)
			if !ok {
				return fmt.Errorf("%s oneOf[%d] must be a schema object", field, index)
			}
			if err := validateSchemaNode(fmt.Sprintf("%s.oneOf[%d]", field, index), child); err != nil {
				return err
			}
		}
	}
	if child, ok := node["not"]; ok {
		not, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("%s not must be a schema object", field)
		}
		if err := validateSchemaNode(field+".not", not); err != nil {
			return err
		}
	}
	if items, ok := node["items"]; ok {
		child, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("%s items must be a schema object", field)
		}
		if err := validateSchemaNode(field+".items", child); err != nil {
			return err
		}
	}
	if properties, ok := node["properties"]; ok {
		entries, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s properties must be an object", field)
		}
		for name, property := range entries {
			child, ok := property.(map[string]any)
			if !ok {
				return fmt.Errorf("%s property %q must be a schema object", field, name)
			}
			if err := validateSchemaNode(field+"."+name, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaType(node map[string]any) string {
	rawType, _ := node["type"].(string)
	return rawType
}

// validateParameters checks values against the strict schema subset.
func validateParameters(workloadName string, schema map[string]any, parameters map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	for name := range parameters {
		if _, declared := properties[name]; !declared {
			return fmt.Errorf("workload %q does not accept parameter %q", workloadName, name)
		}
	}
	required, _ := schema["required"].([]any)
	for _, name := range required {
		field, _ := name.(string)
		if _, present := parameters[field]; !present {
			return fmt.Errorf("workload %q requires parameter %q", workloadName, field)
		}
	}
	for name, value := range parameters {
		property, declared := properties[name].(map[string]any)
		if !declared {
			continue
		}
		if err := validateProperty(workloadName+"."+name, property, value); err != nil {
			return err
		}
	}
	if oneOf, ok := schema["oneOf"].([]any); ok && len(oneOf) > 0 {
		if err := validateOneOf(workloadName, oneOf, parameters); err != nil {
			return err
		}
	}
	return nil
}

func validateProperty(field string, property map[string]any, value any) error {
	if enums, ok := property["enum"].([]any); ok {
		for _, candidate := range enums {
			if valuesEqual(candidate, value) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of the declared enum values", field)
	}
	switch schemaType(property) {
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", field)
		}
		if minimum, ok := lengthBound(property["minLength"]); ok && len([]rune(text)) < minimum {
			return fmt.Errorf("%s is shorter than the minimum length", field)
		}
		if maximum, ok := lengthBound(property["maxLength"]); ok && len([]rune(text)) > maximum {
			return fmt.Errorf("%s exceeds the maximum length", field)
		}
	case "number", "integer":
		number, ok := asFloat(value)
		if !ok {
			return fmt.Errorf("%s must be a number", field)
		}
		if schemaType(property) == "integer" && number != math.Trunc(number) {
			return fmt.Errorf("%s must be an integer", field)
		}
		if minimum, ok := numberBound(property["minimum"]); ok && number < minimum {
			return fmt.Errorf("%s is below the minimum", field)
		}
		if maximum, ok := numberBound(property["maximum"]); ok && number > maximum {
			return fmt.Errorf("%s exceeds the maximum", field)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", field)
		}
	case "object":
		child, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", field)
		}
		properties, _ := property["properties"].(map[string]any)
		for name := range child {
			if _, declared := properties[name]; !declared {
				return fmt.Errorf("%s has undeclared field %q", field, name)
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", field)
		}
		if itemSchema, ok := property["items"].(map[string]any); ok {
			for index, item := range items {
				if err := validateProperty(fmt.Sprintf("%s[%d]", field, index), itemSchema, item); err != nil {
					return err
				}
			}
		}
	case "":
		// No type keyword: enum-only properties are handled above.
		return fmt.Errorf("%s has no JSON Schema type", field)
	}
	return nil
}

func validateOneOf(workloadName string, oneOf []any, parameters map[string]any) error {
	satisfied := 0
	for _, candidate := range oneOf {
		option, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		if optionSatisfied(option, parameters) {
			satisfied++
		}
	}
	if satisfied != 1 {
		return fmt.Errorf("workload %q requires exactly one of the declared parameter alternatives", workloadName)
	}
	return nil
}

func optionSatisfied(option map[string]any, parameters map[string]any) bool {
	if required, ok := option["required"].([]any); ok {
		for _, name := range required {
			field, _ := name.(string)
			if _, present := parameters[field]; !present {
				return false
			}
		}
	}
	if not, ok := option["not"].(map[string]any); ok {
		if required, ok := not["required"].([]any); ok {
			for _, name := range required {
				field, _ := name.(string)
				if _, present := parameters[field]; present {
					return false
				}
			}
		}
	}
	return true
}

func lengthBound(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) {
		return 0, false
	}
	return int(number), true
}

func numberBound(value any) (float64, bool) {
	number, ok := asFloat(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func valuesEqual(left, right any) bool {
	switch l := left.(type) {
	case float64:
		r, ok := right.(float64)
		return ok && l == r
	case string:
		r, ok := right.(string)
		return ok && l == r
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	case nil:
		return right == nil
	}
	return false
}

// schemaDefaults collects the declared default for each property. The UI uses
// these to pre-fill controls that have no workload-declared UI default.
func schemaDefaults(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	defaults := map[string]any{}
	for name, property := range properties {
		child, ok := property.(map[string]any)
		if !ok {
			continue
		}
		if value, present := child["default"]; present {
			defaults[name] = value
		}
	}
	return defaults
}

// SortedFields returns the sorted declared parameter names.
func SortedFields(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
