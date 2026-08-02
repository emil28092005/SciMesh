"""Bounded JSON Schema subset used for SDK v1 public parameters."""

from __future__ import annotations

import math
import re
from fractions import Fraction
from typing import Mapping


_ANNOTATIONS = {
    "$schema",
    "title",
    "description",
    "default",
    "examples",
    "deprecated",
    "readOnly",
    "writeOnly",
}
_KEYWORDS = _ANNOTATIONS | {
    "type",
    "enum",
    "const",
    "properties",
    "additionalProperties",
    "required",
    "minProperties",
    "maxProperties",
    "items",
    "minItems",
    "maxItems",
    "uniqueItems",
    "minLength",
    "maxLength",
    "pattern",
    "minimum",
    "maximum",
    "exclusiveMinimum",
    "exclusiveMaximum",
    "multipleOf",
    "allOf",
    "anyOf",
    "oneOf",
    "not",
}
_TYPES = {"null", "boolean", "object", "array", "number", "integer", "string"}


class ParameterValidationError(ValueError):
    """Sanitized public-parameter schema failure."""


def _schema_error(message: str) -> ValueError:
    return ValueError("unsupported or invalid parameters_schema: " + message)


def _json_equal(left: object, right: object) -> bool:
    """Compare values using the JSON data model rather than Python coercion.

    Python considers ``True == 1`` while JSON has distinct boolean and number
    types. JSON Schema does, however, treat integral and non-integral syntax for
    the same mathematical number (for example ``1`` and ``1.0``) as equal.
    """
    if isinstance(left, bool) or isinstance(right, bool):
        return isinstance(left, bool) and isinstance(right, bool) and left is right
    if isinstance(left, (int, float)) and isinstance(right, (int, float)):
        return left == right
    if left is None or right is None:
        return left is None and right is None
    if isinstance(left, str) or isinstance(right, str):
        return isinstance(left, str) and isinstance(right, str) and left == right
    if isinstance(left, Mapping) and isinstance(right, Mapping):
        return set(left) == set(right) and all(
            _json_equal(left[key], right[key]) for key in left
        )
    if isinstance(left, (list, tuple)) and isinstance(right, (list, tuple)):
        return len(left) == len(right) and all(
            _json_equal(left_item, right_item)
            for left_item, right_item in zip(left, right)
        )
    return False


def _json_key(value: object, depth: int = 0) -> object:
    """Build a hashable JSON-type-aware key in linear time."""
    if depth > 64:
        raise ValueError("JSON value nesting exceeds 64 levels")
    if value is None:
        return ("null",)
    if isinstance(value, bool):
        return ("boolean", value)
    if isinstance(value, (int, float)):
        return (
            "number",
            Fraction(value) if isinstance(value, int) else Fraction.from_float(value),
        )
    if isinstance(value, str):
        return ("string", value)
    if isinstance(value, Mapping):
        return (
            "object",
            tuple(
                (key, _json_key(child, depth + 1))
                for key, child in sorted(value.items())
            ),
        )
    if isinstance(value, (list, tuple)):
        return ("array", tuple(_json_key(child, depth + 1) for child in value))
    raise ValueError("value is not JSON-compatible")


def _validate_safe_pattern(pattern: str) -> None:
    """Accept only the v1 linear-time regex subset.

    Groups, alternation, backreferences, and repetition operators are excluded;
    literals, anchors, character classes, escapes, and ``.`` remain available.
    """
    escaped = False
    in_class = False
    for character in pattern:
        if escaped:
            if character.isdigit():
                raise _schema_error("pattern backreferences are not supported")
            escaped = False
            continue
        if character == "\\":
            escaped = True
            continue
        if character == "[" and not in_class:
            in_class = True
            continue
        if character == "]" and in_class:
            in_class = False
            continue
        if not in_class and character in "()|*+?{}":
            raise _schema_error("pattern uses an unbounded regex operator")
    if escaped or in_class:
        # ``re.compile`` will provide the canonical invalid-regex error below.
        return


def _is_json_multiple(value: int | float, divisor: int | float) -> bool:
    """Evaluate ``multipleOf`` without converting arbitrary integers to float."""
    if isinstance(value, int) and isinstance(divisor, int):
        return value % divisor == 0
    value_fraction = Fraction(value) if isinstance(value, int) else Fraction(str(value))
    divisor_fraction = (
        Fraction(divisor) if isinstance(divisor, int) else Fraction(str(divisor))
    )
    return (value_fraction / divisor_fraction).denominator == 1


def validate_schema_definition(
    schema: Mapping[str, object], *, _depth: int = 0
) -> None:
    """Validate a parameter schema against the bounded JSON Schema subset.

    Raises ``ValueError`` on unknown keywords, unsupported types, unsafe
    patterns, or malformed bounds.
    """
    if _depth > 64:
        raise _schema_error("nesting exceeds 64 levels")
    if not isinstance(schema, Mapping):
        raise _schema_error("each schema node must be an object")
    unknown = set(schema) - _KEYWORDS
    if unknown:
        raise _schema_error("unknown keyword " + sorted(unknown)[0])
    raw_type = schema.get("type")
    if raw_type is not None:
        declared = (raw_type,) if isinstance(raw_type, str) else raw_type
        if not isinstance(declared, (list, tuple)) or not declared:
            raise _schema_error("type must be a string or non-empty array")
        if any(not isinstance(value, str) or value not in _TYPES for value in declared):
            raise _schema_error("type contains an unsupported JSON type")
        if len(declared) != len(set(declared)):
            raise _schema_error("type alternatives must be unique")
    properties = schema.get("properties")
    if properties is not None:
        if not isinstance(properties, Mapping) or any(
            not isinstance(name, str) for name in properties
        ):
            raise _schema_error("properties must be an object")
        for child in properties.values():
            validate_schema_definition(child, _depth=_depth + 1)  # type: ignore[arg-type]
    additional = schema.get("additionalProperties")
    if additional is not None and not isinstance(additional, (bool, Mapping)):
        raise _schema_error("additionalProperties must be a boolean or schema")
    if isinstance(additional, Mapping):
        validate_schema_definition(additional, _depth=_depth + 1)
    required = schema.get("required")
    if required is not None:
        if not isinstance(required, (list, tuple)) or any(
            not isinstance(name, str) for name in required
        ):
            raise _schema_error("required must be an array of strings")
        if len(required) != len(set(required)):
            raise _schema_error("required names must be unique")
    for keyword in ("items", "not"):
        child = schema.get(keyword)
        if child is not None:
            validate_schema_definition(child, _depth=_depth + 1)  # type: ignore[arg-type]
    for keyword in ("allOf", "anyOf", "oneOf"):
        children = schema.get(keyword)
        if children is None:
            continue
        if not isinstance(children, (list, tuple)) or not children:
            raise _schema_error(f"{keyword} must be a non-empty array")
        for child in children:
            validate_schema_definition(child, _depth=_depth + 1)  # type: ignore[arg-type]
    enum = schema.get("enum")
    if enum is not None and (not isinstance(enum, (list, tuple)) or not enum):
        raise _schema_error("enum must be a non-empty array")
    if isinstance(enum, (list, tuple)):
        seen_enum: set[object] = set()
        for item in enum:
            key = _json_key(item)
            if key in seen_enum:
                raise _schema_error("enum values must be unique")
            seen_enum.add(key)
    for keyword in (
        "minProperties",
        "maxProperties",
        "minItems",
        "maxItems",
        "minLength",
        "maxLength",
    ):
        value = schema.get(keyword)
        if value is not None and (
            isinstance(value, bool) or not isinstance(value, int) or value < 0
        ):
            raise _schema_error(f"{keyword} must be a non-negative integer")
    for minimum, maximum in (
        ("minProperties", "maxProperties"),
        ("minItems", "maxItems"),
        ("minLength", "maxLength"),
    ):
        if (
            minimum in schema
            and maximum in schema
            and schema[minimum] > schema[maximum]  # type: ignore[operator]
        ):
            raise _schema_error(f"{minimum} must not exceed {maximum}")
    for keyword in (
        "minimum",
        "maximum",
        "exclusiveMinimum",
        "exclusiveMaximum",
        "multipleOf",
    ):
        value = schema.get(keyword)
        if value is not None and (
            isinstance(value, bool)
            or not isinstance(value, (int, float))
            or (isinstance(value, float) and not math.isfinite(value))
        ):
            raise _schema_error(f"{keyword} must be a finite number")
    if "multipleOf" in schema and schema["multipleOf"] <= 0:  # type: ignore[operator]
        raise _schema_error("multipleOf must be positive")
    pattern = schema.get("pattern")
    if pattern is not None:
        if not isinstance(pattern, str) or len(pattern) > 1024:
            raise _schema_error("pattern must be a string of at most 1024 characters")
        _validate_safe_pattern(pattern)
        try:
            re.compile(pattern)
        except re.error as error:
            raise _schema_error("pattern is not a valid regular expression") from error
    for keyword in ("uniqueItems", "deprecated", "readOnly", "writeOnly"):
        if keyword in schema and not isinstance(schema[keyword], bool):
            raise _schema_error(f"{keyword} must be a boolean")


def _type_matches(value: object, expected: str) -> bool:
    if expected == "null":
        return value is None
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "object":
        return isinstance(value, Mapping)
    if expected == "array":
        return isinstance(value, (list, tuple))
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    if expected == "string":
        return isinstance(value, str)
    return False


def _failure(path: str, reason: str) -> ParameterValidationError:
    return ParameterValidationError(
        f"job parameters violate their schema at {path}: {reason}"
    )


def validate_parameter_instance(
    value: object,
    schema: Mapping[str, object],
    *,
    path: str = "$",
    _depth: int = 0,
) -> None:
    """Validate one parameter value against a schema subset node.

    Raises ``ParameterValidationError`` (a ``ValueError``) with a sanitized
    JSON path on the first violation.
    """
    if _depth > 64:
        raise _failure(path, "nesting exceeds 64 levels")
    raw_type = schema.get("type")
    if raw_type is not None:
        expected = (raw_type,) if isinstance(raw_type, str) else tuple(raw_type)  # type: ignore[arg-type]
        if not any(_type_matches(value, item) for item in expected):
            raise _failure(path, "type mismatch")
    if "enum" in schema and not any(
        _json_equal(value, candidate)
        for candidate in schema["enum"]  # type: ignore[union-attr]
    ):
        raise _failure(path, "value is outside enum")
    if "const" in schema and not _json_equal(value, schema["const"]):
        raise _failure(path, "value does not match const")
    for keyword in ("allOf", "anyOf", "oneOf"):
        children = schema.get(keyword)
        if children is None:
            continue
        matches = 0
        for child in children:  # type: ignore[union-attr]
            try:
                validate_parameter_instance(value, child, path=path, _depth=_depth + 1)
            except ParameterValidationError:
                continue
            matches += 1
        if keyword == "allOf" and matches != len(children):  # type: ignore[arg-type]
            raise _failure(path, "allOf did not match")
        if keyword == "anyOf" and matches == 0:
            raise _failure(path, "anyOf did not match")
        if keyword == "oneOf" and matches != 1:
            raise _failure(path, "oneOf did not match exactly once")
    excluded = schema.get("not")
    if excluded is not None:
        try:
            validate_parameter_instance(value, excluded, path=path, _depth=_depth + 1)  # type: ignore[arg-type]
        except ParameterValidationError:
            pass
        else:
            raise _failure(path, "value matches a forbidden schema")
    if isinstance(value, Mapping):
        required = schema.get("required", ())
        missing = set(required) - set(value)  # type: ignore[arg-type]
        if missing:
            raise _failure(path, "missing required field " + sorted(missing)[0])
        minimum = schema.get("minProperties")
        maximum = schema.get("maxProperties")
        if minimum is not None and len(value) < minimum:  # type: ignore[operator]
            raise _failure(path, "too few properties")
        if maximum is not None and len(value) > maximum:  # type: ignore[operator]
            raise _failure(path, "too many properties")
        properties = schema.get("properties", {})
        additional = schema.get("additionalProperties", True)
        for name, child in value.items():
            if name in properties:  # type: ignore[operator]
                validate_parameter_instance(
                    child,
                    properties[name],  # type: ignore[index]
                    path=f"{path}.{name}",
                    _depth=_depth + 1,
                )
            elif additional is False:
                raise _failure(path, f"unknown field {name}")
            elif isinstance(additional, Mapping):
                validate_parameter_instance(
                    child, additional, path=f"{path}.{name}", _depth=_depth + 1
                )
    if isinstance(value, (list, tuple)):
        minimum = schema.get("minItems")
        maximum = schema.get("maxItems")
        if minimum is not None and len(value) < minimum:  # type: ignore[operator]
            raise _failure(path, "too few items")
        if maximum is not None and len(value) > maximum:  # type: ignore[operator]
            raise _failure(path, "too many items")
        if schema.get("uniqueItems"):
            seen_items: set[object] = set()
            for item in value:
                key = _json_key(item)
                if key in seen_items:
                    raise _failure(path, "items must be unique")
                seen_items.add(key)
        child_schema = schema.get("items")
        if child_schema is not None:
            for index, item in enumerate(value):
                validate_parameter_instance(
                    item,
                    child_schema,  # type: ignore[arg-type]
                    path=f"{path}[{index}]",
                    _depth=_depth + 1,
                )
    if isinstance(value, str):
        if "minLength" in schema and len(value) < schema["minLength"]:  # type: ignore[operator]
            raise _failure(path, "string is too short")
        if "maxLength" in schema and len(value) > schema["maxLength"]:  # type: ignore[operator]
            raise _failure(path, "string is too long")
        if "pattern" in schema and re.search(schema["pattern"], value) is None:  # type: ignore[arg-type]
            raise _failure(path, "string does not match pattern")
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        checks = (
            ("minimum", lambda actual, bound: actual >= bound),
            ("maximum", lambda actual, bound: actual <= bound),
            ("exclusiveMinimum", lambda actual, bound: actual > bound),
            ("exclusiveMaximum", lambda actual, bound: actual < bound),
        )
        for keyword, predicate in checks:
            if keyword in schema and not predicate(value, schema[keyword]):
                raise _failure(path, f"number violates {keyword}")
        if "multipleOf" in schema:
            if not _is_json_multiple(value, schema["multipleOf"]):  # type: ignore[arg-type]
                raise _failure(path, "number violates multipleOf")
