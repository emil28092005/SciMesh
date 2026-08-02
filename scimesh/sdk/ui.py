"""Workload-declared UI elements for the operator "new job" form.

Workloads may declare how their parameters should be rendered in the
coordinator UI. These declarations are presentation metadata only: the
strict parameter schema remains the authoritative validation contract, and
the UI falls back to schema-derived controls when a workload declares no
elements.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any

from ._validation import (
    freeze_json,
    require_exact_keys,
    require_identifier,
    require_string,
)

_WIDGETS = {"text", "textarea", "number", "select", "checkbox"}


def _optional_string(value: object, field: str) -> str:
    if value == "":
        return ""
    return require_string(value, field)


@dataclass(frozen=True, slots=True)
class UIElement:
    """One form control bound to a workload parameter.

    ``field`` must name a property of the workload's ``parameters_schema``;
    ``widget`` is one of ``text``, ``textarea``, ``number``, ``select``, or
    ``checkbox``; ``options`` are required for ``select``. ``default`` must be
    JSON-safe (``None``, boolean, number, or string).
    """

    field: str
    widget: str
    label: str
    help: str = ""
    placeholder: str = ""
    options: tuple[str, ...] = ()
    default: Any = None
    order: int = 0
    group: str = ""

    def __post_init__(self) -> None:
        object.__setattr__(self, "field", require_identifier(self.field, "ui.field"))
        object.__setattr__(self, "widget", require_identifier(self.widget, "ui.widget"))
        if self.widget not in _WIDGETS:
            raise ValueError(f"ui.widget must be one of: {', '.join(sorted(_WIDGETS))}")
        object.__setattr__(self, "label", require_string(self.label, "ui.label"))
        object.__setattr__(self, "help", _optional_string(self.help, "ui.help"))
        object.__setattr__(
            self,
            "placeholder",
            _optional_string(self.placeholder, "ui.placeholder"),
        )
        options = tuple(require_string(value, "ui.option") for value in self.options)
        if len(options) != len(set(options)):
            raise ValueError("ui.options must be unique")
        if self.widget == "select" and not options:
            raise ValueError("select ui elements require options")
        object.__setattr__(self, "options", options)
        freeze_json(self.default, "ui.default")
        object.__setattr__(self, "order", self.order)
        if isinstance(self.order, bool) or not isinstance(self.order, int):
            raise ValueError("ui.order must be an integer")
        object.__setattr__(self, "group", _optional_string(self.group, "ui.group"))

    def to_dict(self) -> dict[str, Any]:
        return {
            "field": self.field,
            "widget": self.widget,
            "label": self.label,
            "help": self.help,
            "placeholder": self.placeholder,
            "options": list(self.options),
            "default": self.default,
            "order": self.order,
            "group": self.group,
        }

    @classmethod
    def from_dict(cls, value: object) -> UIElement:
        if not isinstance(value, Mapping):
            raise ValueError("ui element must be an object")
        require_exact_keys(
            value,
            {
                "field",
                "widget",
                "label",
                "help",
                "placeholder",
                "options",
                "default",
                "order",
                "group",
            },
            "ui element",
        )
        options = value["options"]
        if not isinstance(options, list):
            raise ValueError("ui.options must be an array")
        return cls(
            field=value["field"],  # type: ignore[arg-type]
            widget=value["widget"],  # type: ignore[arg-type]
            label=value["label"],  # type: ignore[arg-type]
            help=value["help"],  # type: ignore[arg-type]
            placeholder=value["placeholder"],  # type: ignore[arg-type]
            options=tuple(options),
            default=value["default"],
            order=value["order"],  # type: ignore[arg-type]
            group=value["group"],  # type: ignore[arg-type]
        )


def ui_elements_from_list(value: Sequence[object], field: str) -> tuple[UIElement, ...]:
    """Validate and freeze a manifest ``ui_elements`` declaration."""
    elements: list[UIElement] = []
    for item in value:
        if isinstance(item, UIElement):
            elements.append(item)
        elif isinstance(item, Mapping):
            elements.append(UIElement.from_dict(item))
        else:
            raise ValueError(f"{field} must contain UIElement values")
    names = [element.field for element in elements]
    if len(names) != len(set(names)):
        raise ValueError(f"{field} fields must be unique")
    return tuple(elements)
